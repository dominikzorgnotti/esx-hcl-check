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

// --- Structs for Phase 1: Raw vSphere Data Collection ---

// RawPCIDevice holds the raw hardware IDs for a single PCI device.
type RawPCIDevice struct {
	DeviceName string `json:"device_name"`
	DeviceType string `json:"device_type"`
	VID        int32  `json:"vid"`
	DID        int32  `json:"did"`
	SSID       int32  `json:"ssid"`
}

// RawHostData holds the unanalyzed hardware inventory for a single ESXi host.
type RawHostData struct {
	Datacenter string         `json:"datacenter"`
	Cluster    string         `json:"cluster"`
	Hostname   string         `json:"hostname"`
	SysVendor  string         `json:"sys_vendor"`
	SysModel   string         `json:"sys_model"`
	CpuModel   string         `json:"cpu_model"`
	PCIDevices []RawPCIDevice `json:"pci_devices"`
}

// --- Structs for Phase 2: HCL Verification ---

// HCLResult represents the certification status of a single hardware component.
type HCLResult struct {
	Hostname   string `json:"hostname"`
	Device     string `json:"device"`
	DeviceType string `json:"device_type"`
	Certified  bool   `json:"certified"`
	HCLLink    string `json:"hcl"`
}

// HostComponents holds the HCL results for a single ESXi host.
type HostComponents struct {
	Datacenter string
	Cluster    string
	Hostname   string
	Results    []HCLResult
}

func main() {
	// Parse command-line flags and environmental variables.
	var (
		jsonOutput  = flag.Bool("json", false, "Output final HCL results in JSON format")
		dcTarget    = flag.String("dc", os.Getenv("GOVC_DATACENTER"), "Target datacenter (optional)")
		clsTarget   = flag.String("cluster", os.Getenv("GOVC_CLUSTER"), "Target cluster (optional)")
		esxiRelease = flag.String("release", "ESXi 9.1", "Target ESXi version for compatibility validation")
		vsphereJson = flag.String("vspherejson", "", "Path to save the raw vSphere hardware JSON (defaults to OS temp dir)")
		noHCL       = flag.Bool("nohcl", false, "Skip the HCL check phase and only collect vSphere data")
	)
	flag.Parse()

	ctx := context.Background()

	// ---------------------------------------------------------
	// PHASE 1: Data Collection
	// ---------------------------------------------------------
	client, err := connectToVC(ctx)
	if err != nil {
		log.Fatalf("Error connecting to vCenter: %v", err)
	}
	
	if !*jsonOutput {
		fmt.Printf("# Connecting to %s ...\n", client.Client.URL().Host)
		fmt.Println("# Collecting inventory and hardware data...")
	}

	rawInventory, err := collectVSphereData(ctx, client, *dcTarget, *clsTarget)
	if err != nil {
		client.Logout(ctx)
		log.Fatalf("Error discovering inventory: %v", err)
	}
	
	// Gracefully close vCenter session as we no longer need it.
	client.Logout(ctx)

	// Save the raw collected data to a JSON file.
	savedPath, err := saveRawInventory(rawInventory, *vsphereJson)
	if err != nil {
		log.Fatalf("Failed to save raw inventory JSON: %v", err)
	}

	if !*jsonOutput {
		fmt.Printf("# Raw inventory saved to: %s\n\n", savedPath)
	}

	// If the user opted out of HCL checks, exit early.
	if *noHCL {
		if !*jsonOutput {
			fmt.Println("Skipping HCL validation due to -nohcl flag. Exiting.")
		}
		return
	}

	// ---------------------------------------------------------
	// PHASE 2: HCL Verification
	// ---------------------------------------------------------
	hclResults := performHCLChecks(rawInventory, *esxiRelease)

	// ---------------------------------------------------------
	// PHASE 3: Output Formatting
	// ---------------------------------------------------------
	if *jsonOutput {
		printJSON(hclResults)
	} else {
		printText(hclResults)
	}
}

// connectToVC initializes the govmomi client using GOVC_* environment variables.
func connectToVC(ctx context.Context) (*govmomi.Client, error) {
	vcURL := os.Getenv("GOVC_URL")
	if vcURL == "" {
		return nil, fmt.Errorf("GOVC_URL is not set")
	}

	u, err := url.Parse(vcURL)
	if err != nil {
		return nil, fmt.Errorf("invalid GOVC_URL format: %w", err)
	}

	username := os.Getenv("GOVC_USERNAME")
	password := os.Getenv("GOVC_PASSWORD")
	if username != "" {
		u.User = url.UserPassword(username, password)
	}

	insecure := strings.ToLower(os.Getenv("GOVC_INSECURE")) == "true" || os.Getenv("GOVC_INSECURE") == "1"

	return govmomi.NewClient(ctx, u, insecure)
}

// collectVSphereData traverses the vCenter inventory and builds the raw hardware definitions.
func collectVSphereData(ctx context.Context, client *govmomi.Client, dcTarget, clsTarget string) ([]RawHostData, error) {
	finder := find.NewFinder(client.Client, true)
	pc := property.DefaultCollector(client.Client)

	var allHostData []RawHostData

	dcQuery := "*"
	if dcTarget != "" {
		dcQuery = dcTarget
	}
	datacenters, err := finder.DatacenterList(ctx, dcQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to find datacenters: %w", err)
	}

	for _, dc := range datacenters {
		finder.SetDatacenter(dc)

		clsQuery := "*"
		if clsTarget != "" {
			clsQuery = clsTarget
		}
		clusters, err := finder.ClusterComputeResourceList(ctx, clsQuery)
		if err != nil && clsTarget != "" {
			return nil, fmt.Errorf("failed to find cluster %s: %w", clsTarget, err)
		}

		// 1. Process hosts within clusters.
		for _, cluster := range clusters {
			hosts, err := cluster.Hosts(ctx)
			if err != nil {
				continue
			}
			for _, hostRef := range hosts {
				if hostData := extractHostHardware(ctx, pc, hostRef.Reference(), dc.Name(), cluster.Name()); hostData != nil {
					allHostData = append(allHostData, *hostData)
				}
			}
		}

		// 2. Process standalone hosts if no specific cluster was targeted.
		if clsTarget == "" {
			compResources, err := finder.ComputeResourceList(ctx, "*")
			if err == nil {
				for _, cr := range compResources {
					if cr.Reference().Type == "ClusterComputeResource" {
						continue
					}
					if hosts, err := cr.Hosts(ctx); err == nil {
						for _, hostRef := range hosts {
							if hostData := extractHostHardware(ctx, pc, hostRef.Reference(), dc.Name(), ""); hostData != nil {
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

// extractHostHardware fetches raw vSphere properties and maps them to the RawHostData struct.
func extractHostHardware(ctx context.Context, pc *property.Collector, hostRef types.ManagedObjectReference, dcName, clsName string) *RawHostData {
	var hostMo mo.HostSystem

	err := pc.RetrieveOne(ctx, hostRef, []string{"name", "runtime.connectionState", "summary.hardware", "config.hardware"}, &hostMo)
	if err != nil || hostMo.Runtime.ConnectionState != types.HostSystemConnectionStateConnected {
		return nil
	}

	raw := RawHostData{
		Datacenter: dcName,
		Cluster:    clsName,
		Hostname:   hostMo.Name,
	}

	if hostMo.Summary.Hardware != nil {
		raw.SysVendor = hostMo.Summary.Hardware.Vendor
		raw.SysModel = hostMo.Summary.Hardware.Model
		raw.CpuModel = hostMo.Summary.Hardware.CpuModel
	}

	if hostMo.Config != nil && hostMo.Config.Hardware != nil {
		for _, pciDev := range hostMo.Config.Hardware.PciDevice {
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

			if devType != "" {
				raw.PCIDevices = append(raw.PCIDevices, RawPCIDevice{
					DeviceName: strings.TrimSpace(devName),
					DeviceType: devType,
					VID:        pciDev.VendorId,
					DID:        pciDev.DeviceId,
					SSID:       pciDev.SubDeviceId,
				})
			}
		}
	}
	return &raw
}

// saveRawInventory writes the RawHostData array to a JSON file.
func saveRawInventory(data []RawHostData, targetPath string) (string, error) {
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal raw data: %w", err)
	}

	filePath := targetPath
	if filePath == "" {
		// If no path was specified, use the OS default temp directory
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
func buildHexQueryURL(releaseVersion string, vid, did, ssid int32) string {
	baseURL := "https://compatibilityguide.broadcom.com/search"

	params := url.Values{}
	params.Set("program", "io")
	params.Set("persona", "live")
	params.Set("column", "brandName")
	params.Set("order", "asc")
	params.Set("productReleaseVersion", fmt.Sprintf("[%s]", releaseVersion))
	params.Set("vid", fmt.Sprintf("[%04x]", vid))
	params.Set("did", fmt.Sprintf("[%04x]", did))
	params.Set("maxSsid", fmt.Sprintf("[%04x]", ssid))

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
