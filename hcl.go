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
						res.HCLLink = buildHexQueryURL(releaseVersion, int16(pci.VID), int16(pci.DID), int16(pci.SVID), int16(pci.SSID))
					}
				}

				hostComp.Results = append(hostComp.Results, res)
				pciMap[k] = len(hostComp.Results) - 1
			}
		}

		// 4. SSD/HDD Disks
		type diskKey struct { Vendor, Model, Firmware string }
		diskMap := make(map[diskKey]int)

		for _, disk := range raw.Disks {
			k := diskKey{Vendor: disk.Vendor, Model: disk.Model, Firmware: disk.Firmware}

			if idx, found := diskMap[k]; found {
				hostComp.Results[idx].Instances++
			} else {
				res := HCLResult{
					Device:     disk.DeviceName,
					DeviceType: disk.DeviceType,
					Instances:  1,
					Firmware:   disk.Firmware,
					DriverCertified: "N/A",
					FirmwareCertified: "N/A",
				}

				foundInVsan := false
				if vsanDB != nil {
					foundInVsan = evaluateVsanDisk(vsanDB, disk.Vendor, disk.Model, releaseVersion, &res)
				}

				if !foundInVsan {
					if disk.DeviceType == "vSAN NVMe PCIe (beta)" {
						res.HCLLink = buildVsanNvmeQueryURL(disk.Vendor, disk.Model, releaseVersion)
					} else {
						res.HCLLink = buildDiskQueryURL(disk.Vendor, disk.Model, releaseVersion)
					}
					filters := []map[string]interface{}{}
					cleanVen := strings.TrimSpace(disk.Vendor)
					
					// FIX: Do not pass generic "NVMe" to the Broadcom API to prevent HTTP 400 Bad Request errors
					if cleanVen != "" && !strings.EqualFold(cleanVen, "NVMe") {
						filters = append(filters, map[string]interface{}{"displayKey": "partnerName", "filterValues": []string{cleanVen}})
					}
					res.Certified = queryBroadcomAPI("vsan", filters, []string{disk.Model}, releaseVersion)
				}

				hostComp.Results = append(hostComp.Results, res)
				diskMap[k] = len(hostComp.Results) - 1
			}
		}

		results = append(results, hostComp)
	}
	return results
}

// -------------------------------------------------------------
// vSAN Offline DB Engine
// -------------------------------------------------------------

func loadVsanHCL(path string) (*VsanOfflineDB, error) {
	needsDownload := false
	
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		needsDownload = true
	} else if err == nil {
		b, err := os.ReadFile(path)
		if err == nil {
			var db VsanOfflineDB
			if err := json.Unmarshal(b, &db); err == nil {
				if time.Now().Unix() - db.Timestamp > 86400 {
					needsDownload = true
				}
			} else {
				needsDownload = true
			}
		}
	}

	if needsDownload {
		resp, err := http.Get("https://vvs.broadcom.com/service/vsan/all.json")
		if err == nil && resp.StatusCode == 200 {
			defer resp.Body.Close()
			b, _ := io.ReadAll(resp.Body)
			os.WriteFile(path, b, 0644)
		}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var db VsanOfflineDB
	if err := json.Unmarshal(b, &db); err != nil {
		return nil, err
	}
	return &db, nil
}

func evaluateVsanPCI(db *VsanOfflineDB, vid, did, svid, ssid, release string, res *HCLResult) bool {
	checkList := append(db.Data.Controller, db.Data.Nic...)
	
	for _, item := range checkList {
		if strings.EqualFold(fmt.Sprintf("%v", item["vid"]), vid) &&
		   strings.EqualFold(fmt.Sprintf("%v", item["did"]), did) &&
		   strings.EqualFold(fmt.Sprintf("%v", item["svid"]), svid) &&
		   strings.EqualFold(fmt.Sprintf("%v", item["ssid"]), ssid) {
			
			res.HCLLink = fmt.Sprintf("%v", item["vcglink"])
			res.Certified = "FALSE"

			releases, ok := item["releases"].(map[string]interface{})
			if !ok { return true }

			relData, exists := releases[release]
			if !exists { return true }
			
			res.Certified = "TRUE"
			
			if res.DriverName != "" { res.DriverCertified = "FALSE" }
			if res.Firmware != "" { res.FirmwareCertified = "FALSE" }

			cleanHostDrvVer := strings.TrimSpace(res.DriverVer)
			cleanHostFw := strings.TrimSpace(res.Firmware)

			for drvName, drvObj := range relData.(map[string]interface{}) {
				if drvName == "vsanSupport" || drvName == "deviceSupport" { continue }
				
				for drvVer, drvDetails := range drvObj.(map[string]interface{}) {
					res.SupportedDrivers = append(res.SupportedDrivers, fmt.Sprintf("%s %s", drvName, drvVer))
					
					hclBaseDrvVer := strings.Split(drvVer, "-")[0]
					if strings.Contains(strings.ToLower(res.DriverName), strings.ToLower(drvName)) {
						if strings.EqualFold(cleanHostDrvVer, drvVer) || strings.EqualFold(cleanHostDrvVer, hclBaseDrvVer) {
							res.DriverCertified = "TRUE"
						}
					}

					if fwList, ok := drvDetails.(map[string]interface{})["firmwares"].([]interface{}); ok {
						for _, fwItem := range fwList {
							fwStr := strings.TrimSpace(fmt.Sprintf("%v", fwItem.(map[string]interface{})["firmware"]))
							res.SupportedFirmwares = append(res.SupportedFirmwares, fwStr)
							
							if cleanHostFw != "" && strings.
