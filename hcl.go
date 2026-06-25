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
							
							if cleanHostFw != "" && strings.EqualFold(cleanHostFw, fwStr) {
								res.FirmwareCertified = "TRUE"
							}
						}
					}
				}
			}
			return true
		}
	}
	return false
}

func evaluateVsanDisk(db *VsanOfflineDB, vendor, model, release string, res *HCLResult) bool {
	checkList := append(db.Data.Ssd, db.Data.Hdd...)
	
	cleanVendor := strings.ToLower(strings.TrimSpace(vendor))
	cleanModel := strings.ToLower(strings.TrimSpace(model))
	
	// FIX: Handle vSphere's generic "NVMe" vendor translation by extracting the true vendor from the model
	if cleanVendor == "nvme" || cleanVendor == "" {
		parts := strings.Fields(cleanModel)
		if len(parts) > 0 {
			cleanVendor = parts[0] // e.g. "dell"
		}
	}

	// Tokenize the vSphere model string to find strong alphanumeric identifiers (e.g. P4510, PM1725b)
	var strongTokens []string
	for _, token := range strings.Fields(cleanModel) {
		if len(token) > 3 && strings.ContainsAny(token, "0123456789") {
			// Filter out generic capacity tokens
			if !strings.HasSuffix(strings.ToUpper(token), "TB") && !strings.HasSuffix(strings.ToUpper(token), "GB") {
				strongTokens = append(strongTokens, token)
			}
		}
	}

	for _, item := range checkList {
		itemModel := strings.ToLower(fmt.Sprintf("%v", item["model"]))
		itemPartner := strings.ToLower(fmt.Sprintf("%v", item["partnername"]))
		
		// Check Partner
		if !strings.Contains(itemPartner, cleanVendor) && !strings.Contains(cleanModel, itemPartner) {
			continue
		}

		// Check Model via direct substring or strong token overlap
		matchFound := false
		if strings.Contains(itemModel, cleanModel) || strings.Contains(cleanModel, itemModel) {
			matchFound = true
		} else {
			for _, token := range strongTokens {
				if strings.Contains(itemModel, token) {
					matchFound = true
					break
				}
			}
		}

		if matchFound {
			res.HCLLink = fmt.Sprintf("%v", item["vcglink"])
			res.Certified = "FALSE"

			releases, ok := item["releases"].(map[string]interface{})
			if !ok { return true }

			relData, exists := releases[release]
			if !exists { return true }

			res.Certified = "TRUE"
			if res.Firmware != "" { res.FirmwareCertified = "FALSE" }
			cleanHostFw := strings.TrimSpace(res.Firmware)

			if fwList, ok := relData.([]interface{}); ok {
				for _, fwItem := range fwList {
					fwStr := strings.TrimSpace(fmt.Sprintf("%v", fwItem.(map[string]interface{})["firmware"]))
					
					// Deduplicate the array when pushing to supported_firmwares
					existsInList := false
					for _, existingFw := range res.SupportedFirmwares {
						if existingFw == fwStr {
							existsInList = true
							break
						}
					}
					if !existsInList {
						res.SupportedFirmwares = append(res.SupportedFirmwares, fwStr)
					}
					
					if cleanHostFw != "" && strings.EqualFold(cleanHostFw, fwStr) {
						res.FirmwareCertified = "TRUE"
					}
				}
			}
			return true
		}
	}
	return false
}

// -------------------------------------------------------------
// Legacy API & Aggregation Functions
// -------------------------------------------------------------

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

	jsonData, _ := json.Marshal(reqBody)
	urlStr := "https://compatibilityguide.broadcom.com/compguide/programs/viewResults?limit=20&page=1&sortBy=&sortType=ASC"
	resp, err := http.Post(urlStr, "application/json", bytes.NewBuffer(jsonData))
	if err != nil || resp.StatusCode != 200 {
		if resp != nil { resp.Body.Close() }
		return "ERROR"
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(bodyBytes, &result)

	data, ok := result["data"].(map[string]interface{})
	if !ok { return "ERROR" }

	countFloat, ok := data["count"].(float64)
	if !ok || countFloat == 0 { return "FALSE" }

	bodyStr := string(bodyBytes)
	if strings.Contains(bodyStr, targetRelease) { return "TRUE" }
	
	if programId == "vsan" && !strings.Contains(targetRelease, "vSAN") {
		vsanVer := strings.Replace(targetRelease, "ESXi", "vSAN", 1)
		vsanTarget := fmt.Sprintf("%s (%s)", targetRelease, vsanVer)
		if strings.Contains(bodyStr, vsanTarget) { return "TRUE" }
	}

	return "FALSE"
}

func aggregateUnique(data []HostComponents) []HostComponents {
	type aggKey struct {
		Device, DeviceType, HCLLink, VID, DID, SVID, SSID, CPUID, Firmware, DriverVer, DriverName string
	}

	aggMap := make(map[aggKey]HCLResult)

	for _, host := range data {
		for _, res := range host.Results {
			k := aggKey{res.Device, res.DeviceType, res.HCLLink, res.VID, res.DID, res.SVID, res.SSID, res.CPUID, res.Firmware, res.DriverVer, res.DriverName}
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

	return []HostComponents{{Datacenter: "Global", Cluster: "(Aggregated Deduplication)", Hostname: "All Scanned Hosts", Results: aggregatedResults}}
}

func buildHexQueryURL(releaseVersion string, vid, did, svid, ssid int16) string {
	u, _ := url.Parse("https://compatibilityguide.broadcom.com/search")
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
	u.RawQuery = params.Encode()
	return u.String()
}

func buildSystemQuery(displayModel, searchKeyword, releaseVersion string) HCLResult {
	u, _ := url.Parse("https://compatibilityguide.broadcom.com/search")
	params := url.Values{}
	params.Set("program", "server")
	params.Set("persona", "live")
	params.Set("keyword", searchKeyword)
	params.Set("productReleaseVersion", fmt.Sprintf("[%s]", releaseVersion))
	u.RawQuery = params.Encode()
	return HCLResult{Device: displayModel, DeviceType: "system", Instances: 1, HCLLink: u.String()}
}

func buildCPUQuery(cpuModel, cpuId, releaseVersion string) HCLResult {
	u, _ := url.Parse("https://compatibilityguide.broadcom.com/search")
	params := url.Values{}
	params.Set("program", "cpu")
	params.Set("persona", "live")
	params.Set("column", "cpuSeries")
	params.Set("order", "asc")
	keyword := cpuModel
	if cpuId != "" { keyword = cpuId }
	params.Set("keyword", keyword)
	u.RawQuery = params.Encode()
	return HCLResult{Device: cpuModel, DeviceType: "CPU", Instances: 1, HCLLink: u.String()}
}

func buildDiskQueryURL(vendor, model, releaseVersion string) string {
	u, _ := url.Parse("https://compatibilityguide.broadcom.com/search")
	params := url.Values{}
	params.Set("program", "ssd")
	params.Set("persona", "live")
	params.Set("column", "partnerName")
	params.Set("order", "asc")
	cleanVen := strings.TrimSpace(vendor)
	if cleanVen != "" && !strings.EqualFold(cleanVen, "NVMe") {
		params.Set("partners", fmt.Sprintf("[%s]", cleanVen))
	}
	params.Set("keyword", model)
	params.Set("productReleaseVersion", fmt.Sprintf("[%s]", releaseVersion))
	u.RawQuery = params.Encode()
	return u.String()
}

func buildVsanNvmeQueryURL(vendor, model, releaseVersion string) string {
	u, _ := url.Parse("https://compatibilityguide.broadcom.com/search")
	params := url.Values{}
	params.Set("program", "ssd")
	params.Set("persona", "live")
	params.Set("column", "partnerName")
	params.Set("order", "asc")
	cleanVen := strings.TrimSpace(vendor)
	if cleanVen != "" && !strings.EqualFold(cleanVen, "NVMe") {
		params.Set("partners", fmt.Sprintf("[%s]", cleanVen))
	}
	params.Set("keyword", model)
	vsanRelease := releaseVersion
	if !strings.Contains(vsanRelease, "vSAN") {
		vsanVer := strings.Replace(releaseVersion, "ESXi", "vSAN", 1)
		vsanRelease = fmt.Sprintf("%s (%s)", releaseVersion, vsanVer)
	}
	params.Set("supportedReleases", fmt.Sprintf("[%s]", vsanRelease))
	u.RawQuery = params.Encode()
	return u.String()
}
