package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
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
		vsanBeta    = flag.Bool("vsan", false, "BETA: Extract vSAN SSD disks")
		uniqueOut   = flag.Bool("unique", false, "Aggregate and deduplicate output across all hosts globally")
		excludeFile = flag.String("exclude", "exclude.json", "Path to the exclude JSON file to filter out specific devices")
		vsanHclFile = flag.String("vsanhcl", "vsan-offline-hcl.json", "Path to the local vSAN HCL offline JSON database")
		unsupported = flag.Bool("unsupported", false, "Filter output to ONLY show hardware that is not certified")
		mismatch    = flag.Bool("mismatch", false, "Filter output to ONLY show hardware that is certified but has a driver/firmware mismatch")
	)
	flag.Parse()

	if *esxiRelease == "" {
		fmt.Println("Error: The -release parameter is mandatory.")
		fmt.Println("Hint: The input should match the 'Product Release Version' on the Compatibility Guide, e.g. 'ESXi 9.1' or 'ESXi 8.0 U3'")
		os.Exit(1)
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
		log.Fatalf("Error connecting to vCenter: %v", err)
	}
	
	if !*jsonOutput {
		fmt.Printf("# Connecting to %s ...\n", client.Client.URL().Host)
		fmt.Println("# Collecting inventory and hardware data...")
	}

	rawInventory, err := collectVSphereData(ctx, client, *dcTarget, *clsTarget, *debugPci, *vsanBeta, excludeCfg)
	if err != nil {
		client.Logout(ctx)
		log.Fatalf("Error discovering inventory: %v", err)
	}
	client.Logout(ctx)

	savedPath, err := saveRawInventory(rawInventory, *vsphereJson)
	if err != nil {
		log.Fatalf("Failed to save raw inventory JSON: %v", err)
	}

	// FIXED: Restored the savedPath print statement to clear the unused variable error
	if !*jsonOutput {
		fmt.Printf("# Raw inventory saved to: %s\n\n", savedPath)
	}

	if *noHCL {
		return
	}

	// ---------------------------------------------------------
	// PHASE 2: HCL Verification (API + vSAN DB)
	// ---------------------------------------------------------
	hclResults := performHCLChecks(rawInventory, *esxiRelease, *detailsOut, *debugPci, *vsanHclFile)

	if *uniqueOut {
		hclResults = aggregateUnique(hclResults)
	}

	// Apply Filters
	hclResults = applyFilters(hclResults, *unsupported, *mismatch)

	// ---------------------------------------------------------
	// PHASE 3: Output Formatting
	// ---------------------------------------------------------
	if *jsonOutput {
		printJSON(hclResults)
	} else {
		printText(hclResults)
	}
}

func applyFilters(data []HostComponents, showUnsupported, showMismatch bool) []HostComponents {
	if !showUnsupported && !showMismatch {
		return data
	}

	var filtered []HostComponents
	for _, host := range data {
		var keepRes []HCLResult
		for _, res := range host.Results {
			if showUnsupported && res.Certified == "FALSE" {
				keepRes = append(keepRes, res)
			} else if showMismatch && res.Certified == "TRUE" && (res.DriverCertified == "FALSE" || res.FirmwareCertified == "FALSE") {
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
