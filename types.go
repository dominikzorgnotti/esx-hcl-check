package main

// --- Structs for Phase 1: Raw vSphere Data Collection ---

// RawPCIDevice holds the raw hardware IDs for a single PCI device.
type RawPCIDevice struct {
	DeviceName string `json:"device_name"`
	DeviceType string `json:"device_type"`
	VID        int16  `json:"vid"`
	DID        int16  `json:"did"`
	SSID       int16  `json:"ssid"`
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
