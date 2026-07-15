package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

var csvHeader = []string{
	"source", "datacenter", "cluster", "hostname", "skip_reason",
	"device", "device_type", "number_of_instances",
	"current_firmware", "current_driver_version", "driver_name",
	"hw_certified", "driver_certified", "firmware_certified",
	"max_supported_release", "supported_drivers", "supported_firmwares",
	"vid", "did", "svid", "ssid", "cpu_id", "hcl",
}

// csvHeaderMap is the -map header: the same columns plus esx_device_name and
// link_state (after firmware_certified, mirroring the JSON field order). The
// number_of_instances column is kept for aggregated device classes but left
// blank on the per-adapter rows -map expands.
var csvHeaderMap = []string{
	"source", "datacenter", "cluster", "hostname", "skip_reason",
	"device", "device_type", "number_of_instances",
	"current_firmware", "current_driver_version", "driver_name",
	"hw_certified", "driver_certified", "firmware_certified",
	"esx_device_name", "link_state",
	"max_supported_release", "supported_drivers", "supported_firmwares",
	"vid", "did", "svid", "ssid", "cpu_id", "hcl",
}

// csvRows builds the CSV as a header plus one row per device. A skipped
// host/cluster (no results) becomes a context-only row so it is not lost.
// Extracted from printCSV so the row/column mapping is unit-testable. When
// mapMode is set, the esx_device_name/link_state columns are added and
// number_of_instances is blank for per-adapter rows (Instances 0).
func csvRows(data []HostComponents, mapMode bool) [][]string {
	header := csvHeader
	if mapMode {
		header = csvHeaderMap
	}
	rows := [][]string{header}
	for _, host := range data {
		if len(host.Results) == 0 {
			if host.SkipReason != "" {
				row := make([]string, len(header))
				row[0], row[1], row[2], row[3], row[4] = host.Source, host.Datacenter, host.Cluster, host.Hostname, host.SkipReason
				rows = append(rows, row)
			}
			continue
		}
		for _, r := range host.Results {
			// Instances 0 marks a per-adapter -map row; show it blank rather
			// than "0" so number_of_instances reads as "not applicable here".
			instances := ""
			if r.Instances > 0 {
				instances = strconv.Itoa(r.Instances)
			}
			if mapMode {
				rows = append(rows, []string{
					host.Source, host.Datacenter, host.Cluster, host.Hostname, host.SkipReason,
					r.Device, r.DeviceType, instances,
					r.Firmware, r.DriverVer, r.DriverName,
					r.Certified.String(), r.DriverCertified.String(), r.FirmwareCertified.String(),
					r.EsxName, r.LinkState,
					r.MaxSupportedRelease, strings.Join(r.SupportedDrivers, ";"), strings.Join(r.SupportedFirmwares, ";"),
					r.VID, r.DID, r.SVID, r.SSID, r.CPUID, r.HCLLink,
				})
				continue
			}
			rows = append(rows, []string{
				host.Source, host.Datacenter, host.Cluster, host.Hostname, host.SkipReason,
				r.Device, r.DeviceType, strconv.Itoa(r.Instances),
				r.Firmware, r.DriverVer, r.DriverName,
				r.Certified.String(), r.DriverCertified.String(), r.FirmwareCertified.String(),
				r.MaxSupportedRelease, strings.Join(r.SupportedDrivers, ";"), strings.Join(r.SupportedFirmwares, ";"),
				r.VID, r.DID, r.SVID, r.SSID, r.CPUID, r.HCLLink,
			})
		}
	}
	return rows
}

// printCSV writes one row per device to stdout — the same content as the JSON
// results, flattened for spreadsheets.
func printCSV(data []HostComponents, mapMode bool) {
	w := csv.NewWriter(os.Stdout)
	_ = w.WriteAll(csvRows(data, mapMode))
	if err := w.Error(); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing CSV: %v\n", err)
	}
}

// computeInventoryStats tallies the collected raw inventory: how many
// datacenters, clusters, hosts, IO cards, and storage devices were seen. It
// fills the count fields of a Stats; timing fields are set elsewhere.
func computeInventoryStats(inv []RawHostData) Stats {
	var s Stats
	dcSet := make(map[string]bool)
	clSet := make(map[string]bool)
	for _, h := range inv {
		if h.Datacenter != "" {
			dcSet[h.Datacenter] = true
		}
		if h.Cluster != "" {
			clSet[h.Cluster] = true
		}
		if h.SkipReason != "" {
			s.HostsSkipped++
			continue
		}
		s.Hosts++
		for _, pci := range h.PCIDevices {
			if strings.HasPrefix(pci.DeviceType, "io card") {
				s.IOCards++
			}
		}
		s.StorageDevices += len(h.Disks)
	}
	s.Datacenters = len(dcSet)
	s.Clusters = len(clSet)
	return s
}

// warnSink routes warnings by output mode: to stderr immediately in text mode
// (so they appear during the run), or collected into messages for inclusion in
// the JSON payload in JSON mode (so stdout stays a single valid JSON document).
type warnSink struct {
	json     bool
	messages []string
}

func (w *warnSink) add(msg string) {
	if w.json {
		w.messages = append(w.messages, msg)
	} else {
		fmt.Fprintln(os.Stderr, msg)
	}
}

// writeFileAtomic writes data to path atomically: it writes to a temporary file
// in the same directory, flushes it, then renames it into place. A crash or
// truncated write therefore never leaves a partially-written (corrupt) file at
// path — the reader sees either the old content or the complete new content.
// The rename is atomic on the same filesystem, and Go's os.Rename replaces an
// existing destination on both Unix and Windows.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we return before the rename succeeds.
	defer func() {
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	tmpName = "" // success — keep the renamed file
	return nil
}

// saveRawInventory writes the RawHostData array to a JSON file.
func saveRawInventory(data []RawHostData, targetPath string) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)

	if err := enc.Encode(data); err != nil {
		return "", fmt.Errorf("failed to marshal raw data: %w", err)
	}

	b := buf.Bytes()

	filePath := targetPath
	if filePath == "" {
		f, err := os.CreateTemp("", "esx_hardware_inventory_*.json")
		if err != nil {
			return "", fmt.Errorf("failed to create temp file: %w", err)
		}
		filePath = f.Name()
		defer f.Close()
		if _, err := f.Write(b); err != nil {
			return "", fmt.Errorf("failed to write inventory to %s: %w", filePath, err)
		}
	} else {
		if err := writeFileAtomic(filePath, b, 0644); err != nil {
			return "", fmt.Errorf("failed to write inventory to %s: %w", filePath, err)
		}
	}

	return filePath, nil
}

func printText(data []HostComponents, stats *Stats, quiet bool) {
	for _, hd := range data {
		if hd.Source != "" {
			fmt.Printf("vCenter: %s\n", hd.Source)
		}
		fmt.Printf("Datacenter: %s\n", hd.Datacenter)
		clusterName := hd.Cluster
		if clusterName == "" {
			clusterName = "(Standalone)"
		}
		fmt.Printf("Cluster: %s\n", clusterName)
		if hd.Hostname != "" {
			fmt.Printf("Host: %s\n", hd.Hostname)
		}
		if hd.SkipReason != "" {
			fmt.Printf("SKIPPED: %s\n\n---\n\n", hd.SkipReason)
			continue
		}
		fmt.Printf("\n")

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		const ruler = "----------------------------------------------------------------------------------------------------------------------------------"
		fmt.Fprintln(w, ruler)
		fmt.Fprintln(w, "| device\t| device type\t| instances\t| hw certified\t| drv certified\t| fw certified\t| max release\t|")
		fmt.Fprintln(w, ruler)

		for _, res := range hd.Results {
			fmt.Fprintf(w, "| %s\t| %s\t| %d\t| %s\t| %s\t| %s\t| %s\t|\n",
				res.Device, res.DeviceType, res.Instances, res.Certified, res.DriverCertified, res.FirmwareCertified, res.MaxSupportedRelease)
		}
		w.Flush()
		fmt.Printf("\n---\n\n")
	}

	// The text table is a high-level summary; the HCL link and hardware IDs live
	// in the richer machine formats.
	fmt.Println("For full detail (HCL link, hardware IDs, supported driver/firmware lists), re-run with -csv or -json.")
	fmt.Println()

	if quiet {
		return
	}

	type issueEntry struct {
		Hostname string
		Device   string
		Reason   string
	}
	seen := make(map[string]bool)
	var issueList []issueEntry
	for _, host := range data {
		for _, issue := range host.Issues {
			key := issue.Hostname + "|" + issue.Device
			if !seen[key] {
				seen[key] = true
				issueList = append(issueList, issueEntry{Hostname: issue.Hostname, Device: issue.Device, Reason: issue.Reason})
			}
		}
	}
	if len(issueList) > 0 {
		sort.Slice(issueList, func(i, j int) bool {
			if issueList[i].Hostname != issueList[j].Hostname {
				return issueList[i].Hostname < issueList[j].Hostname
			}
			return issueList[i].Device < issueList[j].Device
		})
		fmt.Println("Issues:")
		fmt.Println("Could not get firmware/driver information for the following devices")
		for _, e := range issueList {
			prefix := e.Device
			if e.Hostname != "" {
				prefix = fmt.Sprintf("%s on %s", e.Device, e.Hostname)
			}
			if e.Reason != "" {
				fmt.Printf("  * %s (Reason: %s)\n", prefix, e.Reason)
			} else {
				fmt.Printf("  * %s\n", prefix)
			}
		}
		fmt.Println()
	}

	if stats != nil {
		printStatsText(stats)
	}
}

// printStatsText renders the -stats section: inventory counts and runtime
// timings, aligned in two columns.
func printStatsText(s *Stats) {
	fmt.Println("Statistics:")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  Inventory:")
	fmt.Fprintf(w, "    Datacenters:\t%d\n", s.Datacenters)
	fmt.Fprintf(w, "    Clusters:\t%d\n", s.Clusters)
	fmt.Fprintf(w, "    Hosts:\t%d\n", s.Hosts)
	if s.HostsSkipped > 0 {
		fmt.Fprintf(w, "    Hosts skipped:\t%d\n", s.HostsSkipped)
	}
	fmt.Fprintf(w, "    IO cards:\t%d\n", s.IOCards)
	fmt.Fprintf(w, "    Storage devices:\t%d\n", s.StorageDevices)
	if s.SkippedChecks > 0 {
		fmt.Fprintf(w, "    Skipped checks:\t%d\n", s.SkippedChecks)
	}
	fmt.Fprintln(w, "  Runtime:")
	fmt.Fprintf(w, "    vCenter query:\t%d ms\n", s.VCenterQueryMs)
	fmt.Fprintf(w, "    Broadcom HCL query:\t%d ms\n", s.BroadcomQueryMs)
	fmt.Fprintf(w, "    vSAN DB query:\t%d ms\n", s.VsanDBQueryMs)
	w.Flush()
	fmt.Println()
}

// buildJSONOutput assembles the top-level -json payload: results, issues, and
// (when -stats is set) stats. Extracted from printJSON so the wire shape is
// unit-testable. Note: it clears per-host Issues (they are aggregated to the
// top level), mutating data in place, so call it once at output time.
func buildJSONOutput(data []HostComponents, warnings []string, stats *Stats, quiet bool) any {
	var allIssues []MissingDetail
	for i := range data {
		if !quiet {
			allIssues = append(allIssues, data[i].Issues...)
		}
		data[i].Issues = nil
	}

	return struct {
		Results  []HostComponents `json:"results"`
		Issues   []MissingDetail  `json:"issues,omitempty"`
		Warnings []string         `json:"warnings,omitempty"`
		Stats    *Stats           `json:"stats,omitempty"`
	}{
		Results:  data,
		Issues:   allIssues,
		Warnings: warnings,
		Stats:    stats,
	}
}

func printJSON(data []HostComponents, warnings []string, stats *Stats, quiet bool) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(buildJSONOutput(data, warnings, stats, quiet)); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
	}
}
