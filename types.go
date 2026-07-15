package main

import (
	"regexp"
	"strings"
)

// CertStatus is a certification verdict. It exists so the code cannot confuse a
// real answer with a lookup failure: a Broadcom API error is CertError, which is
// distinct from CertFalse ("checked, not certified"). It marshals to/from the
// historical "TRUE"/"FALSE"/"N/A"/"ERROR" strings so the -json wire format is
// unchanged. The zero value is CertNA.
type CertStatus int

const (
	CertNA      CertStatus = iota // "N/A"     — not applicable / not determined
	CertTrue                      // "TRUE"    — certified
	CertFalse                     // "FALSE"   — checked, not certified
	CertError                     // "ERROR"   — lookup failed; result unknown
	CertSkipped                   // "SKIPPED" — not checked (e.g. -offline, no live API)
)

func (c CertStatus) String() string {
	switch c {
	case CertTrue:
		return "TRUE"
	case CertFalse:
		return "FALSE"
	case CertError:
		return "ERROR"
	case CertSkipped:
		return "SKIPPED"
	default:
		return "N/A"
	}
}

func (c CertStatus) MarshalJSON() ([]byte, error) {
	return []byte(`"` + c.String() + `"`), nil
}

func (c *CertStatus) UnmarshalJSON(b []byte) error {
	*c = parseCertStatus(strings.Trim(string(b), `"`))
	return nil
}

func parseCertStatus(s string) CertStatus {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TRUE":
		return CertTrue
	case "FALSE":
		return CertFalse
	case "ERROR":
		return CertError
	case "SKIPPED":
		return CertSkipped
	default:
		return CertNA
	}
}

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
	// EsxName is the ESX-assigned adapter name (e.g. "vmnic0" for a NIC,
	// "vmhba1" for a storage HBA); empty for device classes that have none.
	// LinkState is "UP"/"DOWN" derived from the adapter's connection state, or
	// "" when not applicable. Both feed the -map output.
	EsxName   string `json:"esx_name,omitempty"`
	LinkState string `json:"link_state,omitempty"`
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
	SkipReason  string          `json:"skip_reason,omitempty"`
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
	Device   string   `json:"device"`
	Missing  []string `json:"missing"`
	Reason   string   `json:"reason,omitempty"`
}

// --- Structs for Phase 2: HCL Verification ---

type HCLResult struct {
	Device     string `json:"device"`
	DeviceType string `json:"device_type"`
	// Instances is omitempty so -map, which lists each adapter individually
	// (Instances left 0), drops number_of_instances for those rows while
	// aggregated device classes (Instances >= 1) still report it.
	Instances         int        `json:"number_of_instances,omitempty"`
	Firmware          string     `json:"current_firmware"`
	DriverVer         string     `json:"current_driver_version"`
	DriverName        string     `json:"driver_name"`
	Certified         CertStatus `json:"hw_certified"`
	DriverCertified   CertStatus `json:"driver_certified"`
	FirmwareCertified CertStatus `json:"firmware_certified"`
	// EsxName / LinkState are populated only under -map for network cards and
	// storage adapters: the ESX device name (e.g. "vmnic0") and "UP"/"DOWN".
	EsxName   string `json:"esx_device_name,omitempty"`
	LinkState string `json:"link_state,omitempty"`
	// MaxSupportedRelease is the highest ESXi release this device is certified
	// under in the vSAN offline HCL (e.g. "ESXi 9.0"), useful when it is not
	// certified for the target release. Empty when only the live API was used.
	MaxSupportedRelease string   `json:"max_supported_release,omitempty"`
	SupportedDrivers    []string `json:"supported_drivers,omitempty"`
	SupportedFirmwares  []string `json:"supported_firmwares,omitempty"`
	HCLLink             string   `json:"hcl"`

	// Detailed hardware IDs
	VID   string `json:"vid,omitempty"`
	DID   string `json:"did,omitempty"`
	SVID  string `json:"svid,omitempty"`
	SSID  string `json:"ssid,omitempty"`
	CPUID string `json:"cpu_id,omitempty"`
}

// Stats holds optional run statistics emitted when -stats is set: how much
// inventory was collected and how long the external queries took.
type Stats struct {
	// Inventory counts (from the collected raw inventory)
	Datacenters    int `json:"datacenters"`
	Clusters       int `json:"clusters"`
	Hosts          int `json:"hosts"`
	HostsSkipped   int `json:"hosts_skipped,omitempty"`
	IOCards        int `json:"io_cards"`
	StorageDevices int `json:"storage_devices"`

	// SkippedChecks counts components whose certification could not be checked
	// (e.g. -offline skipping the live Broadcom API). Omitted when zero.
	SkippedChecks int `json:"skipped_checks,omitempty"`

	// Runtime timings (milliseconds)
	VCenterQueryMs  int64 `json:"vcenter_query_ms"`
	BroadcomQueryMs int64 `json:"broadcom_hcl_query_ms"`
	VsanDBQueryMs   int64 `json:"vsan_db_query_ms"`
}

type HostComponents struct {
	Source     string          `json:"source,omitempty"`
	Datacenter string          `json:"datacenter"`
	Cluster    string          `json:"cluster"`
	Hostname   string          `json:"hostname"`
	SkipReason string          `json:"skip_reason,omitempty"`
	Results    []HCLResult     `json:"results"`
	Issues     []MissingDetail `json:"issues,omitempty"`
}
