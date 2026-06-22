package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

// connectToVC initializes the govmomi client using GOVC_* environment variables.
func connectToVC(ctx context.Context) (*govmomi.Client, error) {
	vcURL := os.Getenv("GOVC_URL")
	if vcURL == "" {
		return nil, fmt.Errorf("GOVC_URL is not set")
	}

	u, err := soap.ParseURL(vcURL)
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

	err := pc.RetrieveOne(ctx, hostRef, []string{"name", "runtime.connectionState", "summary.hardware", "hardware"}, &hostMo)
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

	if hostMo.Hardware != nil {
		// Extract CPUID (Processor Signature) from the CPU feature tree
		for _, feat := range hostMo.Hardware.CpuFeature {
			if feat.Level == 1 {
				// EAX value is typically a binary string separated by colons (e.g., "0000:0000:0000:0101:0000:0110:0101:0100")
				cleanEax := strings.ReplaceAll(feat.Eax, ":", "")
				cleanEax = strings.ReplaceAll(cleanEax, "-", "0") // Defensive replacement for masked bits
				cleanEax = strings.ReplaceAll(cleanEax, "x", "0") // Defensive replacement for masked bits
				
				// Parse Base-2 string into an integer, then format into hex
				if val, err := strconv.ParseUint(cleanEax, 2, 32); err == nil {
					raw.CpuId = fmt.Sprintf("0x%08x", val)
				}
				break
			}
		}

		for _, pciDev := range hostMo.Hardware.PciDevice {
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
