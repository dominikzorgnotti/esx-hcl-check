package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
)

func main() {
	var (
		jsonOutput  = flag.Bool("json", false, "Output final HCL results in JSON format")
		dcTarget    = flag.String("dc", os.Getenv("GOVC_DATACENTER"), "Target datacenter (optional)")
		clsTarget   = flag.String("cluster", os.Getenv("GOVC_CLUSTER"), "Target cluster (optional)")
		esxiRelease = flag.String("release", "", "REQUIRED: Target ESXi version for compatibility validation")
		vsphereJson = flag.String("vspherejson", "", "Path to save the raw vSphere hardware JSON (defaults to OS temp dir)")
		noHCL       = flag.Bool("nohcl", false, "Skip the HCL check phase and only collect vSphere data")
		detailsOut  = flag.Bool("details", false, "Include unique IDs (VID, DID, SSID, CPUID) in the JSON output")
		debugPci    = flag.Bool("debugpci", false, "Bypass PCI filters and dump all raw PCI devices into the JSON file for troubleshooting")
		vsanBeta    = flag.Bool("vsan", false, "Extract vSAN SSD and NVMe disks and check against the vSAN HCL database")
		uniqueOut   = flag.Bool("unique", false, "Aggregate and deduplicate output across all hosts globally")
		excludeFile = flag.String("exclude", "exclude.json", "Path to the exclude JSON file to filter out specific devices")
		vsanHclFile = flag.String("vsanhcl", "vsan-offline-hcl.json", "Path to the local vSAN HCL offline JSON database")
		unsupported = flag.Bool("unsupported", false, "Filter output to ONLY show hardware that is not certified")
		mismatch    = flag.Bool("mismatch", false, "Filter output to ONLY show hardware that is certified but has a driver/firmware mismatch")
		quiet       = flag.Bool("quiet", false, "Suppress warnings about missing firmware/driver information")
	)
	flag.Parse()

	if *esxiRelease == "" {
		fmt.Fprintln(os.Stderr, "Error: The -release parameter is mandatory.")
		fmt.Fprintln(os.Stderr, "Hint: The input should match the 'Product Release Version' on the Compatibility Guide, e.g. 'ESXi 9.1' or 'ESXi 8.0 U3'")
		os.Exit(2)
	}

	if *detailsOut {
		*jsonOutput = true
	}

	// Load Exclude Rules
	var excludeCfg ExcludeConfig
	if *excludeFile != "" {
		if b, err := os.ReadFile(*excludeFile); err == nil {
			if err := json.Unmarshal(b, &excludeCfg); err == nil {
				for _, expr := range excludeCfg.RegexNames {
					if re, err := regexp.Compile(expr); err == nil {
						excludeCfg.CompiledRegexes = append(excludeCfg.CompiledRegexes, re)
					}
				}
			}
		}
	}

	ctx := context.Background()

	// ---------------------------------------------------------
	// PHASE 1: Data Collection
	// ---------------------------------------------------------
	client, err := connectToVC(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to vCenter: %v\n", err)
		os.Exit(2)
	}
	
	if !*jsonOutput {
		fmt.Fprintf(os.Stderr, "# Connecting to %s ...\n", client.Client.URL().Host)
		fmt.Fprintln(os.Stderr, "# Collecting inventory and hardware data...")
	}

	rawInventory, err := collectVSphereData(ctx, client, *dcTarget, *clsTarget, *debugPci, *vsanBeta, excludeCfg)
	if err != nil {
		client.Logout(ctx)
		fmt.Fprintf(os.Stderr, "Error discovering inventory: %v\n", err)
		os.Exit(2)
	}
	client.Logout(ctx)

	savedPath, err := saveRawInventory(rawInventory, *vsphereJson)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save raw inventory JSON: %v\n", err)
		os.Exit(2)
	}

	if !*jsonOutput {
		fmt.Fprintf(os.Stderr, "# Raw inventory saved to: %s\n\n", savedPath)
	}

	if *noHCL {
		return
	}

	// ---------------------------------------------------------
	// PHASE 2: HCL Verification (API + vSAN DB)
	// ---------------------------------------------------------
	hclResults := performHCLChecks(rawInventory, *esxiRelease, *detailsOut, *debugPci, *vsanHclFile)

	// Compute the exit code from the complete result set, before -unique/filters
	// drop entries, so the process status always reflects the true findings.
	exitCode := computeExitCode(hclResults)

	if *uniqueOut {
		hclResults = aggregateUnique(hclResults)
	}

	// Apply Filters
	hclResults = applyFilters(hclResults, *unsupported, *mismatch)

	// ---------------------------------------------------------
	// PHASE 3: Output Formatting
	// ---------------------------------------------------------
	if *jsonOutput {
		printJSON(hclResults, *quiet)
	} else {
		printText(hclResults, *quiet)
	}

	os.Exit(exitCode)
}

// computeExitCode maps scan findings to a process exit code for CI/CD gating:
//
//	0 — every component is certified (or not applicable)
//	1 — at least one component is definitively NOT certified
//	2 — the scan could not be fully determined: a host/cluster was skipped, or
//	    an HCL lookup failed (CertError). 2 takes precedence over 1, because an
//	    incomplete answer should not read as a clean "only known-bad found".
//
// Fatal run errors (connect/collect/save) also exit 2, handled at their sites.
func computeExitCode(data []HostComponents) int {
	foundUncertified := false
	for _, host := range data {
		if host.SkipReason != "" {
			return 2
		}
		for _, res := range host.Results {
			if res.Certified == CertError {
				return 2
			}
			if res.Certified == CertFalse {
				foundUncertified = true
			}
		}
	}
	if foundUncertified {
		return 1
	}
	return 0
}

func applyFilters(data []HostComponents, showUnsupported, showMismatch bool) []HostComponents {
	if !showUnsupported && !showMismatch {
		return data
	}

	var filtered []HostComponents
	for _, host := range data {
		// Always keep skipped hosts/clusters visible — a host we could not scan
		// is exactly the kind of thing -unsupported/-mismatch users need to see.
		if host.SkipReason != "" {
			filtered = append(filtered, host)
			continue
		}
		var keepRes []HCLResult
		for _, res := range host.Results {
			if showUnsupported && res.Certified == CertFalse {
				keepRes = append(keepRes, res)
			} else if showMismatch && res.Certified == CertTrue && (res.DriverCertified == CertFalse || res.FirmwareCertified == CertFalse) {
				keepRes = append(keepRes, res)
			}
		}
		if len(keepRes) > 0 {
			host.Results = keepRes
			filtered = append(filtered, host)
		}
	}
	return filtered
}
