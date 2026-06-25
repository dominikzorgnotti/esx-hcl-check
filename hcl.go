package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// performHCLChecks processes the raw inventory and maps it to Broadcom search queries.
func performHCLChecks(rawInventory []RawHostData, releaseVersion string, details, debugPci bool, vsanHclPath string) []HostComponents {
	
	// Ensure we have an up-to-date offline vSAN database
	vsanDB, err := loadVsanHCL(vsanHclPath)
	if err != nil {
		fmt.Printf("Warning: Failed to load or download vSAN HCL database: %v\n", err)
	}

	var results []HostComponents

	for _, raw := range rawInventory {
		hostComp := HostComponents{
			Datacenter: raw.Datacenter,
			Cluster:    raw.Cluster,
			Hostname:   raw.Hostname,
		}

		// 1. System Chassis
		sysFullModel := fmt.Sprintf("%s %s", raw.SysVendor, raw.SysModel)
		sysFilters := []map[string]interface{}{{"displayKey": "productReleaseVersion", "filterValues": []string{releaseVersion}}}
		sysCertified := queryBroadcomAPI("server", sysFilters, []string{raw.SysModel}, releaseVersion)

		sysRes := buildSystemQuery(sysFullModel, raw.SysModel, releaseVersion)
		sysRes.Certified = sysCertified
		sysRes.Firmware = raw.BiosVersion
		sysRes.DriverCertified = "N/A"
		sysRes.FirmwareCertified = "N/A"
		hostComp.Results = append(hostComp.Results, sysRes)

		// 2. CPU
		cpuKeyword := raw.CpuModel
		if raw.CpuId != "" { cpuKeyword = raw.CpuId }
		cpuFilters := []map[string]interface{}{{"displayKey": "productReleaseVersion", "filterValues": []string{releaseVersion}}}
		cpuCertified := queryBroadcomAPI("cpu", cpuFilters, []string{cpuKeyword}, releaseVersion)

		cpuRes := buildCPUQuery(raw.CpuModel, raw.CpuId, releaseVersion)
		if details && raw.CpuId != "" { cpuRes.CPUID = raw.CpuId }
		cpuRes.Certified = cpuCertified
		cpuRes.DriverCertified = "N/A"
		cpuRes.FirmwareCertified = "N/A"
		hostComp.Results = append(hostComp.Results, cpuRes)

		// 3. PCI Devices
		type pciKey struct {
			VID, DID, SVID, SSID int16
			FW, DV, DN           string
		}
		pciMap := make(map[pciKey]int)

		for _, pci := range raw.PCIDevices {
			k := pciKey{VID: pci.VID, DID: pci.DID, SVID: pci.SVID, SSID: pci.SSID, FW: pci.Firmware, DV: pci.DriverVer, DN: pci.DriverName}
			
			if idx, found := pciMap[k]; found {
				hostComp.Results[idx].Instances++
			} else {
				vidHex := fmt.Sprintf("%04x", uint16(pci.VID))
				didHex := fmt.Sprintf("%04x", uint16(pci.DID))
				svidHex := fmt.Sprintf("%04x", uint16(pci.SVID))
				ssidHex := fmt.Sprintf("%04x", uint16(pci.SSID))

				res := HCLResult{
					Device:     pci.DeviceName,
					DeviceType: pci.DeviceType,
					Instances:  1,
					Firmware:   pci.Firmware,
					DriverVer:  pci.DriverVer,
					DriverName: pci.DriverName,
					DriverCertified: "N/A",
					FirmwareCertified: "N/A",
				}

				if details {
					res.VID, res.DID, res.SVID, res.SSID = vidHex, didHex, svidHex, ssidHex
				}

				if pci.DeviceType != "unknown (debug)" {
					// Check vSAN Offline DB First
					foundInVsan := false
					if vsanDB != nil {
						foundInVsan = evaluateVsanPCI(vsanDB, vidHex, didHex, svidHex, ssidHex, releaseVersion, &res)
					}
					
					// Fallback to Broadcom API if not found in vSAN DB
					if !foundInVsan {
						filters := []map[string]interface{}{
							{"displayKey": "vid", "filterValues": []string{vidHex}},
							{"displayKey": "did", "filterValues": []string{didHex}},
							{"displayKey": "svid", "filterValues": []string{svidHex}},
							{"displayKey": "ssid", "filterValues": []string{ssidHex}},
						}
						res.Certified = queryBroadcomAPI("io", filters, []string{}, releaseVersion)
						res.HCLLink = buildHexQueryURL(releaseVersion, int16(pci.VID), int16(pci.DID), int16(pci.SVID), int16(pci.
