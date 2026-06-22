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
		hostComp.Results = append(hostComp.Results, buildCPUQuery(raw.Hostname, raw.CpuModel, releaseVersion))

		// 3. PCI Devices
		for _, pci := range raw.PCIDevices {
			hclURL := buildHexQueryURL(releaseVersion, pci.VID, pci.DID, pci.SSID)
			hostComp.Results = append(hostComp.Results, HCLResult{
				Hostname:   raw.Hostname,
				Device:     pci.DeviceName,
				DeviceType: pci.DeviceType,
				Certified:  true, // Placeholder for future scraper logic
				HCLLink:    hclURL,
			})
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
	
	// Convert the signed int16 from VMware into an unsigned int16 to guarantee valid hex output
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
		Certified:  true,
		HCLLink:    "https://compatibilityguide.broadcom.com/search?" + params.Encode(),
	}
}

func buildCPUQuery(hostname, cpuModel, releaseVersion string) HCLResult {
	params := url.Values{}
	params.Set("program", "cpu")
	params.Set("persona", "live")
	params.Set("keyword", cpuModel)

	return HCLResult{
		Hostname:   hostname,
		Device:     cpuModel,
		DeviceType: "CPU",
		Certified:  true,
		HCLLink:    "https://compatibilityguide.broadcom.com/search?" + params.Encode(),
	}
}
