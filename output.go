package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

// saveRawInventory writes the RawHostData array to a JSON file.
func saveRawInventory(data []RawHostData, targetPath string) (string, error) {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal raw data: %w", err)
	}

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
		fmt.Fprintln(w, "Hostname\tdevice\tdevice type\tcertified\thcl")
		
		for _, res := range hd.Results {
			certStr := "FALSE"
			if res.Certified {
				certStr = "TRUE"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				res.Hostname, res.Device, res.DeviceType, certStr, res.HCLLink)
		}
		w.Flush()
		fmt.Printf("\n---\n\n")
	}
}

func printJSON(data []HostComponents) {
	jsonData, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(jsonData))
}
