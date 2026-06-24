# **esx-hcl-check**

`esx-hcl-check` is a command-line tool designed for vSphere and VMware Cloud Foundation (VCF) administrators. It connects to a vCenter server, extracts the exact hardware inventory of your ESXi hosts, and automatically verifies those components against the Broadcom VMware Compatibility Guide API.

The tool natively handles complex extraction tasks such as parsing binary CPUID instruction sets, identifying PCI bus architectures to distinguish between standard HBAs and NVMe drives, and extracting underlying BIOS/Firmware and Driver versions directly from the hypervisor. It validates System chassis, Processors, I/O devices (Network, Fibre Channel, RAID, GPUs), and vSAN SSDs directly against Broadcom's backend API to provide a clear `TRUE` or `FALSE` certification status.

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

Build the executable:

```bash
   go build -o esx-hcl-check .
```


## **🚀 Basic Usage (Connection Parameters)**

`esx-hcl-check` uses the same environmental connection variables as the standard `govc` CLI tool. You must set these variables in your terminal environment before running the tool so it knows how to authenticate with your vCenter server.

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

The tool provides several command-line flags to filter your scope and control the output format.

```bash
| Flag | Description | Default |
| ----- | ----- | ----- |
| `-release` | **[REQUIRED]** The target ESXi release version to validate compatibility against (e.g., "ESXi 9.1", "ESXi 8.0 U3"). Must match the Broadcom Product Release Version string. | *None* |
| `-dc` | Target a specific Datacenter. Overrides the GOVC_DATACENTER variable. | `""` (All Datacenters) |
| `-cluster` | Target a specific Cluster. Overrides the GOVC_CLUSTER variable. | `""` (All Clusters) |
| `-unique` | Aggregates and deduplicates all hardware findings globally across all scanned hosts. | `false` |
| `-json` | Outputs the final HCL evaluation results as a JSON payload. Required to view extracted firmware and driver versions. | `false` |
| `-details` | Includes raw hardware identifiers (VID, DID, SVID, SSID, CPUID) in the JSON output payload. *Automatically enables -json*. | `false` |
| `-vsan` | **[BETA]** Extracts vSAN SSDs and NVMe drives and checks them against the vSAN HCL database. | `false` |
| `-debugpci` | Bypasses I/O filters and dumps all unknown PCI devices into the raw JSON file for troubleshooting. | `false` |
| `-vspherejson` | Path to save the raw hardware data extracted from vCenter (Phase 1). | OS Temp Directory |
| `-nohcl` | Skips the Broadcom HCL validation phase entirely. Useful if you only want to extract the raw vSphere hardware JSON payload. | `false` |
```

### **Usage Examples**

**Check compatibility for an entire datacenter and aggregate the unique components globally:**

```bash
./esx-hcl-check -release="ESXi 9.1" -dc="Datacenter-London" -unique
```

**Export full detailed hardware JSON (including exact PCI hex IDs, firmwares, and drivers) for CI/CD pipelines:**

```bash
./esx-hcl-check -release="ESXi 8.0 U3" -details -vsan
```

**Extract hardware to a specific JSON file without hitting the Broadcom API:**

```bash
./esx-hcl-check -release="ESXi 9.1" -nohcl -vspherejson="/opt/reports/raw-hardware.json"
```
