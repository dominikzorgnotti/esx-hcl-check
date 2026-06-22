package main

import (
	"fmt"
	"net/url"
)

// performHCLChecks processes the raw inventory and maps it to Broadcom search queries.
func performHCLChecks(rawInventory []RawHostData, releaseVersion string) []HostComponents {
	var results []HostComponents

	for _, raw := range rawInventory {
		hostComp := HostComponents{
			Datacenter: raw.Datacenter,
			Cluster:    raw.Cluster,
			Hostname:   raw.Hostname,
		}

		// 1. System Chassis
		sysFullModel := fmt.Sprintf("%s %s", raw.SysVendor, raw.SysModel)
		hostComp.Results = append(hostComp.Results, buildSystemQuery(raw.Hostname, sysFullModel, releaseVersion))

		// 2. CPU
		hostComp.Results = append(hostComp.Results, buildCPUQuery(raw.Hostname, raw.CpuModel, raw.CpuId, releaseVersion))

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
				hclURL := buildHexQueryURL(releaseVersion, pci.VID, pci.DID, pci.SSID)
				hostComp.Results = append(hostComp.Results, HCLResult{
					Hostname:   raw.Hostname,
					Device:     pci.DeviceName,
					DeviceType: pci.DeviceType,
					Instances:  1,
					Certified:  "",
					HCLLink:    hclURL,
				})
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

func buildSystemQuery(hostname, model, releaseVersion string) HCLResult {
	params := url.Values{}
	params.Set("program", "server")
	params.Set("persona", "live")
	params.Set("keyword", model)
	params.Set("productReleaseVersion", fmt.Sprintf("[%s]", releaseVersion))

	return HCLResult{
		Hostname:   hostname,
		Device:     model,
		DeviceType: "system",
		Instances:  1,
		Certified:  "",
		HCLLink:    "https://compatibilityguide.broadcom.com/search?" + params.Encode(),
	}
}

func buildCPUQuery(hostname, cpuModel, cpuId, releaseVersion string) HCLResult {
	params := url.Values{}
	params.Set("program", "cpu")
	params.Set("persona", "live")
	params.Set("column", "cpuSeries")
	params.Set("order", "asc")

	// Use CPUID hex string if parsed, fallback to string model
	keyword := cpuModel
	if cpuId != "" {
		keyword = cpuId
	}
	params.Set("keyword", keyword)

	return HCLResult{
		Hostname:   hostname,
		Device:     cpuModel, // Output human-readable model in table
		DeviceType: "CPU",
		Instances:  1,
		Certified:  "",
		HCLLink:    "https://compatibilityguide.broadcom.com/search?" + params.Encode(),
	}
}
