package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

// saveRawInventory writes the RawHostData array to a JSON file.
func saveRawInventory(data []RawHostData, targetPath string) (string, error) {
	// Use a buffer and json.Encoder to prevent HTML escaping of URLs/characters (\u0026)
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
			return "", fmt.Errorf("failed to write to temp file: %w", err)
		}
	} else {
		if err := os.WriteFile(filePath, b, 0644); err != nil {
			return "", fmt.Errorf("failed to write file %s: %w", filePath, err)
		}
	}

	return filePath, nil
}

func printText(data []HostComponents) {
	for _, hd := range data {
		fmt.Printf("Datacenter: %s\n", hd.Datacenter)
		clusterName := hd.Cluster
		if clusterName == "" {
			clusterName = "(Standalone)"
		}
		fmt.Printf("Cluster: %s\n", clusterName)
		fmt.Printf("Host: %s\n\n", hd.Hostname)

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "------------------------")
		// Added "number of instances" column
		fmt.Fprintln(w, "Hostname\tdevice\tdevice type\tnumber of instances\tcertified\thcl")
		
		for _, res := range hd.Results {
			// Certified is now empty, but we still print the column dynamically
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
				res.Hostname, res.Device, res.DeviceType, res.Instances, res.Certified, res.HCLLink)
		}
		w.Flush()
		fmt.Printf("\n---\n\n")
	}
}

func printJSON(data []HostComponents) {
	// Use json.Encoder to disable HTML escaping directly to stdout
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
	}
}
