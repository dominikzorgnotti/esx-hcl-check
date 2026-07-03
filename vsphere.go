package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/session"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"
)

const (
	// vcConnectTimeout bounds the vCenter connect + login so an unreachable or
	// misconfigured GOVC_URL fails fast instead of hanging indefinitely.
	vcConnectTimeout = 30 * time.Second
	// soapCallTimeout bounds each raw SOAP HBA firmware call (vSphere 9.1+ path).
	soapCallTimeout = 30 * time.Second
)

// govcInsecure reports whether GOVC_INSECURE requests skipping TLS certificate
// verification for the vCenter connection.
func govcInsecure() bool {
	v := strings.ToLower(os.Getenv("GOVC_INSECURE"))
	return v == "true" || v == "1"
}

// configureTLSFromEnv applies govc-compatible TLS settings from the environment
// to a freshly-constructed soap.Client, before it makes any request. This mirrors
// govc's own cli/flags/client.go so the tool honors the same env vars:
//
//	GOVC_TLS_CA_CERTS          PEM CA bundle(s) trusted *instead of* the system
//	                           roots (OS PathListSeparator-separated).
//	GOVC_TLS_KNOWN_HOSTS       "host thumbprint" file used as a verification
//	                           fallback when normal chain validation fails.
//	GOVC_TLS_HANDSHAKE_TIMEOUT Go duration string bounding the TLS handshake.
//
// A misconfigured value (unreadable/invalid CA bundle, bad duration) is returned
// as a hard error rather than a warning: silently ignoring it would fall back to
// weaker verification than the operator asked for, which is exactly the footgun
// this feature exists to remove. Note that when GOVC_INSECURE disables
// verification, CA_CERTS/KNOWN_HOSTS have no effect (main warns about that).
func configureTLSFromEnv(sc *soap.Client) error {
	if caCerts := os.Getenv("GOVC_TLS_CA_CERTS"); caCerts != "" {
		if err := sc.SetRootCAs(caCerts); err != nil {
			return fmt.Errorf("GOVC_TLS_CA_CERTS: cannot load CA bundle(s) %q: %w", caCerts, err)
		}
	}

	if knownHosts := os.Getenv("GOVC_TLS_KNOWN_HOSTS"); knownHosts != "" {
		if err := sc.LoadThumbprints(knownHosts); err != nil {
			return fmt.Errorf("GOVC_TLS_KNOWN_HOSTS: cannot load thumbprints from %q: %w", knownHosts, err)
		}
	}

	if v := os.Getenv("GOVC_TLS_HANDSHAKE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("GOVC_TLS_HANDSHAKE_TIMEOUT: %q is not a valid Go duration (e.g. \"10s\"): %w", v, err)
		}
		sc.DefaultTransport().TLSHandshakeTimeout = d
	}

	return nil
}

// connectToVC initializes the govmomi client using GOVC_* environment variables.
//
// TLS verification follows govc's model: with GOVC_INSECURE unset/false the
// server certificate is validated against the host's system root CA store (Go's
// crypto/tls default, since soap.NewClient leaves tls.Config.RootCAs nil). The
// GOVC_TLS_* variables (see configureTLSFromEnv) can override the trust anchors
// or pin thumbprints.
//
// govmomi.NewClient offers no hook to configure the soap.Client before its first
// round-trip (vim25.NewClient immediately retrieves ServiceContent), so we
// replicate its small assembly here to slot TLS configuration in between.
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

	insecure := govcInsecure()

	connCtx, cancel := context.WithTimeout(ctx, vcConnectTimeout)
	defer cancel()

	soapClient := soap.NewClient(u, insecure)
	if err := configureTLSFromEnv(soapClient); err != nil {
		return nil, err
	}

	vimClient, err := vim25.NewClient(connCtx, soapClient)
	if err != nil {
		return nil, classifyConnectError(connCtx, u.Host, err)
	}

	client := &govmomi.Client{
		Client:         vimClient,
		SessionManager: session.NewManager(vimClient),
	}

	// Match govmomi.NewClient: only authenticate when the URL carries credentials.
	if u.User != nil {
		if err := client.Login(connCtx, u.User); err != nil {
			return nil, classifyConnectError(connCtx, u.Host, err)
		}
	}

	return client, nil
}

// classifyConnectError turns a raw govmomi/soap connection failure into an
// actionable message that distinguishes timeout, DNS, TLS, and auth problems,
// so an admin can tell "wrong password" from "host unreachable" at a glance.
//
// OS-level failures (timeout, DNS, connection refused) are matched on their
// typed errors rather than message text, because the underlying OS strings are
// localized (e.g. German Windows) and would otherwise slip through. TLS and
// auth failures are matched on substrings, since those come from Go's crypto/tls
// and vCenter's SOAP faults respectively and are not OS-localized.
func classifyConnectError(ctx context.Context, host string, err error) error {
	// Extract the OS-level socket errno once. On Windows these use the WSA* range
	// (10060 = WSAETIMEDOUT, 10061 = WSAECONNREFUSED), which Go's ETIMEDOUT /
	// ECONNREFUSED constants do NOT alias — so we compare the raw errno
	// numerically to stay OS- and locale-independent.
	var errno syscall.Errno
	hasErrno := errors.As(err, &errno)

	var netErr net.Error
	timedOut := errors.Is(err, context.DeadlineExceeded) || ctx.Err() == context.DeadlineExceeded ||
		(errors.As(err, &netErr) && netErr.Timeout()) ||
		(hasErrno && uintptr(errno) == 10060) // WSAETIMEDOUT
	if timedOut {
		return fmt.Errorf("timed out connecting to %s: host unreachable or not responding (check GOVC_URL, network, and firewall)", host)
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("cannot resolve host %s: DNS lookup failed (check GOVC_URL spelling and DNS): %w", host, err)
	}

	if errors.Is(err, syscall.ECONNREFUSED) || (hasErrno && uintptr(errno) == 10061) { // WSAECONNREFUSED
		return fmt.Errorf("connection refused by %s: nothing is listening on that host/port (check GOVC_URL): %w", host, err)
	}

	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "certificate") || strings.Contains(msg, "x509") || strings.Contains(msg, "tls"):
		return fmt.Errorf("TLS verification failed for %s: install the CA in the system trust store, point GOVC_TLS_CA_CERTS at the CA bundle, or set GOVC_INSECURE=1 for self-signed lab certs: %w", host, err)
	case strings.Contains(msg, "incorrect user name or password") || strings.Contains(msg, "login failure") || strings.Contains(msg, "invalidlogin") || strings.Contains(msg, "cannot complete login") || strings.Contains(msg, "permission to perform this operation"):
		return fmt.Errorf("authentication failed for %s: check GOVC_USERNAME and GOVC_PASSWORD: %w", host, err)
	default:
		return fmt.Errorf("failed to connect to %s: %w", host, err)
	}
}

// hostJob is one planned output entry: either an already-resolved record
// (ready, e.g. an enumeration-failure skip) or a host that still needs its
// hardware extracted.
type hostJob struct {
	ready   *RawHostData // non-nil => already resolved; emit as-is
	hostRef types.ManagedObjectReference
	dcName  string
	clsName string
}

// runBounded runs fn(i) for i in [0,n) with at most `workers` running at once,
// and blocks until all complete. fn is responsible for writing its own slot, so
// callers get deterministic, index-ordered results regardless of completion
// order. workers < 1 is treated as 1 (fully sequential).
func runBounded(n, workers int, fn func(i int)) {
	if workers < 1 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			fn(idx)
		}(i)
	}
	wg.Wait()
}

// collectVSphereData traverses the vCenter inventory and builds the raw hardware definitions.
func collectVSphereData(ctx context.Context, client *govmomi.Client, dcTarget, clsTarget string, debugPci, vsanBeta bool, excludeCfg ExcludeConfig, workers int) ([]RawHostData, error) {
	finder := find.NewFinder(client.Client, true)
	pc := property.DefaultCollector(client.Client)
	source := client.Client.URL().Host

	dcQuery := "*"
	if dcTarget != "" {
		dcQuery = dcTarget
	}
	datacenters, err := finder.DatacenterList(ctx, dcQuery)
	if err != nil {
		return nil, fmt.Errorf("failed to find datacenters: %w", err)
	}

	// Enumerate work sequentially (the finder is stateful and this is cheap),
	// building an ordered list of jobs so output ordering stays deterministic.
	var jobs []hostJob

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
				// Cannot enumerate this cluster's hosts (commonly missing
				// permissions). Record a skipped-cluster entry so the failure is
				// visible instead of a silently-absent cluster.
				jobs = append(jobs, hostJob{ready: &RawHostData{
					Source:     source,
					Datacenter: dc.Name(),
					Cluster:    cluster.Name(),
					SkipReason: fmt.Sprintf("could not enumerate hosts: %v", err),
				}})
				continue
			}
			for _, hostRef := range hosts {
				jobs = append(jobs, hostJob{hostRef: hostRef.Reference(), dcName: dc.Name(), clsName: cluster.Name()})
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
							jobs = append(jobs, hostJob{hostRef: hostRef.Reference(), dcName: dc.Name(), clsName: ""})
						}
					} else {
						jobs = append(jobs, hostJob{ready: &RawHostData{
							Source:     source,
							Datacenter: dc.Name(),
							Cluster:    "",
							SkipReason: fmt.Sprintf("could not enumerate standalone host %s: %v", cr.Name(), err),
						}})
					}
				}
			}
		}
	}

	// Extract per-host hardware concurrently (bounded), each worker writing only
	// its own indexed slot. extractHostHardware never returns nil, so every slot
	// is filled.
	results := make([]RawHostData, len(jobs))
	runBounded(len(jobs), workers, func(i int) {
		if jobs[i].ready != nil {
			results[i] = *jobs[i].ready
			return
		}
		if hd := extractHostHardware(ctx, client, pc, jobs[i].hostRef, jobs[i].dcName, jobs[i].clsName, debugPci, vsanBeta, excludeCfg); hd != nil {
			results[i] = *hd
		}
	})

	return results, nil
}

// extractHostHardware fetches raw vSphere properties and maps them to the RawHostData struct.
func extractHostHardware(ctx context.Context, client *govmomi.Client, pc *property.Collector, hostRef types.ManagedObjectReference, dcName, clsName string, debugPci, vsanBeta bool, excludeCfg ExcludeConfig) *RawHostData {
	apiVersion := client.Client.ServiceContent.About.ApiVersion
	var hostMo mo.HostSystem

	err := pc.RetrieveOne(ctx, hostRef, []string{"name", "runtime.connectionState", "summary.hardware", "hardware", "config.network", "config.storageDevice"}, &hostMo)
	if err != nil {
		// Could not read this host's properties (RBAC, network, or a transient
		// API error). Surface it as a skipped host instead of silently dropping
		// it, so "no visibility into this host" is distinguishable from a clean
		// scan. hostMo.Name is likely empty here, so fall back to the MoRef.
		name := hostMo.Name
		if name == "" {
			name = hostRef.Value
		}
		return &RawHostData{
			Source:     client.Client.URL().Host,
			Datacenter: dcName,
			Cluster:    clsName,
			Hostname:   name,
			SkipReason: fmt.Sprintf("property-collector-error: %v", err),
		}
	}
	if hostMo.Runtime.ConnectionState != types.HostSystemConnectionStateConnected {
		// Host is reachable in the inventory but not connected (disconnected /
		// notResponding). Report the state rather than dropping it — this is an
		// expected condition the operator should still see.
		return &RawHostData{
			Source:     client.Client.URL().Host,
			Datacenter: dcName,
			Cluster:    clsName,
			Hostname:   hostMo.Name,
			SkipReason: fmt.Sprintf("host not connected (state: %s)", hostMo.Runtime.ConnectionState),
		}
	}

	raw := RawHostData{
		Source:     client.Client.URL().Host,
		Datacenter: dcName,
		Cluster:    clsName,
		Hostname:   hostMo.Name,
		APIVersion: apiVersion,
	}

	if hostMo.Summary.Hardware != nil {
		raw.SysVendor = hostMo.Summary.Hardware.Vendor
		raw.SysModel = hostMo.Summary.Hardware.Model
		raw.CpuModel = hostMo.Summary.Hardware.CpuModel
	}

	if hostMo.Hardware != nil && hostMo.Hardware.BiosInfo != nil {
		raw.BiosVersion = hostMo.Hardware.BiosInfo.BiosVersion
	}

	pciRoles := make(map[string]string)
	pciPnics := make(map[string]types.PhysicalNic)
	hbaKeyToPci := make(map[string]string)
	hbaDevToPci := make(map[string]string)
	nvmeDriverName := make(map[string]string) // pci_id -> driverName (HostHostBusAdapter.Driver)

	if hostMo.Config != nil {
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
					hbaKeyToPci[hba.Key] = hba.Pci
					hbaDevToPci[hba.Device] = hba.Pci
					switch hbaBase.(type) {
					case *types.HostFibreChannelHba:
						pciRoles[hba.Pci] = "io card (fc)"
					case *types.HostPcieHba:
						pciRoles[hba.Pci] = "nvme-disk"
						if hba.Driver != "" {
							nvmeDriverName[hba.Pci] = hba.Driver
						}
					case *types.HostBlockHba, *types.HostSerialAttachedHba, *types.HostInternetScsiHba:
						pciRoles[hba.Pci] = "io card (raid)"
					default:
						pciRoles[hba.Pci] = "io card (raid)"
					}
				}
			}
		}
	}

	// NVMe controller firmware from NvmeTopology — available on all vSphere versions via govmomi.
	// HostNvmeTopologyInterface.Adapter is a key or device-name reference to the HBA.
	nvmeFirmware := make(map[string]string) // pci_id -> firmware
	if hostMo.Config != nil && hostMo.Config.StorageDevice != nil && hostMo.Config.StorageDevice.NvmeTopology != nil {
		for _, iface := range hostMo.Config.StorageDevice.NvmeTopology.Adapter {
			pci := hbaKeyToPci[iface.Adapter]
			if pci == "" {
				pci = hbaDevToPci[iface.Adapter]
			}
			if pci == "" {
				continue
			}
			for _, ctrl := range iface.ConnectedController {
				if ctrl.FirmwareVersion != "" {
					// NvmeTopology appends the transport type (e.g. " PCIe") to the
					// firmware string. Strip it so comparison against the HCL works.
					fw := strings.TrimSpace(ctrl.FirmwareVersion)
					if strings.HasSuffix(strings.ToUpper(fw), "PCIE") {
						if stripped := strings.TrimSpace(fw[:len(fw)-4]); stripped != "" {
							fw = stripped
						}
					}
					nvmeFirmware[pci] = fw
					break
				}
			}
		}
	}

	// vSphere 9.1+ exposes firmwareVersion/driverVersion on HostHostBusAdapter natively,
	// but govmomi does not yet model those fields. Retrieve them via a raw SOAP call.
	var hba91Firmware map[string]struct{ Firmware, Driver string }
	if isAPIVersionAtLeast(apiVersion, "9.1") {
		hba91Firmware, _ = getHBAFirmwareViaSoap(ctx, client, hostRef)
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

			// Extract lowercase hex strings for ID comparison
			vidHex := fmt.Sprintf("%04x", uint16(pciDev.VendorId))
			didHex := fmt.Sprintf("%04x", uint16(pciDev.DeviceId))
			svidHex := fmt.Sprintf("%04x", uint16(pciDev.SubVendorId))
			ssidHex := fmt.Sprintf("%04x", uint16(pciDev.SubDeviceId))

			excluded := false

			// 1. Exact Name match exclusion
			for _, name := range excludeCfg.Names {
				if strings.EqualFold(strings.TrimSpace(devName), name) {
					excluded = true
					break
				}
			}

			// 2. Regex match exclusion
			if !excluded {
				for _, re := range excludeCfg.CompiledRegexes {
					if re.MatchString(devName) {
						excluded = true
						break
					}
				}
			}

			// 3. ID match exclusion
			if !excluded {
				for _, id := range excludeCfg.IDs {
					match := true
					if id.VID != "" && !strings.EqualFold(id.VID, vidHex) {
						match = false
					}
					if id.DID != "" && !strings.EqualFold(id.DID, didHex) {
						match = false
					}
					if id.SVID != "" && !strings.EqualFold(id.SVID, svidHex) {
						match = false
					}
					if id.SSID != "" && !strings.EqualFold(id.SSID, ssidHex) {
						match = false
					}

					// If the block successfully matched at least one provided ID constraint
					if match && (id.VID != "" || id.DID != "" || id.SVID != "" || id.SSID != "") {
						excluded = true
						break
					}
				}
			}

			if excluded {
				continue // Skip processing and dropping it from the raw inventory completely
			}

			devType := pciRoles[pciDev.Id]
			var fw, dv, dn string
			if pnic, ok := pciPnics[pciDev.Id]; ok {
				fw = pnic.FirmwareVersion
				dv = pnic.DriverVersion
				dn = pnic.Driver
			} else if info, ok := hba91Firmware[pciDev.Id]; ok {
				fw = info.Firmware
				dv = info.Driver
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
						DeviceType: "vSAN NVMe PCIe",
						Vendor:     strings.TrimSpace(vendor),
						Model:      strings.TrimSpace(model),
						Firmware:   nvmeFirmware[pciDev.Id],
						DriverName: nvmeDriverName[pciDev.Id],
						DriverVer:  dv,
						VID:        pciDev.VendorId,
						DID:        pciDev.DeviceId,
						SVID:       pciDev.SubVendorId,
						SSID:       pciDev.SubDeviceId,
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
		// Build a map from ScsiLun key -> HBA PCI address so we can attach the
		// NVMe controller's driver name/version to each SSD disk entry.
		diskKeyToHbaPci := make(map[string]string)
		if hostMo.Config != nil && hostMo.Config.StorageDevice != nil &&
			hostMo.Config.StorageDevice.ScsiTopology != nil {
			for _, topAdap := range hostMo.Config.StorageDevice.ScsiTopology.Adapter {
				pci := hbaKeyToPci[topAdap.Adapter]
				if pci == "" {
					pci = hbaDevToPci[topAdap.Adapter]
				}
				if pci == "" {
					continue
				}
				for _, target := range topAdap.Target {
					for _, lun := range target.Lun {
						diskKeyToHbaPci[lun.ScsiLun] = pci
					}
				}
			}
		}

		if hostMo.Config != nil && hostMo.Config.StorageDevice != nil {
			for _, baseLun := range hostMo.Config.StorageDevice.ScsiLun {
				if disk, ok := baseLun.(*types.HostScsiDisk); ok {
					if disk.Ssd != nil && *disk.Ssd {
						vendor := strings.TrimSpace(disk.Vendor)
						model := strings.TrimSpace(disk.Model)
						diskName := fmt.Sprintf("%s %s", vendor, model)

						// Verify disk against Exclude rules
						excluded := false
						for _, name := range excludeCfg.Names {
							if strings.EqualFold(strings.TrimSpace(diskName), name) {
								excluded = true
								break
							}
						}
						if !excluded {
							for _, re := range excludeCfg.CompiledRegexes {
								if re.MatchString(diskName) {
									excluded = true
									break
								}
							}
						}

						if !excluded {
							// Look up the NVMe PCIe controller this disk is connected to
							// so we can carry its driver name/version down to the disk entry.
							pci := diskKeyToHbaPci[disk.Key]
							driverName := nvmeDriverName[pci]
							driverVer := ""
							if info, ok := hba91Firmware[pci]; ok {
								driverVer = info.Driver
							}

							raw.Disks = append(raw.Disks, RawDiskDevice{
								DeviceName: diskName,
								DeviceType: "vSAN SSD",
								Vendor:     vendor,
								Model:      model,
								Firmware:   strings.TrimSpace(disk.Revision),
								DriverName: driverName,
								DriverVer:  driverVer,
							})
						}
					}
				}
			}
		}
	}

	return &raw
}

// isAPIVersionAtLeast returns true if apiVersion >= minVersion (dot-separated integers).
func isAPIVersionAtLeast(apiVersion, minVersion string) bool {
	if apiVersion == "" {
		return false
	}
	aParts := strings.Split(apiVersion, ".")
	mParts := strings.Split(minVersion, ".")
	for i := 0; i < len(mParts); i++ {
		if i >= len(aParts) {
			return false
		}
		a, _ := strconv.Atoi(aParts[i])
		m, _ := strconv.Atoi(mParts[i])
		if a > m {
			return true
		}
		if a < m {
			return false
		}
	}
	return true
}

// getHBAFirmwareViaSoap retrieves HBA firmwareVersion and driverVersion via a raw SOAP call.
// This is needed for vSphere 9.1+ where these fields exist on HostHostBusAdapter but govmomi
// does not yet model them in its Go struct types.
func getHBAFirmwareViaSoap(ctx context.Context, client *govmomi.Client, hostRef types.ManagedObjectReference) (map[string]struct{ Firmware, Driver string }, error) {
	result := make(map[string]struct{ Firmware, Driver string })

	ctx, cancel := context.WithTimeout(ctx, soapCallTimeout)
	defer cancel()

	sc := client.Client.Client // *soap.Client (embedded in vim25.Client)
	sdkURL := sc.URL()

	// Defense-in-depth: escape the MoRef before interpolating it into the SOAP
	// body. hostRef.Value comes from govmomi (not user input), so this is safe
	// today, but escaping removes an XML-injection footgun if the source changes.
	var moref bytes.Buffer
	_ = xml.EscapeText(&moref, []byte(hostRef.Value))

	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:vim25="urn:vim25">
  <soapenv:Body>
    <vim25:RetrievePropertiesEx>
      <vim25:_this type="PropertyCollector">propertyCollector</vim25:_this>
      <vim25:specSet>
        <vim25:propSet>
          <vim25:type>HostSystem</vim25:type>
          <vim25:pathSet>config.storageDevice.hostBusAdapter</vim25:pathSet>
        </vim25:propSet>
        <vim25:objectSet>
          <vim25:obj type="HostSystem">%s</vim25:obj>
        </vim25:objectSet>
      </vim25:specSet>
      <vim25:options/>
    </vim25:RetrievePropertiesEx>
  </soapenv:Body>
</soapenv:Envelope>`, moref.String())

	req, err := http.NewRequestWithContext(ctx, "POST", sdkURL.String(), strings.NewReader(soapBody))
	if err != nil {
		return result, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", "urn:vim25/8.0")

	var bodyData []byte
	if err := sc.Do(ctx, req, func(resp *http.Response) error {
		if resp.StatusCode != 200 {
			return fmt.Errorf("SOAP call returned HTTP %d", resp.StatusCode)
		}
		bodyData, err = io.ReadAll(resp.Body)
		return err
	}); err != nil {
		return result, err
	}

	return parseHBAFirmwareFromSOAP(bodyData), nil
}

// parseHBAFirmwareFromSOAP scans a raw SOAP XML response and extracts pci -> firmware/driver.
// Uses a token scanner to remain namespace-agnostic and handle any HBA subtype name.
func parseHBAFirmwareFromSOAP(data []byte) map[string]struct{ Firmware, Driver string } {
	result := make(map[string]struct{ Firmware, Driver string })
	dec := xml.NewDecoder(bytes.NewReader(data))

	var (
		inHBA       bool
		pci, fw, dv string
		cur         string
	)

	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			local := t.Name.Local
			if local == "HostHostBusAdapter" || (strings.HasPrefix(local, "Host") && strings.HasSuffix(local, "Hba")) {
				inHBA = true
				pci, fw, dv, cur = "", "", "", ""
			} else if inHBA {
				cur = local
			}
		case xml.EndElement:
			local := t.Name.Local
			if inHBA && (local == "HostHostBusAdapter" || (strings.HasPrefix(local, "Host") && strings.HasSuffix(local, "Hba"))) {
				if pci != "" && (fw != "" || dv != "") {
					result[pci] = struct{ Firmware, Driver string }{fw, dv}
				}
				inHBA = false
				cur = ""
			} else if inHBA {
				cur = ""
			}
		case xml.CharData:
			if inHBA && cur != "" {
				text := strings.TrimSpace(string(t))
				switch cur {
				case "pci":
					pci = text
				case "firmwareVersion":
					fw = text
				case "driverVersion":
					dv = text
				}
			}
		}
	}
	return result
}
