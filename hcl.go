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
	"strconv"
	"strings"
	"sync"
	"time"
)

// queryCache memoizes Broadcom baseline lookups across hosts within a single
// run. Identical hardware on many hosts (a common cluster shape) otherwise
// issues one identical network call per host; the API result depends only on
// the query inputs, so caching by those inputs is exact rather than heuristic.
// The mutex makes it safe for concurrent evaluation.
type queryCache struct {
	mu       sync.Mutex
	results  map[string]CertStatus
	liveTime time.Duration // total wall time spent in live (uncached) queries
}

func newQueryCache() *queryCache {
	return &queryCache{results: make(map[string]CertStatus)}
}

// cacheKey canonicalizes the query inputs. json.Marshal sorts map keys, so the
// encoding is deterministic for the same logical filter set.
func cacheKey(programId string, filters []map[string]interface{}, keywords []string, targetRelease string) string {
	fb, _ := json.Marshal(filters)
	kb, _ := json.Marshal(keywords)
	return programId + "\x00" + targetRelease + "\x00" + string(fb) + "\x00" + string(kb)
}

// get returns a cached verdict or performs the live Broadcom lookup and caches
// it.
func (c *queryCache) get(programId string, filters []map[string]interface{}, keywords []string, targetRelease string) CertStatus {
	return c.getWith(queryBroadcomAPI, programId, filters, keywords, targetRelease)
}

// getWith is the testable core of get: it takes the query function so tests can
// inject a fake and assert dedup behaviour without hitting the network.
// Transient failures (CertError) are not cached, so a blip on the first host
// does not poison every identical device in the run — later occurrences retry.
func (c *queryCache) getWith(query func(string, []map[string]interface{}, []string, string) CertStatus,
	programId string, filters []map[string]interface{}, keywords []string, targetRelease string) CertStatus {
	key := cacheKey(programId, filters, keywords, targetRelease)

	c.mu.Lock()
	v, ok := c.results[key]
	c.mu.Unlock()
	if ok {
		return v
	}

	t0 := time.Now()
	v = query(programId, filters, keywords, targetRelease)
	elapsed := time.Since(t0)

	c.mu.Lock()
	c.liveTime += elapsed
	if v != CertError {
		c.results[key] = v
	}
	c.mu.Unlock()
	return v
}

// Broadcom HCL source endpoints, centralized so the preflight check, the
// downloader, and the user-facing messages all reference the same URLs.
const (
	vsanHCLURL           = "https://vvs.broadcom.com/service/vsan/all.json"
	broadcomGuideBaseURL = "https://compatibilityguide.broadcom.com"
	broadcomAPIURL       = "https://compatibilityguide.broadcom.com/compguide/programs/viewResults?limit=20&page=1&sortBy=&sortType=ASC"
)

// broadcomHTTPClient bounds all outbound Broadcom HCL calls (vSAN DB download and
// the compatibility-guide API) so a slow or hung endpoint cannot stall the scan
// indefinitely. Runs are sequential and per-device, so a missing timeout here is
// the single biggest hang risk in large environments.
var broadcomHTTPClient = &http.Client{Timeout: 30 * time.Second}

type hclEndpoint struct{ name, url string }

// hclEndpoints are the online sources the preflight probes.
var hclEndpoints = []hclEndpoint{
	{"Broadcom Compatibility Guide", broadcomGuideBaseURL},
	{"vSAN offline HCL database", vsanHCLURL},
}

// checkConnectivity probes the online HCL sources and returns a human-readable
// failure line for each one that is unreachable (empty slice = all reachable).
func checkConnectivity(timeout time.Duration) []string {
	return checkEndpoints(&http.Client{Timeout: timeout}, hclEndpoints)
}

// checkEndpoints is the testable core of checkConnectivity. A transport error
// (DNS, timeout, refused, proxy failure) means unreachable; any HTTP response —
// even 4xx/5xx — means the host is reachable, which is all the preflight needs.
func checkEndpoints(client *http.Client, endpoints []hclEndpoint) []string {
	var failures []string
	for _, ep := range endpoints {
		req, err := http.NewRequest("HEAD", ep.url, nil)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", ep.name, ep.url, err))
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", ep.name, ep.url, err))
			continue
		}
		resp.Body.Close()
	}
	return failures
}

const (
	// broadcomMaxAttempts bounds retries against the external Broadcom endpoints,
	// which are occasionally flaky or rate-limited. broadcomBackoffBase is the
	// first delay; it doubles each retry (500ms, 1s, ...).
	broadcomMaxAttempts = 3
	broadcomBackoffBase = 500 * time.Millisecond
)

// isRetryableStatus reports whether an HTTP status is worth retrying: transient
// server-side or rate-limit conditions. A 4xx (other than 429) is the endpoint
// telling us the request itself is wrong, so retrying would not help.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	return false
}

// doBroadcomWithRetry issues a request built by newReq through broadcomHTTPClient
// with bounded exponential backoff, retrying on transport errors and retryable
// HTTP statuses (429, 5xx). newReq is called fresh for each attempt so a request
// body is safely replayable. On success — or on a non-retryable status — it
// returns the response for the caller to inspect and close; if every attempt
// fails it returns the last error.
func doBroadcomWithRetry(newReq func() (*http.Request, error)) (*http.Response, error) {
	var lastErr error
	for attempt := 1; attempt <= broadcomMaxAttempts; attempt++ {
		req, err := newReq()
		if err != nil {
			return nil, err
		}

		resp, err := broadcomHTTPClient.Do(req)
		switch {
		case err != nil:
			lastErr = err
		case isRetryableStatus(resp.StatusCode):
			lastErr = fmt.Errorf("broadcom returned HTTP %d", resp.StatusCode)
			resp.Body.Close()
		default:
			return resp, nil
		}

		if attempt < broadcomMaxAttempts {
			time.Sleep(broadcomBackoffBase * time.Duration(1<<(attempt-1)))
		}
	}
	return nil, lastErr
}

// performHCLChecks processes the raw inventory and maps it to Broadcom search queries.
// When offline is true, no live Broadcom API calls are made: components that can
// only be verified via the API are marked CertSkipped, while components found in
// the local vSAN offline DB still get a real verdict. If stats is non-nil, it
// records the vSAN DB load time, the total live Broadcom query time, and the
// number of skipped checks.
func performHCLChecks(rawInventory []RawHostData, releaseVersion string, details, debugPci, offline bool, vsanHclPath string, stats *Stats, ws *warnSink) []HostComponents {

	// Ensure we have an up-to-date offline vSAN database
	vsanStart := time.Now()
	vsanDB, err := loadVsanHCL(vsanHclPath, offline, ws)
	if stats != nil {
		stats.VsanDBQueryMs = time.Since(vsanStart).Milliseconds()
	}
	if err != nil {
		ws.add(fmt.Sprintf("Failed to load or download vSAN HCL database: %v", err))
	}

	var results []HostComponents

	// Memoize Broadcom baseline lookups so identical hardware across hosts is
	// queried once, not once per host.
	qc := newQueryCache()

	for _, raw := range rawInventory {
		hostComp := HostComponents{
			Source:     raw.Source,
			Datacenter: raw.Datacenter,
			Cluster:    raw.Cluster,
			Hostname:   raw.Hostname,
			SkipReason: raw.SkipReason,
		}

		// A skipped host/cluster has no hardware to evaluate — carry the reason
		// through to the output and move on.
		if raw.SkipReason != "" {
			results = append(results, hostComp)
			continue
		}

		// 1. System Chassis (API-only: no local DB, so offline -> SKIPPED)
		sysFullModel := fmt.Sprintf("%s %s", raw.SysVendor, raw.SysModel)
		sysRes := buildSystemQuery(sysFullModel, raw.SysModel, releaseVersion)
		sysRes.Firmware = raw.BiosVersion
		sysRes.DriverCertified = CertNA
		sysRes.FirmwareCertified = CertNA
		if offline {
			sysRes.Certified = CertSkipped
		} else {
			sysFilters := []map[string]interface{}{{"displayKey": "productReleaseVersion", "filterValues": []string{releaseVersion}}}
			sysRes.Certified = qc.get("server", sysFilters, []string{raw.SysModel}, releaseVersion)
		}
		hostComp.Results = append(hostComp.Results, sysRes)

		// 2. CPU (API-only: no local DB, so offline -> SKIPPED)
		cpuRes := buildCPUQuery(raw.CpuModel, raw.CpuId, releaseVersion)
		if details && raw.CpuId != "" {
			cpuRes.CPUID = raw.CpuId
		}
		cpuRes.DriverCertified = CertNA
		cpuRes.FirmwareCertified = CertNA
		if offline {
			cpuRes.Certified = CertSkipped
		} else {
			cpuKeyword := raw.CpuModel
			if raw.CpuId != "" {
				cpuKeyword = raw.CpuId
			}
			cpuFilters := []map[string]interface{}{{"displayKey": "productReleaseVersion", "filterValues": []string{releaseVersion}}}
			cpuRes.Certified = qc.get("cpu", cpuFilters, []string{cpuKeyword}, releaseVersion)
		}
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
					Device:            pci.DeviceName,
					DeviceType:        pci.DeviceType,
					Instances:         1,
					Firmware:          pci.Firmware,
					DriverVer:         pci.DriverVer,
					DriverName:        pci.DriverName,
					DriverCertified:   CertNA,
					FirmwareCertified: CertNA,
				}

				if details {
					res.VID, res.DID, res.SVID, res.SSID = vidHex, didHex, svidHex, ssidHex
				}

				// Track missing firmware/driver for storage HBAs
				if pci.DeviceType == "io card (fc)" || pci.DeviceType == "io card (raid)" {
					var missing []string
					if pci.Firmware == "" {
						missing = append(missing, "firmware")
					}
					if pci.DriverVer == "" {
						missing = append(missing, "driver")
					}
					if len(missing) > 0 {
						reason := "vSphere API pre-9.1 does not expose HBA firmware/driver"
						if isAPIVersionAtLeast(raw.APIVersion, "9.1") {
							reason = "vSphere 9.1 SOAP call returned no firmware/driver for this device"
						}
						hostComp.Issues = append(hostComp.Issues, MissingDetail{Hostname: raw.Hostname, Device: pci.DeviceName, Missing: missing, Reason: reason})
					}
				}

				if pci.DeviceType != "unknown (debug)" {
					// Check vSAN Offline DB First
					foundInVsan := false
					if vsanDB != nil {
						foundInVsan = evaluateVsanPCI(vsanDB, vidHex, didHex, svidHex, ssidHex, releaseVersion, &res)
					}

					// Fallback to Broadcom API if not found in vSAN DB (skipped offline)
					if !foundInVsan {
						res.HCLLink = buildHexQueryURL(releaseVersion, int16(pci.VID), int16(pci.DID), int16(pci.SVID), int16(pci.SSID))
						if offline {
							res.Certified = CertSkipped
						} else {
							filters := []map[string]interface{}{
								{"displayKey": "vid", "filterValues": []string{vidHex}},
								{"displayKey": "did", "filterValues": []string{didHex}},
								{"displayKey": "svid", "filterValues": []string{svidHex}},
								{"displayKey": "ssid", "filterValues": []string{ssidHex}},
							}
							res.Certified = qc.get("io", filters, []string{}, releaseVersion)
						}
					}
				}

				hostComp.Results = append(hostComp.Results, res)
				pciMap[k] = len(hostComp.Results) - 1
			}
		}

		// 4. SSD/HDD Disks
		type diskKey struct{ Vendor, Model, Firmware, DriverName, DriverVer string }
		diskMap := make(map[diskKey]int)

		for _, disk := range raw.Disks {
			k := diskKey{Vendor: disk.Vendor, Model: disk.Model, Firmware: disk.Firmware, DriverName: disk.DriverName, DriverVer: disk.DriverVer}

			if idx, found := diskMap[k]; found {
				hostComp.Results[idx].Instances++
			} else {
				res := HCLResult{
					Device:            disk.DeviceName,
					DeviceType:        disk.DeviceType,
					Instances:         1,
					Firmware:          disk.Firmware,
					DriverName:        disk.DriverName,
					DriverVer:         disk.DriverVer,
					DriverCertified:   CertNA,
					FirmwareCertified: CertNA,
				}

				// Track missing firmware for vSAN disks
				if disk.Firmware == "" {
					reason := "SCSI firmware revision not reported by vSphere API"
					if disk.DeviceType == "vSAN NVMe PCIe" {
						reason = "NVMe controller not found in vSphere topology data"
					}
					hostComp.Issues = append(hostComp.Issues, MissingDetail{Hostname: raw.Hostname, Device: disk.DeviceName, Missing: []string{"firmware"}, Reason: reason})
				}

				// Track missing driver version for NVMe controllers (PCIe controller and correlated SSD entries)
				if (disk.DeviceType == "vSAN NVMe PCIe" || (disk.DeviceType == "vSAN SSD" && disk.DriverName != "")) && disk.DriverVer == "" {
					reason := "vSphere API pre-9.1 does not expose NVMe controller driver version"
					if isAPIVersionAtLeast(raw.APIVersion, "9.1") {
						reason = "vSphere 9.1 SOAP call returned no driver version for this NVMe controller"
					}
					hostComp.Issues = append(hostComp.Issues, MissingDetail{Hostname: raw.Hostname, Device: disk.DeviceName, Missing: []string{"driver"}, Reason: reason})
				}

				foundInVsan := false
				if vsanDB != nil {
					// For NVMe PCIe controllers, check the controller DB by PCI ID first —
					// this gives driver arrays + firmware arrays, which the SSD DB does not have.
					if disk.DeviceType == "vSAN NVMe PCIe" && (disk.VID != 0 || disk.DID != 0) {
						vidHex := fmt.Sprintf("%04x", uint16(disk.VID))
						didHex := fmt.Sprintf("%04x", uint16(disk.DID))
						svidHex := fmt.Sprintf("%04x", uint16(disk.SVID))
						ssidHex := fmt.Sprintf("%04x", uint16(disk.SSID))
						foundInVsan = evaluateVsanPCI(vsanDB, vidHex, didHex, svidHex, ssidHex, releaseVersion, &res)
					}
					if !foundInVsan {
						foundInVsan = evaluateVsanDisk(vsanDB, disk.Vendor, disk.Model, releaseVersion, &res)
					}
				}

				if !foundInVsan {
					if disk.DeviceType == "vSAN NVMe PCIe" {
						res.HCLLink = buildVsanNvmeQueryURL(disk.Vendor, disk.Model, releaseVersion)
					} else {
						res.HCLLink = buildDiskQueryURL(disk.Vendor, disk.Model, releaseVersion)
					}

					if offline {
						res.Certified = CertSkipped
					} else {
						filters := []map[string]interface{}{}
						cleanVen := strings.TrimSpace(disk.Vendor)

						// Do not pass generic "NVMe" to the Broadcom API to prevent HTTP 400 Bad Request errors.
						// Short/ambiguous vendor names (e.g. "HP") can also trigger 400; we retry without them below.
						if cleanVen != "" && !strings.EqualFold(cleanVen, "NVMe") {
							filters = append(filters, map[string]interface{}{"displayKey": "partnerName", "filterValues": []string{cleanVen}})
						}
						res.Certified = qc.get("vsan", filters, []string{disk.Model}, releaseVersion)

						// Retry without the partner filter if the API rejected the request
						if res.Certified == CertError && len(filters) > 0 {
							res.Certified = qc.get("vsan", nil, []string{disk.Model}, releaseVersion)
						}
					}
				}

				hostComp.Results = append(hostComp.Results, res)
				diskMap[k] = len(hostComp.Results) - 1
			}
		}

		results = append(results, hostComp)
	}

	if stats != nil {
		stats.BroadcomQueryMs = qc.liveTime.Milliseconds()
		for _, host := range results {
			for _, res := range host.Results {
				if res.Certified == CertSkipped {
					stats.SkippedChecks++
				}
			}
		}
	}
	return results
}

// -------------------------------------------------------------
// vSAN Offline DB Engine
// -------------------------------------------------------------

func loadVsanHCL(path string, offline bool, ws *warnSink) (*VsanOfflineDB, error) {
	needsDownload := false

	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		needsDownload = true
	} else if err == nil {
		b, err := os.ReadFile(path)
		if err == nil {
			var db VsanOfflineDB
			if err := json.Unmarshal(b, &db); err == nil {
				if time.Now().Unix()-db.Timestamp > 86400 {
					needsDownload = true
				}
			} else {
				needsDownload = true
			}
		}
	}

	if needsDownload && offline {
		// Cannot refresh in offline mode. If a (possibly stale) copy exists we
		// use it below; if none exists, guide the user to fetch it by hand.
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("vSAN HCL database not found at %q and -offline prevents downloading it.\n"+
				"  Download it manually from %s and save it to that path, or pass -vsanhcl <path>", path, vsanHCLURL)
		}
		ws.add(fmt.Sprintf("vSAN HCL database at %q may be stale; -offline prevents refreshing it.", path))
	}

	if needsDownload && !offline {
		resp, err := doBroadcomWithRetry(func() (*http.Request, error) {
			return http.NewRequest("GET", vsanHCLURL, nil)
		})
		if err == nil && resp.StatusCode == 200 {
			b, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			// Only cache the download if it is a complete, parseable DB with a
			// timestamp. This prevents a truncated or corrupt body (e.g. a
			// mid-transfer drop) from clobbering a previously-good cache.
			if readErr == nil && len(b) > 0 {
				var check VsanOfflineDB
				if json.Unmarshal(b, &check) == nil && check.Timestamp > 0 {
					_ = writeFileAtomic(path, b, 0644)
				}
			}
		} else if resp != nil {
			resp.Body.Close()
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

// parseESXiRelease extracts (major, minor, update) from a vSAN HCL release key
// like "ESXi 8.0 U3" or "ESXi 9.1". A missing update is 0. ok is false if the
// string can't be parsed as a version.
func parseESXiRelease(s string) (maj, min, upd int, ok bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimSpace(strings.TrimPrefix(s, "esxi"))
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return 0, 0, 0, false
	}
	vparts := strings.Split(fields[0], ".")
	if len(vparts) < 2 {
		return 0, 0, 0, false
	}
	maj, e1 := strconv.Atoi(vparts[0])
	min, e2 := strconv.Atoi(vparts[1])
	if e1 != nil || e2 != nil {
		return 0, 0, 0, false
	}
	if len(fields) > 1 {
		if n, e := strconv.Atoi(strings.TrimPrefix(fields[1], "u")); e == nil {
			upd = n
		}
	}
	return maj, min, upd, true
}

// releaseLess reports whether release a sorts before b. Unparseable values fall
// back to a plain string comparison so ordering is still deterministic.
func releaseLess(a, b string) bool {
	amaj, amin, aupd, aok := parseESXiRelease(a)
	bmaj, bmin, bupd, bok := parseESXiRelease(b)
	if !aok || !bok {
		return a < b
	}
	if amaj != bmaj {
		return amaj < bmaj
	}
	if amin != bmin {
		return amin < bmin
	}
	return aupd < bupd
}

// maxSupportedRelease returns the highest release key in a vSAN HCL entry's
// "releases" map — i.e. the newest ESXi release the device is certified for.
func maxSupportedRelease(releases map[string]interface{}) string {
	best := ""
	for k := range releases {
		if best == "" || releaseLess(best, k) {
			best = k
		}
	}
	return best
}

func evaluateVsanPCI(db *VsanOfflineDB, vid, did, svid, ssid, release string, res *HCLResult) bool {
	// Search the controller and SSD categories by PCI ID (NVMe PCIe SSDs can
	// appear under SSD). Entries without vid/did fields simply won't match.
	//
	// The vSAN "nic" category is deliberately excluded: it is the vSAN RDMA NIC
	// HCL (program=rdmanic), a separate and more restrictive list than the
	// general I/O device HCL. Matching a general NIC against it produced false
	// negatives — e.g. an Intel E810-C reported as only "ESXi 7.0 U3" when the
	// I/O device HCL certifies it through ESXi 9.1. NICs therefore fall through
	// to the live Broadcom I/O device API (program=io) instead. Building a fresh
	// slice also avoids aliasing db.Data.Controller's backing array.
	checkList := append(append([]map[string]interface{}{}, db.Data.Controller...), db.Data.Ssd...)

	for _, item := range checkList {
		if strings.EqualFold(fmt.Sprintf("%v", item["vid"]), vid) &&
			strings.EqualFold(fmt.Sprintf("%v", item["did"]), did) &&
			strings.EqualFold(fmt.Sprintf("%v", item["svid"]), svid) &&
			strings.EqualFold(fmt.Sprintf("%v", item["ssid"]), ssid) {

			res.HCLLink = fmt.Sprintf("%v", item["vcglink"])
			res.Certified = CertFalse

			releases, ok := item["releases"].(map[string]interface{})
			if !ok {
				return true
			}

			// The device is in the DB — record the newest release it supports,
			// which is the actionable answer when the target release is not.
			res.MaxSupportedRelease = maxSupportedRelease(releases)

			relData, exists := releases[release]
			if !exists {
				return true
			}

			res.Certified = CertTrue
			cleanHostDrvVer := strings.TrimSpace(res.DriverVer)
			cleanHostFw := strings.TrimSpace(res.Firmware)

			if relMap, ok := relData.(map[string]interface{}); ok {
				// Controller/NIC-style releases: keyed by driver name → version → {firmwares:[…]}
				if res.DriverName != "" && res.DriverVer != "" {
					res.DriverCertified = CertFalse
				}
				if res.Firmware != "" {
					res.FirmwareCertified = CertFalse
				}

				for drvName, drvObj := range relMap {
					if drvName == "vsanSupport" || drvName == "deviceSupport" {
						continue
					}
					drvVersions, ok := drvObj.(map[string]interface{})
					if !ok {
						continue
					}
					for drvVer, drvDetails := range drvVersions {
						res.SupportedDrivers = append(res.SupportedDrivers, fmt.Sprintf("%s %s", drvName, drvVer))
						hclBaseDrvVer := strings.Split(drvVer, "-")[0]
						if strings.Contains(strings.ToLower(res.DriverName), strings.ToLower(drvName)) {
							// Prefix match handles build-qualifier suffixes: installed
							// "1.4.0.8-1vmw.910.0.25370933" matches HCL "1.4.0.8-1vmw.910".
							if strings.EqualFold(cleanHostDrvVer, drvVer) ||
								strings.EqualFold(cleanHostDrvVer, hclBaseDrvVer) ||
								strings.HasPrefix(strings.ToLower(cleanHostDrvVer), strings.ToLower(drvVer)) {
								res.DriverCertified = CertTrue
							}
						}
						if detailMap, ok := drvDetails.(map[string]interface{}); ok {
							if fwList, ok := detailMap["firmwares"].([]interface{}); ok {
								for _, fwItem := range fwList {
									if fwMap, ok := fwItem.(map[string]interface{}); ok {
										fwStr := strings.TrimSpace(fmt.Sprintf("%v", fwMap["firmware"]))
										res.SupportedFirmwares = append(res.SupportedFirmwares, fwStr)
										if cleanHostFw != "" && strings.EqualFold(cleanHostFw, fwStr) {
											res.FirmwareCertified = CertTrue
										}
									}
								}
							}
						}
					}
				}
			} else if relArr, ok := relData.([]interface{}); ok {
				// SSD/HDD-style releases: [{firmware: "…"}, …] — no driver arrays
				if res.Firmware != "" {
					res.FirmwareCertified = CertFalse
				}
				for _, fwItem := range relArr {
					if fwMap, ok := fwItem.(map[string]interface{}); ok {
						fwStr := strings.TrimSpace(fmt.Sprintf("%v", fwMap["firmware"]))
						res.SupportedFirmwares = append(res.SupportedFirmwares, fwStr)
						if cleanHostFw != "" && strings.EqualFold(cleanHostFw, fwStr) {
							res.FirmwareCertified = CertTrue
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
		itemProductId := strings.ToLower(fmt.Sprintf("%v", item["productid"]))
		itemModel := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", item["model"])))
		itemPartner := strings.ToLower(fmt.Sprintf("%v", item["partnername"]))

		// 1. Exact productid match — vSphere reports the part number as the model string,
		//    which maps directly to productid in the HCL (e.g. "MO000800JWFWP").
		//    Product IDs are globally unique so no partner check is required.
		matchFound := strings.EqualFold(cleanModel, itemProductId)

		// 2. Model/partner fuzzy matching fallback
		if !matchFound {
			partnerOK := strings.Contains(itemPartner, cleanVendor) || strings.Contains(cleanModel, itemPartner)
			if !partnerOK {
				continue
			}
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
		}

		if matchFound {
			res.HCLLink = fmt.Sprintf("%v", item["vcglink"])
			res.Certified = CertFalse

			releases, ok := item["releases"].(map[string]interface{})
			if !ok {
				return true
			}

			// The device is in the DB — record the newest release it supports.
			res.MaxSupportedRelease = maxSupportedRelease(releases)

			relData, exists := releases[release]
			if !exists {
				return true
			}

			res.Certified = CertTrue
			if res.Firmware != "" {
				res.FirmwareCertified = CertFalse
			}
			cleanHostFw := strings.TrimSpace(res.Firmware)

			if fwList, ok := relData.([]interface{}); ok {
				// SATA/SAS SSD-style: flat array [{firmware:"…"}, …]
				for _, fwItem := range fwList {
					if fwMap, ok := fwItem.(map[string]interface{}); ok {
						fwStr := strings.TrimSpace(fmt.Sprintf("%v", fwMap["firmware"]))
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
							res.FirmwareCertified = CertTrue
						}
					}
				}
			} else if relMap, ok := relData.(map[string]interface{}); ok {
				// NVMe SSD-style: same map structure as controller entries
				// (NVMe SSDs in data.ssd use driverName → version → {firmwares:[…]})
				cleanHostDrvVer := strings.TrimSpace(res.DriverVer)
				if res.DriverName != "" && res.DriverVer != "" {
					res.DriverCertified = CertFalse
				}
				for drvName, drvObj := range relMap {
					if drvName == "vsanSupport" || drvName == "deviceSupport" {
						continue
					}
					drvVersions, ok := drvObj.(map[string]interface{})
					if !ok {
						continue
					}
					for drvVer, drvDetails := range drvVersions {
						res.SupportedDrivers = append(res.SupportedDrivers, fmt.Sprintf("%s %s", drvName, drvVer))
						hclBaseDrvVer := strings.Split(drvVer, "-")[0]
						if strings.Contains(strings.ToLower(res.DriverName), strings.ToLower(drvName)) {
							if strings.EqualFold(cleanHostDrvVer, drvVer) ||
								strings.EqualFold(cleanHostDrvVer, hclBaseDrvVer) ||
								strings.HasPrefix(strings.ToLower(cleanHostDrvVer), strings.ToLower(drvVer)) {
								res.DriverCertified = CertTrue
							}
						}
						if detailMap, ok := drvDetails.(map[string]interface{}); ok {
							if fwList2, ok := detailMap["firmwares"].([]interface{}); ok {
								for _, fwItem := range fwList2 {
									if fwMap, ok := fwItem.(map[string]interface{}); ok {
										fwStr := strings.TrimSpace(fmt.Sprintf("%v", fwMap["firmware"]))
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
											res.FirmwareCertified = CertTrue
										}
									}
								}
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

// -------------------------------------------------------------
// Legacy API & Aggregation Functions
// -------------------------------------------------------------

func queryBroadcomAPI(programId string, filters []map[string]interface{}, keywords []string, targetRelease string) CertStatus {
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
	resp, err := doBroadcomWithRetry(func() (*http.Request, error) {
		r, e := http.NewRequest("POST", broadcomAPIURL, bytes.NewReader(jsonData))
		if e == nil {
			r.Header.Set("Content-Type", "application/json")
		}
		return r, e
	})
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return CertError
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	json.Unmarshal(bodyBytes, &result)

	data, ok := result["data"].(map[string]interface{})
	if !ok {
		return CertError
	}

	countFloat, ok := data["count"].(float64)
	if !ok || countFloat == 0 {
		return CertFalse
	}

	bodyStr := string(bodyBytes)
	if strings.Contains(bodyStr, targetRelease) {
		return CertTrue
	}

	if programId == "vsan" && !strings.Contains(targetRelease, "vSAN") {
		vsanVer := strings.Replace(targetRelease, "ESXi", "vSAN", 1)
		vsanTarget := fmt.Sprintf("%s (%s)", targetRelease, vsanVer)
		if strings.Contains(bodyStr, vsanTarget) {
			return CertTrue
		}
	}

	return CertFalse
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

	issuesSeen := make(map[string]bool)
	var aggregatedIssues []MissingDetail
	for _, host := range data {
		for _, issue := range host.Issues {
			key := issue.Hostname + "|" + issue.Device
			if !issuesSeen[key] {
				issuesSeen[key] = true
				aggregatedIssues = append(aggregatedIssues, issue)
			}
		}
	}

	// Preserve the source (vCenter) in the aggregated record for consistency with
	// per-host output. Collect the distinct sources so a multi-vCenter run lists
	// all of them, sorted for deterministic output.
	sourceSet := make(map[string]bool)
	for _, host := range data {
		if host.Source != "" {
			sourceSet[host.Source] = true
		}
	}
	sources := make([]string, 0, len(sourceSet))
	for s := range sourceSet {
		sources = append(sources, s)
	}
	sort.Strings(sources)

	return []HostComponents{{
		Source:     strings.Join(sources, ", "),
		Datacenter: "Global",
		Cluster:    "(Aggregated Deduplication)",
		Hostname:   "All Scanned Hosts",
		Results:    aggregatedResults,
		Issues:     aggregatedIssues,
	}}
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
	if cpuId != "" {
		keyword = cpuId
	}
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
