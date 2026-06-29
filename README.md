# **esx-hcl-check**

`esx-hcl-check` is a command-line tool designed for vSphere and VMware Cloud Foundation (VCF) administrators. It connects to a vCenter server, extracts the exact hardware inventory of your ESXi hosts, and automatically verifies those components against the Broadcom VMware Compatibility Guide API and the offline vSAN database.

The tool natively handles complex extraction tasks such as parsing binary CPUID instruction sets, identifying PCI bus architectures to distinguish between standard HBAs and NVMe drives, and extracting underlying BIOS, Firmware, and Driver versions directly from the hypervisor. It provides a clear `TRUE` or `FALSE` certification status for System chassis, Processors, I/O devices, and vSAN SSDs.

## **🚀 Downloads **

Check out the [releases](https://github.com/dominikzorgnotti/esx-hcl-check/releases) to find the latest binaries for your system.

## **🛠️ Requirements for Building the Code**

To compile this code from source, you will need:

* **Go (Golang):** Version 1.18 or higher is recommended.  
* **Network Access:** To download the required `govmomi` SDK dependencies.

**Build Instructions:**

1. Clone or download the repository to your local machine.

Initialize the Go module and fetch the required dependencies:
```bash
go mod init esx-hcl-check
go mod tidy  
```

2. Build the executable:

```bash
 go build -o esx-hcl-check .
```

## **🚀 Basic Usage (Connection Parameters)**

`esx-hcl-check` uses the same environmental connection variables as the standard `govc` CLI tool. You must set these variables in your terminal environment before running the tool.

**Linux / macOS:**

```bash
export GOVC_URL="vcsa.yourdomain.com"  
export GOVC_USERNAME="administrator@vsphere.local"  
export GOVC_PASSWORD="YourSecurePassword!"  
export GOVC_INSECURE=1
```

**Windows (PowerShell):**

```powershell
$env:GOVC_URL="vcsa.yourdomain.com"  
$env:GOVC_USERNAME="administrator@vsphere.local"  
$env:GOVC_PASSWORD="YourSecurePassword!"  
$env:GOVC_INSECURE="1"
```

Once your variables are set, run the tool with the mandatory release parameter:

```bash
./esx-hcl-check -release="ESXi 9.1"
```

## **⚙️ Command Line Parameters**

| Flag | Description | Default |
| ----- | ----- | ----- |
| `-release` | **[REQUIRED]** The target ESXi release version to validate compatibility against (e.g., "ESXi 9.1", "ESXi 8.0 U3"). | *None* |
| `-dc` | Target a specific Datacenter. Overrides the GOVC_DATACENTER variable. | `""` |
| `-cluster` | Target a specific Cluster. Overrides the GOVC_CLUSTER variable. | `""` |
| `-unique` | Aggregates and deduplicates all hardware findings globally across all scanned hosts. | `false` |
| `-exclude` | Path to the JSON file containing rules to drop specific devices from the scan. | `exclude.json` |
| `-vsanhcl` | Path to the local vSAN HCL offline JSON database. Automatically downloaded if missing or outdated. | `vsan-offline-hcl.json` |
| `-unsupported` | Filters the output to ONLY show hardware components that are NOT certified. | `false` |
| `-mismatch` | Filters the output to ONLY show certified hardware that has an unsupported Firmware or Driver installed. | `false` |
| `-json` | Outputs the final HCL evaluation results as a JSON payload instead of a text table. | `false` |
| `-details` | Includes raw hardware identifiers (VID, DID, SVID, SSID) and supported firmware/driver arrays in the JSON. *Auto-enables -json*. | `false` |
| `-vsan` | Extracts vSAN SSDs and NVMe drives and checks them against the vSAN HCL database. | `false` |
| `-quiet` | Suppresses the Issues section that lists devices for which firmware/driver information could not be retrieved. | `false` |
| `-debugpci` | Bypasses I/O filters and dumps all unknown PCI devices into the raw JSON file for troubleshooting. | `false` |
| `-nohcl` | Skips the Broadcom HCL validation phase entirely. Useful to just extract the vSphere hardware payload. | `false` |

### **💡 Usage Examples**

**1. Find non-certified components globally across your environment:**

```
./esx-hcl-check -release="ESXi 9.1" -unique -unsupported
```

**2. Find certified hardware running the WRONG firmware or driver:**

```
./esx-hcl-check -release="ESXi 8.0 U3" -mismatch
```

**3. Export a complete, deduplicated, detailed JSON payload for CI/CD or reporting (includes supported driver/FW lists):**

```
./esx-hcl-check -release="ESXi 9.1" -unique -json -details -vsan
```

## **🧠 Architecture: vSAN Offline DB & Firmware Limitations**

Because Broadcom's live REST API does not easily expose deep firmware and driver matrices for unauthenticated queries, this tool relies on a hybrid approach for maximum accuracy:

### **The vSAN Offline Database (`vsan-offline-hcl.json`)**

To reliably validate I/O controllers, NICs, and vSAN SSDs, the tool automatically downloads Broadcom's comprehensive vSAN Offline JSON Database. It caches this file locally (updating it automatically if it is older than 24 hours).

During the HCL verification phase, the tool first searches this offline database using exact hex identifiers (VID, DID, SVID, SSID) or Disk model/vendor combinations. If a match is found, the tool can definitively cross-reference the exact driver and firmware installed on your host against the arrays of certified versions listed in the database. This allows for the precise `-mismatch` filtering feature.

### **Live API Fallback & Storage Device Limitations**

If a component (such as a generic storage HBA or an older disk) is not found in the vSAN Offline DB, the tool seamlessly falls back to querying the live Broadcom Compatibility Guide API.

**Important Limitations:**

* **Firmware Extraction:** The standard vSphere hypervisor API (which this tool uses) does not natively expose the firmware versions for standard PCI HBAs and NICs without executing privileged `esxcli` commands.  
* **NVMe Vendors:** vSphere often translates the vendor of directly-attached NVMe drives generically as "NVMe", moving the true vendor (e.g., Dell, Samsung) into the model string. The tool automatically tokenizes these strings to perform intelligent matching.  
* **API Limitations:** When falling back to the live Broadcom API, the tool can verify if the hardware baseline is certified (`TRUE/FALSE`), but cannot definitively certify the exact Firmware/Driver combination. In these cases, the `drv certified` and `fw certified` columns will gracefully report `N/A`.

## **🛡️ Excluding Specific Devices**

In large environments, you may want to ignore non-critical components (like integrated AHCI controllers or USB bridges) to prevent them from cluttering your reports. You can achieve this by creating an `exclude.json` file in your working directory.

You can filter devices using three different methods:

1. **names:** An exact string match of the device name.  
2. **regex_names:** A Regular Expression applied to the device name.  
3. **ids:** Specific hexadecimal hardware identifiers (VID, DID, SVID, SSID).

**Example `exclude.json` Payload:**

```bash
{  
  "names": [  
    "Lewisburg SATA AHCI Controller",  
    "VMware NVMe Controller"  
  ],  
  "regex_names": [  
    "Lewisburg.*",  
    "(?i)^intel.*usb.*"  
  ],  
  "ids": [  
    {  
      "vid": "8086",  
      "did": "a1d2"  
    },  
    {  
      "vid": "15b3",  
      "ssid": "0091"  
    }  
  ]  
}
```
