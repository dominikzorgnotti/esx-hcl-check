package main

import (
	"fmt"
	"net/url"
)

// performHCLChecks processes the raw inventory and maps it to Broadcom search queries.
func performHCLChecks(rawInventory []RawHostData, releaseVersion string, details, debugPci bool) []HostComponents {
	var results []HostComponents

	for _, raw := range rawInventory {
		hostComp := HostComponents{
			Datacenter: raw.Datacenter,
			Cluster:    raw.Cluster,
			Hostname:   raw.Hostname,
		}

		// 1. System Chassis
		sysFullModel := fmt.Sprintf("%s %s", raw.SysVendor, raw.SysModel)
		
		// Use sysFullModel for the output table display, but ONLY raw.SysModel for the URL query keyword
		hostComp.Results = append(hostComp.Results, buildSystemQuery(sysFullModel, raw.SysModel, releaseVersion))

		// 2. CPU
		cpuRes := buildCPUQuery(raw.CpuModel, raw.CpuId, releaseVersion)
		if details && raw.CpuId != "" {
			cpuRes.CPUID = raw.CpuId
		}
		hostComp.Results = append(hostComp.Results, cpuRes)

		// 3. PCI Devices (with Deduplication)
		type pciKey struct {
			VID  int16
			DID  int16
			SSID int16
		}
		
		pciMap := make(map[pciKey]int)

		for _, pci := range raw.PCIDevices {
			k := pciKey{VID: pci.VID, DID: pci.DID, SSID: pci.SSID}
			
			if idx, found := pciMap[k]; found {
				hostComp.Results[idx].Instances++
			} else {
				hclURL := ""
				// If we dumped all devices via -debugpci, don't try to build HCL URLs for RAM/USB bridges
				if pci.DeviceType != "unknown (debug)" {
					hclURL = buildHexQueryURL(releaseVersion, pci.VID, pci.DID, pci.SSID)
				}

				res := HCLResult{
					Device:     pci.DeviceName,
					DeviceType: pci.DeviceType,
					Instances:  1,
					Certified:  "",
					HCLLink:    hclURL,
				}

				if details {
					res.VID = fmt.Sprintf("%04x", uint16(pci.VID))
					res.DID = fmt.Sprintf("%04x", uint16(pci.DID))
					res.SSID = fmt.Sprintf("%04x", uint16(pci.SSID))
				}

				hostComp.Results = append(hostComp.Results, res)
				pciMap[k] = len(hostComp.Results) - 1
			}
		}
		results = append(results, hostComp)
	}
	return results
}

// buildHexQueryURL translates decimal PCI IDs into hex and constructs the Broadcom URL.
func buildHexQueryURL(releaseVersion string, vid, did, ssid int16) string {
	baseURL := "https://compatibilityguide.broadcom.com/search"

	params := url.Values{}
	params.Set("program", "io")
	params.Set("persona", "live")
	params.Set("column", "brandName")
	params.Set("order", "asc")
	params.Set("productReleaseVersion", fmt.Sprintf("[%s]", releaseVersion))
	
	params.Set("vid", fmt.Sprintf("[%04x]", uint16(vid)))
	params.Set("did", fmt.Sprintf("[%04x]", uint16(did)))
	params.Set("maxSsid", fmt.Sprintf("[%04x]", uint16(ssid)))

	return fmt.Sprintf("%s?%s", baseURL, params.Encode())
}

// buildSystemQuery now takes both a display model and a specific search keyword
func buildSystemQuery(displayModel, searchKeyword, releaseVersion string) HCLResult {
	params := url.Values{}
	params.Set("program", "server")
	params.Set("persona", "live")
	params.Set("keyword", searchKeyword)
	params.Set("productReleaseVersion", fmt.Sprintf("[%s]", releaseVersion))

	return HCLResult{
		Device:     displayModel,
		DeviceType: "system",
		Instances:  1,
		Certified:  "",
		HCLLink:    "https://compatibilityguide.broadcom.com/search?" + params.Encode(),
	}
}

func buildCPUQuery(cpuModel, cpuId, releaseVersion string) HCLResult {
	params := url.Values{}
	params.Set("program", "cpu")
	params.Set("persona", "live")
	params.Set("column", "cpuSeries")
	params.Set("order", "asc")

	keyword := cpuModel
	if cpuId != "" {
		keyword = cpuId
	}
	params.Set("keyword", keyword)

	return HCLResult{
		Device:     cpuModel,
		DeviceType: "CPU",
		Instances:  1,
		Certified:  "",
		HCLLink:    "https://compatibilityguide.broadcom.com/search?" + params.Encode(),
	}
}
