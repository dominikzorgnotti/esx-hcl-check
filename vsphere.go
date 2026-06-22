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
func collectVSphereData(ctx context.Context, client *govmomi.Client, dcTarget, clsTarget string, debugPci bool) ([]RawHostData, error) {
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
				if hostData := extractHostHardware(ctx, pc, hostRef.Reference(), dc.Name(), cluster.Name(), debugPci); hostData != nil {
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
							if hostData := extractHostHardware(ctx, pc, hostRef.Reference(), dc.Name(), "", debugPci); hostData != nil {
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
func extractHostHardware(ctx context.Context, pc *property.Collector, hostRef types.ManagedObjectReference, dcName, clsName string, debugPci bool) *RawHostData {
	var hostMo mo.HostSystem

	// Added "config.storageDevice" to fetch the disk layouts
	err := pc.RetrieveOne(ctx, hostRef, []string{"name", "runtime.connectionState", "summary.hardware", "hardware", "config.storageDevice"}, &hostMo)
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

	// 1. Extract CPU and PCI devices
	if hostMo.Hardware != nil {
		for _, feat := range hostMo.Hardware.CpuFeature {
			if feat.Level == 1 {
				cleanEax := strings.ReplaceAll(feat.Eax, ":", "")
				cleanEax = strings.ReplaceAll(cleanEax, "-", "0") 
				cleanEax = strings.ReplaceAll(cleanEax, "x", "0") 
				
				if val, err := strconv.ParseUint(cleanEax, 2, 32); err == nil {
					raw.CpuId = fmt.Sprintf("0x%08x", val)
				}
				break
			}
		}

		for _, pciDev := range hostMo.Hardware.PciDevice {
			devName := pciDev.DeviceName
			var devType string

			devNameLower := strings.ToLower(devName)
			if strings.Contains(devNameLower, "network") || strings.Contains(devNameLower, "ethernet") || strings.Contains(devNameLower, "nic") || strings.Contains(devNameLower, "mellanox") || strings.Contains(devNameLower, "connectx") {
				devType = "io card (network)"
			} else if strings.Contains(devNameLower, "fibre channel") || strings.Contains(devNameLower, "hba") || strings.Contains(devNameLower, "qlogic") || strings.Contains(devNameLower, "emulex") {
				devType = "io card (fc)"
			} else if strings.Contains(devNameLower, "raid") || strings.Contains(devNameLower, "storage") || strings.Contains(devNameLower, "lsi") || strings.Contains(devNameLower, "broadcom") || strings.Contains(devNameLower, "adaptec") || strings.Contains(devNameLower, "megaraid") {
				devType = "io card (raid)"
			} else if strings.Contains(devNameLower, "vga") || strings.Contains(devNameLower, "display") || strings.Contains(devNameLower, "nvidia") || strings.Contains(devNameLower, "amd") {
				devType = "GPU"
			}

			if devType != "" || debugPci {
				if devType == "" {
					devType = "unknown (debug)"
				}
				
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

	// 2. Extract Storage / vSAN SSD Disks
	if hostMo.Config != nil && hostMo.Config.StorageDevice != nil {
		for _, baseLun := range hostMo.Config.StorageDevice.ScsiLun {
			// Type assert to ensure this LUN is an actual physical disk
			if disk, ok := baseLun.(*types.HostScsiDisk); ok {
				// We only care about SSD/NVMe for vSAN compatibility
				if disk.Ssd != nil && *disk.Ssd {
					vendor := strings.TrimSpace(disk.Vendor)
					model := strings.TrimSpace(disk.Model)
					
					raw.Disks = append(raw.Disks, RawDiskDevice{
						DeviceName: fmt.Sprintf("%s %s", vendor, model),
						DeviceType: "vSAN SSD",
						Vendor:     vendor,
						Model:      model,
					})
				}
			}
		}
	}

	return &raw
}
