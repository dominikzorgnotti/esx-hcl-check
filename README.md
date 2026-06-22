# esx-hcl-check

`esx-hcl-check` is a command-line tool designed for vSphere and VMware Cloud Foundation (VCF) administrators. It connects to a vCenter server, extracts the exact hardware inventory of your ESXi hosts, and maps those components to precise search queries on the Broadcom VMware Compatibility Guide.

Because Broadcom does not provide a public REST API for unauthenticated HCL queries, this tool automatically generates the exact URLs needed to verify your System chassis, CPU, and I/O devices (Network, Fibre Channel, RAID, GPUs). It natively handles complex tasks like extracting binary CPUID instruction sets for accurate processor matching and deduplicating PCI devices using hex-formatted Vendor, Device, and Sub-Device IDs (VID, DID, SSID).



## Basic Usage (Connection Parameters)

esx-hcl-check uses the same environmental connection variables as the standard govc CLI tool. You must set these variables in your terminal environment before running the tool so it knows how to authenticate with your vCenter server.

Linux / macOS:
```
export GOVC_URL="vcsa.yourdomain.com"
export GOVC_USERNAME="administrator@vsphere.local"
export GOVC_PASSWORD="YourSecurePassword!"
export GOVC_INSECURE=1
```

Windows (PowerShell):
```
$env:GOVC_URL="vcsa.yourdomain.com"
$env:GOVC_USERNAME="administrator@vsphere.local"
$env:GOVC_PASSWORD="YourSecurePassword!"
$env:GOVC_INSECURE="1"
```

Once your variables are set, simply run the tool:

`./esx-hcl-check`

## Optional Command Line Parameters

The tool provides several command-line flags to filter your scope and control the output format.

Flag	Description	Default
```
-release	The target ESXi release version to validate compatibility against.	"ESXi 9.1"
-dc	Target a specific Datacenter. Overrides the GOVC_DATACENTER variable.	"" (All Datacenters)
-cluster	Target a specific Cluster. Overrides the GOVC_CLUSTER variable.	"" (All Clusters)
-json	Outputs the final HCL evaluation results as a JSON payload instead of a text table.	false
-vspherejson	Path to save the raw hardware data extracted from vCenter (Phase 1).	OS Temp Directory
-nohcl	Skips the Broadcom HCL validation phase entirely. Useful if you only want to extract the raw vSphere hardware JSON payload.	false
```

## Usage Examples

Target a specific cluster and check for ESXi 8.0 U3 compatibility:

`./esx-hcl-check -cluster="Compute-Cluster-01" -release="ESXi 8.0 U3"`
Extract hardware to a specific JSON file without generating HCL URLs:

`./esx-hcl-check -nohcl -vspherejson="/opt/reports/raw-hardware.json"`
Output full results in JSON format for a CI/CD pipeline:

`./esx-hcl-check -dc="Datacenter-London" -json`


## Requirements for Building the Code

To compile this code from source, you will need:

- Go (Golang): Version 1.18 or higher is recommended.
- Network Access: To download the required govmomi SDK dependencies.

Build Instructions:

- Clone or download the repository to your local machine.
 Initialize the Go module and fetch the required dependencies:
```
go mod init esx-hcl-check
go mod tidy
```
Build the executable:
```go build -o esx-hcl-check . ```
