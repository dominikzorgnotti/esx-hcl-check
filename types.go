package main

import "regexp"

// --- Structs for Phase 1: Raw vSphere Data Collection ---

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

type RawDiskDevice struct {
	DeviceName string `json:"device_name"`
	DeviceType string `json:"device_type"`
	Vendor     string `json:"vendor"`
	Model      string `json:"model"`
	Firmware   string `json:"firmware"`
	DriverName string `json:"driver_name,omitempty"`
	DriverVer  string `json:"driver_version,omitempty"`
	VID        int16  `json:"vid,omitempty"`
	DID        int16  `json:"did,omitempty"`
	SVID       int16  `json:"svid,omitempty"`
	SSID       int16  `json:"ssid,omitempty"`
}

type RawHostData struct {
	Source      string          `json:"source,omitempty"`
	Datacenter  string          `json:"datacenter"`
	Cluster     string          `json:"cluster"`
	Hostname    string          `json:"hostname"`
	APIVersion  string          `json:"api_version,omitempty"`
	SysVendor   string          `json:"sys_vendor"`
	SysModel    string          `json:"sys_model"`
	BiosVersion string          `json:"bios_version"`
	CpuModel    string          `json:"cpu_model"`
	CpuId       string          `json:"cpu_id"`
	PCIDevices  []RawPCIDevice  `json:"pci_devices"`
	Disks       []RawDiskDevice `json:"disks"`
}

// --- Exclude Configuration ---

type ExcludeID struct {
	VID  string `json:"vid"`
	DID  string `json:"did"`
	SVID string `json:"svid"`
	SSID string `json:"ssid"`
}

type ExcludeConfig struct {
	Names           []string         `json:"names"`
	RegexNames      []string         `json:"regex_names"`
	IDs             []ExcludeID      `json:"ids"`
	CompiledRegexes []*regexp.Regexp `json:"-"`
}

// --- vSAN Offline DB ---

type VsanOfflineDB struct {
	Timestamp int64 `json:"timestamp"`
	Data      struct {
		Controller []map[string]interface{} `json:"controller"`
		Hdd        []map[string]interface{} `json:"hdd"`
		Ssd        []map[string]interface{} `json:"ssd"`
		Nic        []map[string]interface{} `json:"nic"`
	} `json:"data"`
}

// --- Missing Info Tracking ---

type MissingDetail struct {
	Hostname string   `json:"hostname,omitempty"`
	Device  string   `json:"device"`
	Missing []string `json:"missing"`
	Reason  string   `json:"reason,omitempty"`
}

// --- Structs for Phase 2: HCL Verification ---

type HCLResult struct {
	Device             string   `json:"device"`
	DeviceType         string   `json:"device_type"`
	Instances          int      `json:"number_of_instances"`
	Firmware           string   `json:"current_firmware"`
	DriverVer          string   `json:"current_driver_version"`
	DriverName         string   `json:"driver_name"`
	Certified          string   `json:"hw_certified"`
	DriverCertified    string   `json:"driver_certified"`
	FirmwareCertified  string   `json:"firmware_certified"`
	SupportedDrivers   []string `json:"supported_drivers,omitempty"`
	SupportedFirmwares []string `json:"supported_firmwares,omitempty"`
	HCLLink            string   `json:"hcl"`

	// Detailed hardware IDs
	VID   string `json:"vid,omitempty"`
	DID   string `json:"did,omitempty"`
	SVID  string `json:"svid,omitempty"`
	SSID  string `json:"ssid,omitempty"`
	CPUID string `json:"cpu_id,omitempty"`
}

type HostComponents struct {
	Source     string          `json:"source,omitempty"`
	Datacenter string          `json:"datacenter"`
	Cluster    string          `json:"cluster"`
	Hostname   string          `json:"hostname"`
	Results    []HCLResult     `json:"results"`
	Issues     []MissingDetail `json:"issues,omitempty"`
}
