# esx-hcl-check
A tool that allows a vSphere/VCF administrator to check if the host hardware in a given vCenter is certified for a VCF/ESXi release, e.g. “VCF/ESXi 9.1”


## Build code

To run or compile this tool, you will need to initialize a Go module and fetch the required govmomi dependencies:

```bash
go mod init esx-hcl-check
go get github.com/vmware/govmomi
go get github.com/vmware/govmomi/find
go get github.com/vmware/govmomi/property
go get github.com/vmware/govmomi/vim25
go get github.com/vmware/govmomi/vim25/mo
go get github.com/vmware/govmomi/vim25/types
```
