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
		vsanBeta    = flag.Bool("vsan", false, "BETA: Extract vSAN SSD disks (Work in progress, results may not be reliable)")
		uniqueOut   = flag.Bool("unique", false, "Aggregate and deduplicate output across all hosts globally")
		excludeFile = flag.String("exclude", "exclude.json", "Path to the exclude JSON file to filter out specific devices")
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

	// Load and parse the exclude rules
	var excludeCfg ExcludeConfig
	if *excludeFile != "" {
		if b, err := os.ReadFile(*excludeFile); err == nil {
			if err := json.Unmarshal(b, &excludeCfg); err != nil {
				log.Printf("Warning: Failed to parse exclude file %s: %v\n", *excludeFile, err)
			} else {
				if !*jsonOutput {
					fmt.Printf("# Loaded exclude rules from %s\n", *excludeFile)
				}
				// Pre-compile the regex statements for efficiency
				for _, expr := range excludeCfg.RegexNames {
					if re, err := regexp.Compile(expr); err == nil {
						excludeCfg.CompiledRegexes = append(excludeCfg.CompiledRegexes, re)
					} else {
						log.Printf("Warning: Invalid regex '%s' in exclude file: %v\n", expr, err)
					}
				}
			}
		} else if *excludeFile != "exclude.json" {
			// Only throw a warning if the user explicitly provided a filename that wasn't found
			log.Printf("Warning: Could not read exclude file %s: %v\n", *excludeFile, err)
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
		if *vsanBeta {
			fmt.Println("⚠️  NOTE: vSAN disk extraction (-vsan) is a BETA feature in work and progress. Results will not be reliable.")
		}
		fmt.Println("ℹ️  NOTE: The text output displays a minimized view. Run with -json to see full details (firmware, drivers, etc.).")
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

	if !*jsonOutput {
		fmt.Printf("# Raw inventory saved to: %s\n\n", savedPath)
	}

	if *noHCL {
		if !*jsonOutput {
			fmt.Println("Skipping HCL validation due to -nohcl flag. Exiting.")
		}
		return
	}

	// ---------------------------------------------------------
	// PHASE 2: HCL Verification
	// ---------------------------------------------------------
	hclResults := performHCLChecks(rawInventory, *esxiRelease, *detailsOut, *debugPci)

	if *uniqueOut {
		hclResults = aggregateUnique(hclResults)
	}

	// ---------------------------------------------------------
	// PHASE 3: Output Formatting
	// ---------------------------------------------------------
	if *jsonOutput {
		printJSON(hclResults)
	} else {
		printText(hclResults)
	}
}
