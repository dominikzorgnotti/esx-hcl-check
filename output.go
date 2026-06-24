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
		fmt.Fprintln(w, "----------------------------------------------------------------------------------------------------------------------------------------------------------------")
		fmt.Fprintln(w, "| device\t| device type\t| number of instances\t| current firmware\t| driver name\t| current driver version\t| certified\t| hcl\t|")
		fmt.Fprintln(w, "----------------------------------------------------------------------------------------------------------------------------------------------------------------")
		
		for _, res := range hd.Results {
			fmt.Fprintf(w, "| %s\t| %s\t| %d\t| %s\t| %s\t| %s\t| %s\t| %s\t|\n",
				res.Device, res.DeviceType, res.Instances, res.Firmware, res.DriverName, res.DriverVer, res.Certified, res.HCLLink)
		}
		w.Flush()
		fmt.Printf("\n---\n\n")
	}
}

func printJSON(data []HostComponents) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		fmt.Fprintf(os.Stderr, "Error encoding JSON: %v\n", err)
	}
}
