# **esx-hcl-check**

`esx-hcl-check` is a command-line tool designed for vSphere and VMware Cloud Foundation (VCF) administrators. It connects to a vCenter server, extracts the exact hardware inventory of your ESXi hosts, and automatically verifies those components against the Broadcom VMware Compatibility Guide API and the offline vSAN database.

The tool natively handles complex extraction tasks such as parsing binary CPUID instruction sets, identifying PCI bus architectures to distinguish between standard HBAs and NVMe drives, and extracting BIOS, firmware, and driver versions directly from the hypervisor — all through the vSphere API, **without ever running `esxcli` on a host**. It reports a per-component verdict (`TRUE`, `FALSE`, `N/A`, `ERROR`, or `SKIPPED`) for the system chassis, processors, I/O devices, and vSAN disks, collects hosts in parallel, and returns a findings-based exit code so it can gate upgrades directly in CI/CD. It can also run fully air-gapped.

## **🚀 Downloads**

Check out the [releases](https://github.com/dominikzorgnotti/esx-hcl-check/releases) to find the latest binaries for your system. Release binaries are built with the [SLSA3](https://slsa.dev) Go builder and ship with signed build provenance — see [Verifying Release Binaries](#-verifying-release-binaries).

## **🛠️ Requirements for Building the Code**

To compile this code from source, you will need:

* **Go (Golang):** Version 1.18 or higher is recommended.  
* **Network Access:** To download the required `govmomi` SDK dependencies.

**Build Instructions:**

1. Clone the repository to your local machine. Dependencies are pinned in the committed `go.mod` / `go.sum` and fetched automatically on first build.

2. Build the executable:

```bash
 go build -o esx-hcl-check .
```

To stamp a version into the binary (reported by `-version`), pass it via `-ldflags`:

```bash
 go build -ldflags "-X main.version=v0.4.0 -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o esx-hcl-check .
```

Official release binaries are stamped automatically. A plain `go build` reports version `dev`.

## **🔏 Verifying Release Binaries**

Every release binary is built by the [SLSA3](https://slsa.dev) Go builder and published alongside a signed provenance attestation (`<binary>.intoto.jsonl`) that proves it was built from this repository, at the released tag, by the trusted builder — not tampered with in transit or produced on someone's laptop.

To verify a download, install [`slsa-verifier`](https://github.com/slsa-framework/slsa-verifier) and run:

```bash
slsa-verifier verify-artifact \
  esx-hcl-check-linux-amd64 \
  --provenance-path esx-hcl-check-linux-amd64.intoto.jsonl \
  --source-uri github.com/dominikzorgnotti/esx-hcl-check \
  --source-tag v0.4.1
```

A `PASSED: SLSA verification passed` result confirms the binary's origin and integrity.

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
| `-offline` | Run without internet access. Skips all Broadcom Compatibility Guide API checks (affected components are marked `SKIPPED`) and verifies only against the local vSAN HCL database. See [Offline / Air-Gapped Operation](#-offline--air-gapped-operation). | `false` |
| `-unsupported` | Filters the output to ONLY show hardware components that are NOT certified. | `false` |
| `-mismatch` | Filters the output to ONLY show certified hardware that has an unsupported Firmware or Driver installed. | `false` |
| `-json` | Outputs the final HCL evaluation results as a JSON payload instead of a text table. | `false` |
| `-csv` | Outputs the results as CSV (one row per device) to stdout, with the same detail as `-json`. Redirect with `> report.csv`. Takes precedence over `-json`. | `false` |
| `-details` | Includes raw hardware identifiers (VID, DID, SVID, SSID) in the output. Auto-enables `-json` (unless `-csv` is set, giving a detailed CSV). | `false` |
| `-vsan` | Extracts vSAN SSDs and NVMe drives and checks them against the vSAN HCL database. | `false` |
| `-quiet` | Suppresses the Issues section that lists devices for which firmware/driver information could not be retrieved. | `false` |
| `-workers` | How many hosts to collect from at once. **`1` runs fully sequentially** (one host at a time); higher values collect that many hosts in parallel. Valid range is `1`–`8` (hard maximum `8`): values above `8` are capped, and values below `1` are rejected. Use `1` in constrained or rate-sensitive environments. | `4` |
| `-stats` | Emits run statistics: inventory counts (datacenters, clusters, hosts, IO cards, storage devices) and query timings (vCenter, Broadcom HCL, vSAN DB). In JSON this adds a top-level `stats` object; in text, a Statistics section. | `false` |
| `-debugpci` | Bypasses I/O filters and dumps all unknown PCI devices into the raw JSON file for troubleshooting. | `false` |
| `-nohcl` | Skips the Broadcom HCL validation phase entirely. Useful to just extract the vSphere hardware payload. | `false` |
| `-vspherejson` | Path to save the raw collected vSphere hardware inventory as JSON. If empty, a file is written to the OS temp directory. | `""` (temp dir) |
| `-version` | Prints the version, commit, and build date, then exits. | `false` |

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

**4. Export a spreadsheet for Excel / asset management:**

```
./esx-hcl-check -release="ESXi 9.1" -unique -vsan -details -csv > hcl-report.csv
```

**5. Gate an upgrade in CI/CD (the exit code drives the pipeline):**

```bash
./esx-hcl-check -release="ESXi 9.1" -unique -json > report.json
case $? in
  0) echo "All certified — proceed with the upgrade." ;;
  1) echo "Uncertified hardware found — block." ; exit 1 ;;
  2) echo "Result undetermined (skipped host, lookup error, or -offline) — needs review." ; exit 1 ;;
esac
```

## **🔌 Offline / Air-Gapped Operation**

`esx-hcl-check` relies on two Broadcom internet endpoints — the Compatibility Guide API and the vSAN offline HCL database. Before the HCL phase runs, the tool **probes both** (unless `-nohcl` or `-offline` is set). If either is unreachable, it aborts early with a clear message naming the failed endpoint(s) and three ways forward:

* Verify the host has internet access to `broadcom.com`
* Configure a proxy via the `HTTPS_PROXY` / `HTTP_PROXY` / `NO_PROXY` environment variables (these are honored automatically; note that a **TLS-intercepting** proxy typically surfaces as a *certificate* error instead)
* Re-run with `-offline`

### **`-offline` mode**

`-offline` runs a **partial** verification without any internet access:

* All Broadcom Compatibility Guide API checks are skipped. Components that can only be verified via that API — the system chassis, the CPU, and any I/O device not found locally — are reported as **`SKIPPED`**.
* I/O controllers, NICs, and vSAN SSDs that **are** present in the local vSAN offline HCL database still receive a real `TRUE`/`FALSE` verdict.
* The vSAN database is **not** auto-downloaded. Place it on disk beforehand at the default path (`vsan-offline-hcl.json`) or point to it with `-vsanhcl <path>`. If it is missing, the tool tells you to download it from `https://vvs.broadcom.com/service/vsan/all.json`.

Because an offline run cannot fully determine certification, it exits with code `2` (see below).

## **🚦 Exit Codes**

The process exit code reflects the scan findings, so `esx-hcl-check` can be used directly as a pre-upgrade gate in CI/CD without parsing its output:

| Code | Meaning |
| ----- | ----- |
| `0` | Every scanned component is certified (or not applicable). |
| `1` | At least one component is definitively **not** certified. |
| `2` | The result could not be fully determined — a host or cluster was skipped (disconnected, in maintenance, or a permissions/API error), an HCL lookup failed, a check was skipped (`SKIPPED`, e.g. under `-offline`), or a fatal run error occurred (connection, inventory, or file write). A `2` takes precedence over a `1`. |

## **🔀 Output Streams**

Only report data is written to **stdout** — the text table, or the JSON payload with `-json`. All diagnostics (progress messages, warnings, and errors) go to **stderr**. This means you can safely pipe or redirect the report (`> report.json`) without diagnostics contaminating it.

## **📊 Run Statistics (`-stats`)**

Pass `-stats` to measure a run. It reports how much inventory was collected and how long each external dependency took — useful for sizing large environments and for seeing the effect of `-workers`.

With `-json`, a `stats` object is added at the top level, alongside `results` and `issues`:

```json
{
  "results": [ ... ],
  "issues":  [ ... ],
  "stats": {
    "datacenters": 1,
    "clusters": 4,
    "hosts": 15,
    "io_cards": 100,
    "storage_devices": 30,
    "vcenter_query_ms": 1234,
    "broadcom_hcl_query_ms": 5678,
    "vsan_db_query_ms": 234
  }
}
```

Without `-json`, the same figures are printed as a **Statistics** section (Inventory + Runtime). The `broadcom_hcl_query_ms` timing counts only live calls, so with cross-host de-duplication it reflects the true network cost rather than the number of devices.

## **⏭️ Skipped Hosts**

Hosts that cannot be scanned are no longer silently dropped. Each appears in the output with a `skip_reason` (e.g. `host not connected (state: disconnected)`, `property-collector-error: ...`, or `could not enumerate hosts: ...`), and the JSON also carries a `source` field identifying the originating vCenter.

## **🧠 Architecture: How Verification Works**

`esx-hcl-check` runs in three phases: it **collects** a hardware inventory from vCenter, **verifies** each component against Broadcom's HCL data, then **reports** the result. A deliberate design constraint shapes the whole tool:

> **It never runs `esxcli` or any command on the hosts.** Everything is read through the vSphere API (govmomi) using your existing vCenter credentials. This keeps the tool safe to run against production and requires no host-level access — at the cost of a few extraction limits noted below.

### **Phase 1 — Inventory collection (vSphere API only)**

Hosts are queried in parallel (bounded by `-workers`) via the vCenter property collector. For each host the tool extracts the system make/model and BIOS, decodes the processor's **CPUID** from the raw CPU feature bits, and enumerates every PCI device — classifying it as a NIC, Fibre Channel / RAID controller, GPU, or NVMe device — plus vSAN SSD/NVMe disks (`-vsan`).

Where it gets firmware and driver versions **without `esxcli`**:

* **NICs** — firmware, driver, and driver version come directly from the physical-NIC properties.
* **NVMe controllers** — firmware from the NVMe topology; driver name from the PCIe HBA; driver version from vSphere **9.1+** (see below).
* **Fibre Channel / RAID HBAs** — these fields are **not** modeled by the vSphere API on releases before **9.1**. On vSphere 9.1 and later the tool retrieves them through a targeted SOAP call. On older releases they are unavailable, and the affected devices are listed in the **Issues** section with the reason — rather than guessing or shelling out to `esxcli`.
* **NVMe vendor tokenization** — vSphere often reports the vendor of a direct-attached NVMe drive generically as "NVMe" and folds the real vendor (Dell, Samsung, …) into the model string. The tool tokenizes the model to recover it for matching.

### **Phase 2 — Two-source HCL verification**

Broadcom does not expose deep firmware/driver matrices through its live API for unauthenticated queries, so the tool uses two data sources and prefers the richer one:

1. **The vSAN Offline Database (local, authoritative for firmware/driver).** Each component is first matched here — by exact hex identifiers (VID, DID, SVID, SSID) for controllers/NICs/NVMe, or by vendor + model tokens for disks. A match yields not just a certified baseline but the **arrays of certified driver and firmware versions**, which the tool cross-references against what is actually installed. This is what powers the `driver_certified` / `firmware_certified` columns, the `supported_drivers` / `supported_firmwares` lists (`-details`), and the `-mismatch` filter. Driver versions are matched by prefix so vendor build suffixes don't cause false mismatches.

2. **The live Broadcom Compatibility Guide API (fallback, baseline only).** Anything not covered by the offline DB — the system chassis, the CPU, and I/O devices absent from the DB — is checked against the live API. This confirms whether the hardware **baseline** is certified (`TRUE`/`FALSE`) but cannot certify an exact firmware/driver combination, so those columns report `N/A`. Live calls are protected by timeouts and bounded retry/backoff, and identical hardware seen across many hosts is queried **once** and reused (cross-host de-duplication) to avoid hammering the endpoint.

The **vSAN Offline Database** (`vsan-offline-hcl.json`) is downloaded automatically, refreshed when older than 24 hours, and written atomically with an integrity check so an interrupted or truncated download can never corrupt the cache. Relocate it with `-vsanhcl`, or run fully air-gapped with `-offline` (see [Offline / Air-Gapped Operation](#-offline--air-gapped-operation)).

### **Certification status vocabulary**

Every result carries three verdicts — `hw_certified`, `driver_certified`, and `firmware_certified` — each one of:

| Value | Meaning |
| ----- | ----- |
| `TRUE` | Certified for the target release. |
| `FALSE` | Checked and **not** certified. |
| `N/A` | Not applicable or not determinable from the available data (e.g. a firmware verdict for a device the offline DB has no matrix for, or any driver/firmware column when only the live API baseline was available). |
| `ERROR` | A live Broadcom API lookup failed (network or parse error). Kept **distinct from `FALSE`** so a transient outage is never mistaken for "not certified". |
| `SKIPPED` | The check was not performed — e.g. an API-only check under `-offline`. |

## **🛡️ Excluding Specific Devices**

In large environments, you may want to ignore non-critical components (like integrated AHCI controllers or USB bridges) to prevent them from cluttering your reports. You can achieve this by creating an `exclude.json` file in your working directory.

You can filter devices using three different methods:

1. **names:** An exact string match of the device name.  
2. **regex_names:** A Regular Expression applied to the device name.  
3. **ids:** Specific hexadecimal hardware identifiers (VID, DID, SVID, SSID).

**Example `exclude.json` Payload:**

```json
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
