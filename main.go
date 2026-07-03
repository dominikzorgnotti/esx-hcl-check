package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"
)

// Build metadata, set via -ldflags at build time (see the CI/release workflows).
// A plain `go build` leaves the defaults, clearly marking a local dev build.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// versionString formats the build metadata for the -version flag.
func versionString() string {
	return fmt.Sprintf("esx-hcl-check %s (commit %s, built %s)", version, commit, date)
}

// maxWorkers is the hard ceiling for -workers: enough parallelism to speed up
// large environments without overwhelming vCenter or the Broadcom API.
const maxWorkers = 8

// defaultExcludeFile is the -exclude default; a missing file at this path is not
// worth warning about (exclusions are optional), but an explicitly-set missing
// or malformed file is.
const defaultExcludeFile = "exclude.json"

// normalizeWorkers validates the -workers value: it errors below 1 and caps
// above maxWorkers. The returned value is always within [1, maxWorkers].
func normalizeWorkers(n int) (int, error) {
	if n < 1 {
		return 0, fmt.Errorf("-workers must be at least 1 (got %d)", n)
	}
	if n > maxWorkers {
		return maxWorkers, nil
	}
	return n, nil
}

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
		excludeFile = flag.String("exclude", defaultExcludeFile, "Path to the exclude JSON file to filter out specific devices")
		vsanHclFile = flag.String("vsanhcl", "vsan-offline-hcl.json", "Path to the local vSAN HCL offline JSON database")
		unsupported = flag.Bool("unsupported", false, "Filter output to ONLY show hardware that is not certified")
		mismatch    = flag.Bool("mismatch", false, "Filter output to ONLY show hardware that is certified but has a driver/firmware mismatch")
		quiet       = flag.Bool("quiet", false, "Suppress warnings about missing firmware/driver information")
		workers     = flag.Int("workers", 4, "How many hosts to collect from at once (1-8). 1 = sequential, one host at a time; higher = that many in parallel")
		statsFlag   = flag.Bool("stats", false, "Emit run statistics (inventory counts and query timings) as a 'stats' block/key")
		offline     = flag.Bool("offline", false, "Run without internet: skip all Broadcom Compatibility Guide API checks (marked SKIPPED) and use only the local vSAN HCL database")
		csvOut      = flag.Bool("csv", false, "Output results as CSV (one row per device) to stdout, with the same detail as -json")
		showVersion = flag.Bool("version", false, "Print version information and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(versionString())
		return
	}

	if *esxiRelease == "" {
		fmt.Fprintln(os.Stderr, "Error: The -release parameter is mandatory.")
		fmt.Fprintln(os.Stderr, "Hint: The input should match the 'Product Release Version' on the Compatibility Guide, e.g. 'ESXi 9.1' or 'ESXi 8.0 U3'")
		os.Exit(2)
	}

	// -details implies detailed output; it selects JSON unless CSV was requested
	// (so -csv -details produces a detailed CSV rather than switching to JSON).
	if *detailsOut && !*csvOut {
		*jsonOutput = true
	}

	// warnings are routed by output mode: to stderr in text/CSV mode (visible
	// during the run, and kept off the CSV/text stdout), or into the JSON
	// payload's "warnings" key in JSON mode (so stdout stays a single valid JSON
	// document for CI/CD). CSV takes precedence over JSON for output.
	ws := &warnSink{json: *jsonOutput && !*csvOut}

	if govcInsecure() {
		ws.add("GOVC_INSECURE is set — TLS certificate verification is DISABLED. The vCenter connection is vulnerable to man-in-the-middle interception; use it only for trusted, self-signed lab environments.")
		if os.Getenv("GOVC_TLS_CA_CERTS") != "" || os.Getenv("GOVC_TLS_KNOWN_HOSTS") != "" {
			ws.add("GOVC_INSECURE overrides GOVC_TLS_CA_CERTS / GOVC_TLS_KNOWN_HOSTS — with certificate verification disabled, the custom CA bundle and known-hosts thumbprints are NOT enforced. Unset GOVC_INSECURE to have them take effect.")
		}
	}

	// Soft-validate -release: warn (don't reject) if it doesn't look like a
	// Broadcom release version, so a typo doesn't silently return all-uncertified.
	if _, _, _, ok := parseESXiRelease(*esxiRelease); !ok {
		ws.add(fmt.Sprintf("-release=%q doesn't look like a Broadcom release version (expected e.g. \"ESXi 9.1\" or \"ESXi 8.0 U3\"); results may be empty or all-uncertified.", *esxiRelease))
	}

	// Validate -workers: reject nonsensical values and enforce the hard maximum.
	if v, err := normalizeWorkers(*workers); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v.\n", err)
		os.Exit(2)
	} else {
		if v != *workers {
			ws.add(fmt.Sprintf("-workers capped at the maximum of %d (requested %d).", maxWorkers, *workers))
		}
		*workers = v
	}

	// Load Exclude Rules — surface problems instead of silently ignoring them,
	// so a user whose exclusions "don't work" learns why.
	var excludeCfg ExcludeConfig
	if *excludeFile != "" {
		b, err := os.ReadFile(*excludeFile)
		switch {
		case err != nil:
			// A missing default file is expected (exclusions are optional); only
			// warn if the user explicitly pointed at a file we couldn't read.
			if *excludeFile != defaultExcludeFile {
				ws.add(fmt.Sprintf("could not read exclude file %q: %v (no exclusions applied)", *excludeFile, err))
			}
		default:
			if err := json.Unmarshal(b, &excludeCfg); err != nil {
				ws.add(fmt.Sprintf("exclude file %q is not valid JSON: %v (no exclusions applied)", *excludeFile, err))
			} else {
				for _, expr := range excludeCfg.RegexNames {
					if re, err := regexp.Compile(expr); err != nil {
						ws.add(fmt.Sprintf("invalid regex %q in exclude file %q: %v (this rule is ignored)", expr, *excludeFile, err))
					} else {
						excludeCfg.CompiledRegexes = append(excludeCfg.CompiledRegexes, re)
					}
				}
			}
		}
	}

	// Connectivity handling — only relevant when the HCL phase will run.
	if !*noHCL {
		if *offline {
			ws.add("-offline mode: Broadcom Compatibility Guide checks are skipped; affected components are marked SKIPPED.")
			ws.add(fmt.Sprintf("Verification uses only the local vSAN HCL database at %q (override with -vsanhcl).", *vsanHclFile))
			ws.add(fmt.Sprintf("If the database is missing, download it from %s and save it to that path.", vsanHCLURL))
		} else if failures := checkConnectivity(5 * time.Second); len(failures) > 0 {
			fmt.Fprintln(os.Stderr, "Error: cannot reach the online HCL sources required for verification:")
			for _, f := range failures {
				fmt.Fprintf(os.Stderr, "  - %s\n", f)
			}
			fmt.Fprintln(os.Stderr, "")
			fmt.Fprintln(os.Stderr, "This usually means no internet access, a firewall, or a required proxy. Try one of:")
			fmt.Fprintln(os.Stderr, "  * Verify this host has internet access to broadcom.com")
			fmt.Fprintln(os.Stderr, "  * Configure a proxy via the HTTPS_PROXY / HTTP_PROXY / NO_PROXY environment variables")
			fmt.Fprintln(os.Stderr, "    (a TLS-intercepting proxy may instead surface as a certificate error)")
			fmt.Fprintln(os.Stderr, "  * Re-run with -offline to skip the Broadcom API and use only the local vSAN HCL database")
			fmt.Fprintf(os.Stderr, "    (download it from %s and save it to %q, or pass -vsanhcl <path>)\n", vsanHCLURL, *vsanHclFile)
			os.Exit(2)
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

	collectStart := time.Now()
	rawInventory, err := collectVSphereData(ctx, client, *dcTarget, *clsTarget, *debugPci, *vsanBeta, excludeCfg, *workers)
	if err != nil {
		_ = client.Logout(ctx)
		fmt.Fprintf(os.Stderr, "Error discovering inventory: %v\n", err)
		os.Exit(2)
	}
	vcenterQueryMs := time.Since(collectStart).Milliseconds()
	_ = client.Logout(ctx) // best-effort session cleanup

	savedPath, err := saveRawInventory(rawInventory, *vsphereJson)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to save raw inventory JSON: %v\n", err)
		os.Exit(2)
	}

	if !*jsonOutput {
		fmt.Fprintf(os.Stderr, "# Raw inventory saved to: %s\n\n", savedPath)
	}

	// Assemble run statistics: inventory counts now, query timings as we go.
	stats := computeInventoryStats(rawInventory)
	stats.VCenterQueryMs = vcenterQueryMs
	var statsOut *Stats
	if *statsFlag {
		statsOut = &stats
	}

	if *noHCL {
		// No HCL phase; nothing to certify. Emit an empty CSV (header only) or
		// surface stats (inventory + vCenter timing) if asked.
		switch {
		case *csvOut:
			printCSV(nil)
		case statsOut != nil && *jsonOutput:
			printJSON([]HostComponents{}, ws.messages, statsOut, *quiet)
		case statsOut != nil:
			printStatsText(statsOut)
		}
		return
	}

	// ---------------------------------------------------------
	// PHASE 2: HCL Verification (API + vSAN DB)
	// ---------------------------------------------------------
	hclResults := performHCLChecks(rawInventory, *esxiRelease, *detailsOut, *debugPci, *offline, *vsanHclFile, &stats, ws)

	// Compute the exit code from the complete result set, before -unique/filters
	// drop entries, so the process status always reflects the true findings.
	exitCode := computeExitCode(hclResults)

	if *uniqueOut {
		hclResults = aggregateUnique(hclResults)
	}

	// Apply Filters
	hclResults = applyFilters(hclResults, *unsupported, *mismatch)

	// ---------------------------------------------------------
	// PHASE 3: Output Formatting (CSV > JSON > text)
	// ---------------------------------------------------------
	switch {
	case *csvOut:
		printCSV(hclResults)
	case *jsonOutput:
		printJSON(hclResults, ws.messages, statsOut, *quiet)
	default:
		printText(hclResults, statsOut, *quiet)
	}

	os.Exit(exitCode)
}

// computeExitCode maps scan findings to a process exit code for CI/CD gating:
//
//	0 — every component is certified (or not applicable)
//	1 — at least one component is definitively NOT certified
//	2 — the scan could not be fully determined: a host/cluster was skipped, an
//	    HCL lookup failed (CertError), or a check was skipped (CertSkipped, e.g.
//	    -offline). 2 takes precedence over 1, because an incomplete answer should
//	    not read as a clean "only known-bad found".
//
// Fatal run errors (connect/collect/save) also exit 2, handled at their sites.
func computeExitCode(data []HostComponents) int {
	foundUncertified := false
	for _, host := range data {
		if host.SkipReason != "" {
			return 2
		}
		for _, res := range host.Results {
			if res.Certified == CertError || res.Certified == CertSkipped {
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
