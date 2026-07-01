package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"
)

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
		f.Write(b)
	} else {
		if err := writeFileAtomic(filePath, b, 0644); err != nil {
			return "", fmt.Errorf("failed to write inventory to %s: %w", filePath, err)
		}
	}

	return filePath, nil
}

func printText(data []HostComponents, quiet bool) {
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
		fmt.Printf("Host: %s\n\n", hd.Hostname)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "------------------------------------------------------------------------------------------------------------------------------------------------")
		fmt.Fprintln(w, "| device\t| device type\t| instances\t| hw certified\t| drv certified\t| fw certified\t| hcl link\t|")
		fmt.Fprintln(w, "------------------------------------------------------------------------------------------------------------------------------------------------")
		
		for _, res := range hd.Results {
			fmt.Fprintf(w, "| %s\t| %s\t| %d\t| %s\t| %s\t| %s\t| %s\t|\n",
				res.Device, res.DeviceType, res.Instances, res.Certified, res.DriverCertified, res.FirmwareCertified, res.HCLLink)
		}
		w.Flush()
		fmt.Printf("\n---\n\n")
	}

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
}

func printJSON(data []HostComponents, quiet bool) {
	var allIssues []MissingDetail
	for i := range data {
		if !quiet {
			allIssues = append(allIssues, data[i].Issues...)
		}
		data[i].Issues = nil
	}

	out := struct {
		Results []HostComponents `json:"results"`
		Issues  []MissingDetail  `json:"issues,omitempty"`
	}{
		Results: data,
		Issues:  allIssues,
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
	}
}
