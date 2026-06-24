package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
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
		sysFilters := []map[string]interface{}{
			{"displayKey": "productReleaseVersion", "filterValues": []string{releaseVersion}},
		}
		sysCertified := queryBroadcomAPI("server", sysFilters, []string{raw.SysModel}, releaseVersion)

		sysRes := buildSystemQuery(sysFullModel, raw.SysModel, releaseVersion)
		sysRes.Certified = sysCertified
		hostComp.Results = append(hostComp.Results, sysRes)

		// 2. CPU
		cpuKeyword := raw.CpuModel
		if raw.CpuId != "" {
			cpuKeyword = raw.CpuId
		}
		cpuFilters := []map[string]interface{}{
			{"displayKey": "productReleaseVersion", "filterValues": []string{releaseVersion}},
		}
		cpuCertified := queryBroadcomAPI("cpu", cpuFilters, []string{cpuKeyword}, releaseVersion)

		cpuRes := buildCPUQuery(raw.CpuModel, raw.CpuId, releaseVersion)
		if details && raw.CpuId != "" {
			cpuRes.CPUID = raw.CpuId
		}
		cpuRes.Certified = cpuCertified
		hostComp.Results = append(hostComp.Results, cpuRes)

		// 3. PCI Devices
		type pciKey struct {
			VID  int16
			DID  int16
			SVID int16
			SSID int16
		}
		
		pciMap := make(map[pciKey]int)

		for _, pci := range raw.PCIDevices {
			k := pciKey{VID: pci.VID, DID: pci.DID, SVID: pci.SVID, SSID: pci.SSID}
			
			if idx, found := pciMap[k]; found {
				hostComp.Results[idx].Instances++
			} else {
				hclURL := ""
				certifiedStatus := ""
				
				vidHex := fmt.Sprintf("%04x", uint16(pci.VID))
				didHex := fmt.Sprintf("%04x", uint16(pci.DID))
				svidHex := fmt.Sprintf("%04x", uint16(pci.SVID))
				ssidHex := fmt.Sprintf("%04x", uint16(pci.SSID))

				if pci.DeviceType != "unknown (debug)" {
					hclURL = buildHexQueryURL(releaseVersion, int16(pci.VID), int16(pci.DID), int16(pci.SVID), int16(pci.SSID))
					
					// Rebuilt filter logic targeting precise API display keys
					filters := []map[string]interface{}{
						{"displayKey": "vid", "filterValues": []string{vidHex}},
						{"displayKey": "did", "filterValues": []string{didHex}},
						{"displayKey": "svid", "filterValues": []string{svidHex}},
						{"displayKey": "ssid", "filterValues": []string{ssidHex}},
					}
					certifiedStatus = queryBroadcomAPI("io", filters, []string{}, releaseVersion)
				}

				res := HCLResult{
					Device:     pci.DeviceName,
					DeviceType: pci.DeviceType,
					Instances:  1,
					Firmware:   pci.Firmware,
					Certified:  certifiedStatus,
					HCLLink:    hclURL,
				}

				if details {
					res.VID = vidHex
					res.DID = didHex
					res.SVID = svidHex
					res.SSID = ssidHex
				}

				hostComp.Results = append(hostComp.Results, res)
				pciMap[k] = len(hostComp.Results) - 1
			}
		}

		// 4. SSD Disks
		type diskKey struct {
			Vendor   string
			Model    string
			Firmware string
		}

		diskMap := make(map[diskKey]int)

		for _, disk := range raw.Disks {
			k := diskKey{Vendor: disk.Vendor, Model: disk.Model, Firmware: disk.Firmware}

			if idx, found := diskMap[k]; found {
				hostComp.Results[idx].Instances++
			} else {
				var hclURL string
				
				if disk.DeviceType == "vSAN NVMe PCIe (beta)" {
					hclURL = buildVsanNvmeQueryURL(disk.Vendor, disk.Model, releaseVersion)
				} else {
					hclURL = buildDiskQueryURL(disk.Vendor, disk.Model, releaseVersion)
				}
				
				filters := []map[string]interface{}{}
				if disk.Vendor != "" {
					filters = append(filters, map[string]interface{}{
						"displayKey": "partnerName",
						"filterValues": []string{disk.Vendor},
					})
				}
				
				certifiedStatus := queryBroadcomAPI("vsan", filters, []string{disk.Model}, releaseVersion)

				res := HCLResult{
					Device:     disk.DeviceName,
					DeviceType: disk.DeviceType,
					Instances:  1,
					Firmware:   disk.Firmware,
					Certified:  certifiedStatus,
					HCLLink:    hclURL,
				}

				hostComp.Results = append(hostComp.Results, res)
				diskMap[k] = len(hostComp.Results) - 1
			}
		}

		results = append(results, hostComp)
	}
	return results
}

// queryBroadcomAPI sends a POST request to the Broadcom JSON endpoint and parses the certification status.
func queryBroadcomAPI(programId string, filters []map[string]interface{}, keywords []string, targetRelease string) string {
	type bcmRequest struct {
		ProgramId string                   `json:"programId"`
		Filters   []map[string]interface{} `json:"filters"`
		Keyword   []string                 `json:"keyword"`
		Date      map[string]string        `json:"date"`
	}

	reqBody := bcmRequest{
		ProgramId: programId,
		Filters:   filters,
		Keyword:   keywords,
		Date:      map[string]string{"startDate": "", "endDate": ""},
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "ERROR"
	}

	url := "https://compatibilityguide.broadcom.com/compguide/programs/viewResults?limit=20&page=1&sortBy=&sortType=ASC"
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonData))
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return "ERROR"
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "ERROR"
	}

	var result map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return "ERROR"
	}

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return "ERROR"
	}

	countFloat, ok := data["count"].(float64)
	if !ok || countFloat == 0 {
		return "FALSE"
	}

	bodyStr := string(bodyBytes)
	if strings.Contains(bodyStr, targetRelease) {
		return "TRUE"
	}
	
	if programId == "vsan" && !strings.Contains(targetRelease, "vSAN") {
		vsanVer := strings.Replace(targetRelease, "ESXi", "vSAN", 1)
		vsanTarget := fmt.Sprintf("%s (%s)", targetRelease, vsanVer)
		if strings.Contains(bodyStr, vsanTarget) {
			return "TRUE"
		}
	}

	return "FALSE"
}

// aggregateUnique flattens and deduplicates all results globally across the environment.
func aggregateUnique(data []HostComponents) []HostComponents {
	type aggKey struct {
		Device     string
		DeviceType string
		HCLLink    string
		VID        string
		DID        string
		SVID       string
		SSID       string
		CPUID      string
		Firmware   string
	}

	aggMap := make(map[aggKey]HCLResult)

	for _, host := range data {
		for _, res := range host.Results {
			k := aggKey{
				Device:     res.Device,
				DeviceType: res.DeviceType,
				HCLLink:    res.HCLLink,
				VID:        res.VID,
				DID:        res.DID,
				SVID:       res.SVID,
				SSID:       res.SSID,
				CPUID:      res.CPUID,
				Firmware:   res.Firmware,
			}

			if existing, found := aggMap[k]; found {
				existing.Instances += res.Instances
				aggMap[k] = existing
			} else {
				aggMap[k] = res
			}
		}
	}

	var aggregatedResults []HCLResult
	for _, res := range aggMap {
		aggregatedResults = append(aggregatedResults, res)
	}

	sort.Slice(aggregatedResults, func(i, j int) bool {
		if aggregatedResults[i].DeviceType == aggregatedResults[j].DeviceType {
			return aggregatedResults[i].Device < aggregatedResults[j].Device
		}
		return aggregatedResults[i].DeviceType < aggregatedResults[j].DeviceType
	})

	return []HostComponents{
		{
			Datacenter: "Global",
			Cluster:    "(Aggregated Deduplication)",
			Hostname:   "All Scanned Hosts",
			Results:    aggregatedResults,
		},
	}
}

// buildHexQueryURL translates decimal PCI IDs into hex and constructs the Broadcom URL.
func buildHexQueryURL(releaseVersion string, vid, did, svid, ssid int16) string {
	baseURL := "https://compatibilityguide.broadcom.com/search"

	params := url.Values{}
	params.Set("program", "io")
	params.Set("persona", "live")
	params.Set("column", "brandName")
	params.Set("order", "asc")
	params.Set("productReleaseVersion", fmt.Sprintf("[%s]", releaseVersion))
	
	params.Set("vid", fmt.Sprintf("[%04x]", uint16(vid)))
	params.Set("did", fmt.Sprintf("[%04x]", uint16(did)))
	params.Set("svid", fmt.Sprintf("[%04x]", uint16(svid)))
	params.Set("ssid", fmt.Sprintf("[%04x]", uint16(ssid)))

	return fmt.Sprintf("%s?%s", baseURL, params.Encode())
}

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
		Firmware:   "",
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
		Firmware:   "",
		Certified:  "",
		HCLLink:    "https://compatibilityguide.broadcom.com/search?" + params.Encode(),
	}
}

func buildDiskQueryURL(vendor, model, releaseVersion string) string {
	baseURL := "https://compatibilityguide.broadcom.com/search"
	
	params := url.Values{}
	params.Set("program", "ssd")
	params.Set("persona", "live")
	params.Set("column", "partnerName")
	params.Set("order", "asc")
	
	if vendor != "" {
		params.Set("partners", fmt.Sprintf("[%s]", vendor))
	}
	
	params.Set("keyword", model)
	params.Set("productReleaseVersion", fmt.Sprintf("[%s]", releaseVersion))

	return fmt.Sprintf("%s?%s", baseURL, params.Encode())
}

func buildVsanNvmeQueryURL(vendor, model, releaseVersion string) string {
	baseURL := "https://compatibilityguide.broadcom.com/search"
	
	params := url.Values{}
	params.Set("program", "ssd")
	params.Set("persona", "live")
	params.Set("column", "partnerName")
	params.Set("order", "asc")
	
	if vendor != "" {
		params.Set("partners", fmt.Sprintf("[%s]", vendor))
	}
	
	params.Set("keyword", model)
	
	vsanRelease := releaseVersion
	if !strings.Contains(vsanRelease, "vSAN") {
		vsanVer := strings.Replace(releaseVersion, "ESXi", "vSAN", 1)
		vsanRelease = fmt.Sprintf("%s (%s)", releaseVersion, vsanVer)
	}
	params.Set("supportedReleases", fmt.Sprintf("[%s]", vsanRelease))

	return fmt.Sprintf("%s?%s", baseURL, params.Encode())
}
