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
func collectVSphereData(ctx context.Context, client *govmomi.Client, dcTarget, clsTarget string, debugPci, vsanBeta bool) ([]RawHostData, error) {
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
				if hostData := extractHostHardware(ctx, pc, hostRef.Reference(), dc.Name(), cluster.Name(), debugPci, vsanBeta); hostData != nil {
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
							if hostData := extractHostHardware(ctx, pc, hostRef.Reference(), dc.Name(), "", debugPci, vsanBeta); hostData != nil {
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
func extractHostHardware(ctx context.Context, pc *property.Collector, hostRef types.ManagedObjectReference, dcName, clsName string, debugPci, vsanBeta bool) *RawHostData {
	var hostMo mo.HostSystem

	err := pc.RetrieveOne(ctx, hostRef, []string{"name", "runtime.connectionState", "summary.hardware", "hardware", "config.network", "config.storageDevice"}, &hostMo)
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

	// NEW: Extract the System BIOS version as the primary firmware representation for the server
	if hostMo.Hardware != nil && hostMo.Hardware.BiosInfo != nil {
		raw.BiosVersion = hostMo.Hardware.BiosInfo.BiosVersion
	}

	pciRoles := make(map[string]string)
	pciPnics := make(map[string]types.PhysicalNic)
	
	if hostMo.Config != nil {
		// Map Physical NICs to cross-reference firmware and drivers later
		if hostMo.Config.Network != nil {
			for _, pnic := range hostMo.Config.Network.Pnic {
				pciRoles[pnic.Pci] = "io card (network)"
				pciPnics[pnic.Pci] = pnic
			}
		}
		if hostMo.Config.StorageDevice != nil {
			for _, hbaBase := range hostMo.Config.StorageDevice.HostBusAdapter {
				hba := hbaBase.GetHostHostBusAdapter()
				if hba != nil && hba.Pci != "" {
					switch hbaBase.(type) {
					case *types.HostFibreChannelHba:
						pciRoles[hba.Pci] = "io card (fc)"
					case *types.HostPcieHba: 
						pciRoles[hba.Pci] = "nvme-disk" 
					case *types.HostBlockHba, *types.HostSerialAttachedHba, *types.HostInternetScsiHba:
						pciRoles[hba.Pci] = "io card (raid)"
					default:
						pciRoles[hba.Pci] = "io card (raid)"
					}
				}
			}
		}
	}

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
			devType := pciRoles[pciDev.Id]

			// Dynamically populate Firmware and Drivers if this is a Physical NIC
			var fw, dv, dn string
			if pnic, ok := pciPnics[pciDev.Id]; ok {
				fw = pnic.FirmwareVersion
				dv = pnic.DriverVersion
				dn = pnic.Driver
			}

			if devType == "nvme-disk" {
				if vsanBeta {
					vendor := ""
					model := devName
					parts := strings.SplitN(devName, " ", 2)
					if len(parts) == 2 {
						vendor = parts[0]
						model = parts[1]
					}
					
					raw.Disks = append(raw.Disks, RawDiskDevice{
						DeviceName: strings.TrimSpace(devName),
						DeviceType: "vSAN NVMe PCIe (beta)",
						Vendor:     strings.TrimSpace(vendor),
						Model:      strings.TrimSpace(model),
						Firmware:   "",
					})
				}
				continue
			}

			if devType == "" {
				devNameLower := strings.ToLower(devName)
				if strings.Contains(devNameLower, "vga") || strings.Contains(devNameLower, "display") || strings.Contains(devNameLower, "nvidia") || strings.Contains(devNameLower, "amd") {
					devType = "GPU"
				} else if strings.Contains(devNameLower, "network") || strings.Contains(devNameLower, "ethernet") || strings.Contains(devNameLower, "nic") || strings.Contains(devNameLower, "mellanox") || strings.Contains(devNameLower, "connectx") {
					devType = "io card (network)"
				} else if strings.Contains(devNameLower, "fibre channel") || strings.Contains(devNameLower, "hba") || strings.Contains(devNameLower, "qlogic") || strings.Contains(devNameLower, "emulex") {
					devType = "io card (fc)"
				} else if strings.Contains(devNameLower, "raid") || strings.Contains(devNameLower, "storage") || strings.Contains(devNameLower, "lsi") || strings.Contains(devNameLower, "broadcom") || strings.Contains(devNameLower, "adaptec") || strings.Contains(devNameLower, "megaraid") || strings.Contains(devNameLower, "smart array") || strings.Contains(devNameLower, "sas") || strings.Contains(devNameLower, "perc") {
					devType = "io card (raid)"
				} else if debugPci {
					devType = "unknown (debug)"
				}
			}

			if devType != "" {
				raw.PCIDevices = append(raw.PCIDevices, RawPCIDevice{
					DeviceName: strings.TrimSpace(devName),
					DeviceType: devType,
					VID:        pciDev.VendorId,
					DID:        pciDev.DeviceId,
					SVID:       pciDev.SubVendorId,
					SSID:       pciDev.SubDeviceId,
					Firmware:   fw,
					DriverVer:  dv,
					DriverName: dn,
				})
			}
		}
	}

	if vsanBeta {
		if hostMo.Config != nil && hostMo.Config.StorageDevice != nil {
			for _, baseLun := range hostMo.Config.StorageDevice.ScsiLun {
				if disk, ok := baseLun.(*types.HostScsiDisk); ok {
					if disk.Ssd != nil && *disk.Ssd {
						vendor := strings.TrimSpace(disk.Vendor)
						model := strings.TrimSpace(disk.Model)
						
						raw.Disks = append(raw.Disks, RawDiskDevice{
							DeviceName: fmt.Sprintf("%s %s", vendor, model),
							DeviceType: "vSAN SSD (beta)",
							Vendor:     vendor,
							Model:      model,
							Firmware:   strings.TrimSpace(disk.Revision),
						})
					}
				}
			}
		}
	}

	return &raw
}
