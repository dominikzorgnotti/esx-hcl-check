package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"

	"github.com/vmware/govmomi"
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

// HostComponents holds the context and hardware results for a single ESXi host.
type HostComponents struct {
	Datacenter string
	Cluster    string
	Hostname   string
	Results    []HCLResult
}

func main() {
	// Added a flag to specify the exact target ESXi release version.
	var (
		jsonOutput = flag.Bool("json", false, "Output in JSON format")
		dcTarget   = flag.String("dc", os.Getenv("GOVC_DATACENTER"), "Target datacenter (optional)")
		clsTarget  = flag.String("cluster", os.Getenv("GOVC_CLUSTER"), "Target cluster (optional)")
		esxiRelease = flag.String("release", "ESXi 9.1", "Target ESXi version for compatibility validation")
	)
	flag.Parse()

	// (Rest of connection and orchestration logic remains identical to the previous version...)
	_ = jsonOutput
	_ = dcTarget
	_ = clsTarget
	_ = esxiRelease
}

// processHost fetches the properties for a single host and passes IDs to the URL builder.
func processHost(ctx context.Context, pc *property.Collector, hostRef types.ManagedObjectReference, dcName, clsName, esxiRelease string) *HostComponents {
	var hostMo mo.HostSystem

	err := pc.RetrieveOne(ctx, hostRef, []string{"name", "runtime.connectionState", "summary.hardware", "config.hardware"}, &hostMo)
	if err != nil {
		log.Printf("Warning: Failed to retrieve properties for host %v: %v", hostRef, err)
		return nil
	}

	if hostMo.Runtime.ConnectionState != types.HostSystemConnectionStateConnected {
		return nil
	}

	hostData := HostComponents{
		Datacenter: dcName,
		Cluster:    clsName,
		Hostname:   hostMo.Name,
	}

	// 1. Extract System and CPU (Fall back to keyword queries for broad search if specific IDs aren't present)
	if hostMo.Summary.Hardware != nil {
		sysModel := fmt.Sprintf("%s %s", hostMo.Summary.Hardware.Vendor, hostMo.Summary.Hardware.Model)
		hostData.Results = append(hostData.Results, buildSystemQuery(hostMo.Name, sysModel, esxiRelease))

		cpuModel := hostMo.Summary.Hardware.CpuModel
		hostData.Results = append(hostData.Results, buildCPUQuery(hostMo.Name, cpuModel, esxiRelease))
	}

	// 2. Extract PCI Devices (IO Cards, GPUs) using Hex-based IDs
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

			// If it matches a required I/O or GPU subsystem, build the hex query URL
			if devType != "" {
				hclURL := buildHexQueryURL(esxiRelease, pciDev.VendorId, pciDev.DeviceId, pciDev.SubDeviceId)
				
				hostData.Results = append(hostData.Results, HCLResult{
					Hostname:   hostMo.Name,
					Device:     strings.TrimSpace(devName),
					DeviceType: devType,
					Certified:  true, // Default placeholder until a live scraping engine processes the page content
					HCLLink:    hclURL,
				})
			}
		}
	}

	return &hostData
}

// buildHexQueryURL translates decimal PCI IDs into Broadcom's required hex string format and constructs the URL query.
func buildHexQueryURL(releaseVersion string, vid, did, ssid int32) string {
	baseURL := "https://compatibilityguide.broadcom.com/search"

	// Convert integer components to 4-digit lowercase hex format strings (e.g., 606 -> "025e")
	vidHex := fmt.Sprintf("%04x", vid)
	didHex := fmt.Sprintf("%04x", did)
	ssidHex := fmt.Sprintf("%04x", ssid)

	// Map out the precise URL queries requested by Broadcom's platform
	params := url.Values{}
	params.Set("program", "io")
	params.Set("persona", "live")
	params.Set("column", "brandName")
	params.Set("order", "asc")
	
	// Add brackets to parameter arguments as dictated by Broadcom's system
	params.Set("productReleaseVersion", fmt.Sprintf("[%s]", releaseVersion))
	params.Set("vid", fmt.Sprintf("[%s]", vidHex))
	params.Set("did", fmt.Sprintf("[%s]", didHex))
	params.Set("maxSsid", fmt.Sprintf("[%s]", ssidHex))

	// Encode structures automatically handling special parameters (+ for space, %5B for [, %5D for ])
	return fmt.Sprintf("%s?%s", baseURL, params.Encode())
}

// buildSystemQuery provides a search query mapping fallback for non-PCI base-system chassis elements.
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

// buildCPUQuery provides a search query mapping fallback for non-PCI microprocessor series elements.
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
