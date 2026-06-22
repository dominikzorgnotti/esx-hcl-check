package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	var (
		jsonOutput  = flag.Bool("json", false, "Output final HCL results in JSON format")
		dcTarget    = flag.String("dc", os.Getenv("GOVC_DATACENTER"), "Target datacenter (optional)")
		clsTarget   = flag.String("cluster", os.Getenv("GOVC_CLUSTER"), "Target cluster (optional)")
		esxiRelease = flag.String("release", "ESXi 9.1", "Target ESXi version for compatibility validation")
		vsphereJson = flag.String("vspherejson", "", "Path to save the raw vSphere hardware JSON (defaults to OS temp dir)")
		noHCL       = flag.Bool("nohcl", false, "Skip the HCL check phase and only collect vSphere data")
		detailsOut  = flag.Bool("details", false, "Include unique IDs (VID, DID, SSID, CPUID) in the JSON output")
		debugPci    = flag.Bool("debugpci", false, "Bypass PCI filters and dump all raw PCI devices into the JSON file for troubleshooting")
	)
	flag.Parse()

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

	rawInventory, err := collectVSphereData(ctx, client, *dcTarget, *clsTarget, *debugPci)
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

	// ---------------------------------------------------------
	// PHASE 3: Output Formatting
	// ---------------------------------------------------------
	if *jsonOutput {
		printJSON(hclResults)
	} else {
		printText(hclResults)
	}
}
