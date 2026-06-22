// esx-hcl-check allows a vSphere/VCF administrator to verify host hardware
// against the Broadcom Compatibility Guide.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"
)

// HCLResult represents the certification status of a single hardware component.
type HCLResult struct {
	Hostname   string `json:"hostname"`
	Device     string `json:"device"`
	DeviceType string `json:"device_type"`
	Certified  bool   `json:"certified"`
	HCLLink    string `json:"hcl"`
}

// HostComponents holds the contextual inventory data and hardware results for a single ESXi host.
type HostComponents struct {
	Datacenter string
	Cluster    string // Can be empty if standalone
	Hostname   string
	Results    []HCLResult
}

func main() {
	// Parse command-line flags and environmental variables.
	var (
		jsonOutput = flag.Bool("json", false, "Output in JSON format")
		dcTarget   = flag.String("dc", os.Getenv("GOVC_DATACENTER"), "Target datacenter (optional)")
		clsTarget  = flag.String("cluster", os.Getenv("GOVC_CLUSTER"), "Target cluster (optional)")
	)
	flag.Parse()

	// Use a background context for the lifecycle of the application.
	ctx := context.Background()

	// 1. Establish vCenter Connection.
	client, err := connectToVC(ctx)
	if err != nil {
		log.Fatalf("Error connecting to vCenter: %v", err)
	}
	// Ensure the session is logged out upon application exit.
	defer client.Logout(ctx)

	if !*jsonOutput {
		fmt.Printf("# Connecting to %s ...\n#\n\n", client.Client.URL().Host)
	}

	// 2. Discover Inventory and Hardware properties.
	hostDataList, err := discoverHostHardware(ctx, client, *dcTarget, *clsTarget)
	if err != nil {
		log.Fatalf("Error discovering inventory: %v", err)
	}

	// 3. Output formatting (Text or JSON).
	if *jsonOutput {
		printJSON(hostDataList)
	} else {
		printText(hostDataList)
	}
}

// connectToVC reads GOVC_* environmental variables and initializes the govmomi client.
func connectToVC(ctx context.Context) (*govmomi.Client, error) {
	vcURL := os.Getenv("GOVC_URL")
	if vcURL == "" {
		return nil, fmt.Errorf("GOVC_URL is not set. Please provide the vCenter URL")
	}

	u, err := url.Parse(vcURL)
	if err != nil {
		return nil, fmt.Errorf("invalid GOVC_URL format: %w", err)
	}

	// Embed credentials into the URL object if provided via environment.
	username := os.Getenv("GOVC_USERNAME")
	password := os.Getenv("GOVC_PASSWORD")
	if username != "" {
		u.User = url.UserPassword(username, password)
	}

	// Determine if we should bypass certificate verification.
	insecure := strings.ToLower(os.Getenv("GOVC_INSECURE")) == "true" || os.Getenv("GOVC_INSECURE") == "1"

	// Create the client and login.
	client, err := govmomi.NewClient(ctx, u, insecure)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate or connect to vCenter (check credentials and permissions): %w", err)
	}

	return client, nil
}

// discoverHostHardware traverses the vCenter inventory to find hosts based on DC/Cluster filters,
// retrieves hardware specifications, and runs the HCL checks.
func discoverHostHardware(ctx context.Context, client *govmomi.Client, dcTarget, clsTarget string) ([]HostComponents, error) {
	finder := find.NewFinder(client.Client, true)
	pc := property.DefaultCollector(client.Client)

	var allHostData []HostComponents

	// Find Datacenters. If no specific target is provided, find all ("*").
	dcQuery := "*"
	if dcTarget != "" {
		dcQuery = dcTarget
	}
	datacenters, err := finder.DatacenterList(ctx, dcQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to find datacenters: %w", err)
	}

	for _, dc := range datacenters {
		finder.SetDatacenter(dc) // Scope finder to current Datacenter

		// Find Clusters inside the Datacenter.
		clsQuery := "*"
		if clsTarget != "" {
			clsQuery = clsTarget
		}
		clusters, err := finder.ClusterComputeResourceList(ctx, clsQuery)
		if err != nil {
			// If a specific cluster was asked for and not found, return an error.
			if clsTarget != "" {
				return nil, fmt.Errorf("failed to find cluster %s in datacenter %s: %w", clsTarget, dc.Name(), err)
			}
			// If no clusters exist, just continue (we will fetch standalone hosts).
		}

		// 1. Process hosts within clusters.
		for _, cluster := range clusters {
			hosts, err := cluster.Hosts(ctx)
			if err != nil {
				continue
			}
			for _, hostRef := range hosts {
				hostData := processHost(ctx, pc, hostRef.Reference(), dc.Name(), cluster.Name())
				if hostData != nil {
					allHostData = append(allHostData, *hostData)
				}
			}
		}

		// 2. Process standalone hosts (in DC but not in a cluster) if no specific cluster was targeted.
		if clsTarget == "" {
			// Fetch ComputeResources (which include standalone hosts).
			compResources, err := finder.ComputeResourceList(ctx, "*")
			if err == nil {
				for _, cr := range compResources {
					// Skip if it's actually a ClusterComputeResource (already processed above)
					if cr.Reference().Type == "ClusterComputeResource" {
						continue
					}
					hosts, err := cr.Hosts(ctx)
					if err == nil {
						for _, hostRef := range hosts {
							hostData := processHost(ctx, pc, hostRef.Reference(), dc.Name(), "")
							if hostData != nil {
								allHostData = append(allHostData, *hostData)
							}
						}
					}
				}
			}
		}
	}

	return allHostData, nil
}

// processHost fetches the properties for a single host and builds the HCL results.
func processHost(ctx context.Context, pc *property.Collector, hostRef types.ManagedObjectReference, dcName, clsName string) *HostComponents {
	var hostMo mo.HostSystem

	// Retrieve properties needed: Name, connection state, and hardware info.
	err := pc.RetrieveOne(ctx, hostRef, []string{"name", "runtime.connectionState", "summary.hardware", "config.hardware"}, &hostMo)
	if err != nil {
		log.Printf("Warning: Failed to retrieve properties for host %v (Permission issues?): %v", hostRef, err)
		return nil
	}

	// Check if host is disconnected.
	if hostMo.Runtime.ConnectionState != types.HostSystemConnectionStateConnected {
		log.Printf("Warning: Host %s is not connected. Skipping hardware discovery.", hostMo.Name)
		return nil
	}

	hostData := HostComponents{
		Datacenter: dcName,
		Cluster:    clsName,
		Hostname:   hostMo.Name,
	}

	// Extract System Information
	if hostMo.Summary.Hardware != nil {
		sysModel := fmt.Sprintf("%s %s", hostMo.Summary.Hardware.Vendor, hostMo.Summary.Hardware.Model)
		hostData.Results = append(hostData.Results, checkBroadcomHCL(hostMo.Name, sysModel, "system", nil))
		
		cpuModel := hostMo.Summary.Hardware.CpuModel
		hostData.Results = append(hostData.Results, checkBroadcomHCL(hostMo.Name, cpuModel, "CPU", nil))
	}

	// Extract PCI Devices (IO Cards, GPUs, Storage Controllers)
	if hostMo.Config != nil && hostMo.Config.Hardware != nil {
		for _, pciDev := range hostMo.Config.Hardware.PciDevice {
			// We identify IO/GPU components by DID, VID, SSID, SVID for accurate querying.
			identifiers := map[string]int16{
				"VID":  pciDev.VendorId,
				"DID":  pciDev.DeviceId,
				"SVID": pciDev.SubVendorId,
				"SSID": pciDev.SubDeviceId,
			}
			
			// Simple heuristic: Devices with "Network", "Fibre Channel", "RAID", "Display/VGA" (GPU)
			devName := pciDev.DeviceName
			
			var devType string
			if strings.Contains(strings.ToLower(devName), "network") || strings.Contains(strings.ToLower(devName), "ethernet") {
				devType = "io card (network)"
			} else if strings.Contains(strings.ToLower(devName), "fibre channel") {
				devType = "io card (fc)"
			} else if strings.Contains(strings.ToLower(devName), "raid") {
				devType = "io card (raid)"
			} else if strings.Contains(strings.ToLower(devName), "vga") || strings.Contains(strings.ToLower(devName), "display") || strings.Contains(strings.ToLower(devName), "nvidia") {
				devType = "GPU"
			}

			// Only add if it matches our scope of interest to avoid cluttering.
			if devType != "" {
				hostData.Results = append(hostData.Results, checkBroadcomHCL(hostMo.Name, devName, devType, identifiers))
			}
		}
	}

	// NOTE: vSAN SSD extraction would require traversing `hostMo.Config.StorageDevice.ScsiLun`
	// and verifying the 'ssd' property. Omitted for brevity but architecture supports it easily here.

	return &hostData
}

// checkBroadcomHCL is a mock function simulating a query to the Broadcom Compatibility Guide.
// As Broadcom does not provide a public REST API for direct unauthenticated programmatic HCL queries,
// this function would need to be expanded later using web scraping or a private Broadcom API client.
func checkBroadcomHCL(hostname, deviceName, deviceType string, identifiers map[string]int16) HCLResult {
	// TODO: Implement actual HTTP request logic to https://compatibilityguide.broadcom.com
	// Use the `identifiers` (VID, DID, etc.) to construct the payload/URL accurately.

	// Placeholder logic to demonstrate structure.
	return HCLResult{
		Hostname:   hostname,
		Device:     strings.TrimSpace(deviceName),
		DeviceType: deviceType,
		Certified:  true, // Mocked as TRUE
		HCLLink:    "https://compatibilityguide.broadcom.com/detail?mock=true&dev=" + url.QueryEscape(deviceName),
	}
}

// printText formats the output into a human-readable table grouped by DC -> Cluster -> Host.
func printText(data []HostComponents) {
	for _, hd := range data {
		fmt.Printf("Datacenter: %s\n", hd.Datacenter)
		if hd.Cluster != "" {
			fmt.Printf("Cluster: %s\n", hd.Cluster)
		} else {
			fmt.Printf("Cluster: (Standalone)\n")
		}
		fmt.Printf("Host: %s\n\n", hd.Hostname)

		// Create a new tabwriter to align the table columns.
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', tabwriter.Debug)
		fmt.Fprintln(w, "------------------------")
		fmt.Fprintln(w, "Hostname\tdevice\tdevice type\tcertified\thcl")
		
		for _, res := range hd.Results {
			certStr := "FALSE"
			if res.Certified {
				certStr = "TRUE"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
				res.Hostname,
				res.Device,
				res.DeviceType,
				certStr,
				res.HCLLink,
			)
		}
		w.Flush()
		fmt.Printf("\n---\n\n")
	}
}

// printJSON formats the entire collected dataset as a JSON payload for automated pipelines.
func printJSON(data []HostComponents) {
	// Flatten the nested data into a single array of HCL results for easier JSON consumption,
	// or return the structural grouping based on preference. Here, we output the raw grouping.
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		log.Fatalf("Error marshaling JSON: %v", err)
	}
	fmt.Println(string(jsonData))
}
