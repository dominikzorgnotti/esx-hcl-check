# **esx-hcl-check**

`esx-hcl-check` is a command-line tool designed for vSphere and VMware Cloud Foundation (VCF) administrators. It connects to a vCenter server, extracts the exact hardware inventory of your ESXi hosts, and maps those components to precise search queries on the Broadcom VMware Compatibility Guide.

Because Broadcom does not provide a public REST API for unauthenticated HCL queries, this tool automatically generates the exact URLs needed to verify your System chassis, CPU, and I/O devices (Network, Fibre Channel, RAID, GPUs). It natively handles complex tasks like extracting binary CPUID instruction sets for accurate processor matching and deduplicating PCI devices using hex-formatted Vendor, Device, and Sub-Device IDs (VID, DID, SSID).

## **🛠️ Requirements for Building the Code**

To compile this code from source, you will need:

* **Go (Golang):** Version 1.18 or higher is recommended.  
* **Network Access:** To download the required `govmomi` SDK dependencies.

**Build Instructions:**

1. Clone or download the repository to your local machine.

Initialize the Go module and fetch the required dependencies:

```
go mod init esx-hcl-check
go mod tidy  
```

2. Build the executable:

   `go build \-o esx-hcl-check .`

## **🚀 Basic Usage (Connection Parameters)**

`esx-hcl-check` uses the same environmental connection variables as the standard `govc` CLI tool. You must set these variables in your terminal environment before running the tool so it knows how to authenticate with your vCenter server.

**Linux / macOS:**

export GOVC\_URL="vcsa.yourdomain.com"  
export GOVC\_USERNAME="administrator@vsphere.local"  
export GOVC\_PASSWORD="YourSecurePassword\!"  
export GOVC\_INSECURE=1

**Windows (PowerShell):**

$env:GOVC\_URL="vcsa.yourdomain.com"  
$env:GOVC\_USERNAME="administrator@vsphere.local"  
$env:GOVC\_PASSWORD="YourSecurePassword\!"  
$env:GOVC\_INSECURE="1"

Once your variables are set, simply run the tool:

./esx-hcl-check

## **⚙️ Optional Command Line Parameters**

The tool provides several command-line flags to filter your scope and control the output format.

| Flag | Description | Default |
| ----- | ----- | ----- |
| `-release` | The target ESXi release version to validate compatibility against. | `"ESXi 9.1"` |
| `-dc` | Target a specific Datacenter. Overrides the GOVC\_DATACENTER variable. | `""` (All Datacenters) |
| `-cluster` | Target a specific Cluster. Overrides the GOVC\_CLUSTER variable. | `""` (All Clusters) |
| `-unique` | Aggregates and deduplicates all hardware findings globally across all scanned hosts. | `false` |
| `-json` | Outputs the final HCL evaluation results as a JSON payload instead of a text table. | `false` |
| `-details` | Includes raw hardware identifiers (VID, DID, SSID, CPUID) in the JSON output payload. | `false` |
| `-vsan` | **\[BETA\]** Extracts vSAN SSD NVMe drives. Work in progress, results may not map reliably. | `false` |
| `-debugpci` | Bypasses I/O filters and dumps all unknown PCI devices into the raw JSON file for troubleshooting. | `false` |
| `-vspherejson` | Path to save the raw hardware data extracted from vCenter (Phase 1). | OS Temp Directory |
| `-nohcl` | Skips the Broadcom HCL validation phase entirely. Useful if you only want to extract the raw vSphere hardware JSON payload. | `false` |

### **Usage Examples**

**Check compatibility for an entire datacenter and aggregate the components globally:**

`./esx-hcl-check -dc="Datacenter-London" -unique`

**Extract detailed troubleshooting hardware and vSAN disks to a JSON file without HCL URLs:**

`./esx-hcl-check -nohcl -vsan -debugpci -vspherejson="/opt/reports/debug-hardware.json"`

