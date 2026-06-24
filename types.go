package main

// --- Structs for Phase 1: Raw vSphere Data Collection ---

// RawPCIDevice holds the raw hardware IDs for a single PCI device.
type RawPCIDevice struct {
	DeviceName string `json:"device_name"`
	DeviceType string `json:"device_type"`
	VID        int16  `json:"vid"`
	DID        int16  `json:"did"`
	SVID       int16  `json:"svid"`
	SSID       int16  `json:"ssid"`
	Firmware   string `json:"firmware"`
	DriverVer  string `json:"driver_version"`
	DriverName string `json:"driver_name"`
}

// RawDiskDevice holds the raw vendor and model for a storage disk.
type RawDiskDevice struct {
	DeviceName string `json:"device_name"`
	DeviceType string `json:"device_type"`
	Vendor     string `json:"vendor"`
	Model      string `json:"model"`
	Firmware   string `json:"firmware"`
}

// RawHostData holds the unanalyzed hardware inventory for a single ESXi host.
type RawHostData struct {
	Datacenter  string          `json:"datacenter"`
	Cluster     string          `json:"cluster"`
	Hostname    string          `json:"hostname"`
	SysVendor   string          `json:"sys_vendor"`
	SysModel    string          `json:"sys_model"`
	BiosVersion string          `json:"bios_version"`
	CpuModel    string          `json:"cpu_model"`
	CpuId       string          `json:"cpu_id"`
	PCIDevices  []RawPCIDevice  `json:"pci_devices"`
	Disks       []RawDiskDevice `json:"disks"`
}

// --- Structs for Phase 2: HCL Verification ---

// HCLResult represents the certification status of a single hardware component.
type HCLResult struct {
	Device     string `json:"device"`
	DeviceType string `json:"device_type"`
	Instances  int    `json:"number_of_instances"`
	Firmware   string `json:"current_firmware"`
	DriverVer  string `json:"current_driver_version"`
	DriverName string `json:"driver_name"`
	Certified  string `json:"certified"`
	HCLLink    string `json:"hcl"`

	// Detailed hardware IDs (conditionally populated via -details flag)
	VID   string `json:"vid,omitempty"`
	DID   string `json:"did,omitempty"`
	SVID  string `json:"svid,omitempty"`
	SSID  string `json:"ssid,omitempty"`
	CPUID string `json:"cpu_id,omitempty"`
}

// HostComponents holds the HCL results for a single ESXi host.
type HostComponents struct {
	Datacenter string
	Cluster    string
	Hostname   string
	Results    []HCLResult
}
