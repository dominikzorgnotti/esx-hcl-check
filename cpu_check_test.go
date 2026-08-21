package main

import (
	"bytes"
	"io"
	"net/http"
	"testing"
)

// findResultByType returns the first result of the given device type.
func findResultByType(results []HCLResult, deviceType string) *HCLResult {
	for i := range results {
		if results[i].DeviceType == deviceType {
			return &results[i]
		}
	}
	return nil
}

// stubRoundTripper answers every Broadcom call with an empty result set, so a
// test can exercise an online code path without touching the network.
type stubRoundTripper struct{ calls int }

func (s *stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	s.calls++
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":{"count":0}}`))),
		Header:     make(http.Header),
	}, nil
}

// stubBroadcom redirects the shared Broadcom HTTP client at a stub for the
// duration of a test and reports how many calls were made.
func stubBroadcom(t *testing.T) *stubRoundTripper {
	t.Helper()
	rt := &stubRoundTripper{}
	orig := broadcomHTTPClient
	broadcomHTTPClient = &http.Client{Transport: rt}
	t.Cleanup(func() { broadcomHTTPClient = orig })
	return rt
}

// A host whose CPUID could not be extracted must never be reported as "not
// certified": the Broadcom CPU guide is indexed by CPUID, so a model-name
// lookup returns nothing even for a supported CPU (measured across Intel
// Skylake-SP/Cascade Lake/Sapphire Rapids and AMD EPYC). The check is
// undetermined (SKIPPED) and the reason is surfaced in Issues.
func TestCPUWithoutCPUIDIsSkippedNotFalse(t *testing.T) {
	inventory := []RawHostData{{
		Datacenter: "DC1", Cluster: "ClusterA", Hostname: "esx-01",
		SysVendor: "HPE", SysModel: "ProLiant DL380 Gen10",
		CpuModel: "Intel(R) Xeon(R) Gold 6136 CPU @ 3.00GHz",
		CpuId:    "", // extraction failed / not available (e.g. an imported inventory)
	}}

	stubBroadcom(t) // the system check runs online; keep it off the network
	ws := &warnSink{}
	results := performHCLChecks(inventory, "ESXi 9.1", false, false, false, false, "does-not-exist.json", nil, ws)
	if len(results) != 1 {
		t.Fatalf("got %d hosts, want 1", len(results))
	}

	cpu := findResultByType(results[0].Results, "CPU")
	if cpu == nil {
		t.Fatal("no CPU result produced")
	}
	if cpu.Certified == CertFalse {
		t.Error("CPU without a CPUID reported FALSE — a certified CPU would be a false negative")
	}
	if cpu.Certified != CertSkipped {
		t.Errorf("CPU certified = %v, want SKIPPED", cpu.Certified)
	}

	// The operator must be told why the check could not run.
	var found bool
	for _, iss := range results[0].Issues {
		for _, m := range iss.Missing {
			if m == "cpu_id" {
				found = true
				if iss.Reason == "" {
					t.Error("cpu_id issue carries no reason")
				}
			}
		}
	}
	if !found {
		t.Error("no Issues entry explaining the missing CPUID")
	}
}

// With a CPUID present the CPU is still queried live (unchanged behaviour):
// the guide is reachable by CPUID, so the check must actually run.
func TestCPUWithCPUIDIsQueried(t *testing.T) {
	inventory := []RawHostData{{
		Hostname: "esx-01",
		CpuModel: "Intel(R) Xeon(R) Gold 6136 CPU @ 3.00GHz",
		CpuId:    "0x00050654",
	}}

	rt := stubBroadcom(t)
	ws := &warnSink{}
	results := performHCLChecks(inventory, "ESXi 9.1", false, false, false, false, "does-not-exist.json", nil, ws)

	cpu := findResultByType(results[0].Results, "CPU")
	if cpu == nil {
		t.Fatal("no CPU result produced")
	}
	if cpu.Certified == CertSkipped {
		t.Error("CPU with a CPUID was skipped; it should have been queried")
	}
	// system + cpu = at least two lookups actually issued
	if rt.calls < 2 {
		t.Errorf("made %d Broadcom calls, want >= 2 (system + cpu)", rt.calls)
	}
}

// SKIPPED must drive exit code 2 (undetermined), not 0 (clean) or 1 (uncertified).
func TestCPUWithoutCPUIDDrivesExitCode2(t *testing.T) {
	data := []HostComponents{{
		Hostname: "esx-01",
		Results: []HCLResult{
			{Device: "Some Server", DeviceType: "system", Certified: CertTrue},
			{Device: "Some CPU", DeviceType: "CPU", Certified: CertSkipped},
		},
	}}
	if got := computeExitCode(data); got != 2 {
		t.Errorf("exit code = %d, want 2 (undetermined)", got)
	}
}
