package main

import (
	"net/url"
	"strings"
	"testing"
)

// TestBuildGpuQueryURL pins the GPU link to the vSphere third-party GPU guide
// (program=sptg) with a model keyword and release filter — not the I/O guide.
func TestBuildGpuQueryURL(t *testing.T) {
	link := buildGpuQueryURL("NVIDIA A40", "ESXi 9.1")

	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("buildGpuQueryURL produced an unparseable URL %q: %v", link, err)
	}
	q := u.Query()

	if got := q.Get("program"); got != "sptg" {
		t.Errorf("program = %q, want \"sptg\" (GPU guide, not the I/O guide)", got)
	}
	if got := q.Get("keyword"); got != "NVIDIA A40" {
		t.Errorf("keyword = %q, want \"NVIDIA A40\"", got)
	}
	if got := q.Get("productReleaseVersion"); got != "[ESXi 9.1]" {
		t.Errorf("productReleaseVersion = %q, want \"[ESXi 9.1]\"", got)
	}
	if got := q.Get("persona"); got != "live" {
		t.Errorf("persona = %q, want \"live\"", got)
	}
	// The GPU guide ignores PCI-ID filters, so the link must not smuggle them in.
	for _, k := range []string{"vid", "did", "svid", "ssid"} {
		if q.Has(k) {
			t.Errorf("GPU link unexpectedly carries PCI-ID param %q", k)
		}
	}
}

// TestGpuHCLVerdict covers the VID:DID match: certified for the target release,
// present but not for that release, and absent — plus hex case-insensitivity.
func TestGpuHCLVerdict(t *testing.T) {
	g := gpuHCL{
		"10de:2235": {"ESXi 9.1", "ESXi 8.0 U3"}, // A40
		"10de:27b8": {"ESXi 9.1"},                // L4
	}
	cases := []struct {
		name          string
		vid, did, rel string
		want          CertStatus
	}{
		{"A40 certified for 9.1", "10de", "2235", "ESXi 9.1", CertTrue},
		{"A40 not listed for 7.0", "10de", "2235", "ESXi 7.0", CertFalse},
		{"L4 certified for 9.1", "10de", "27b8", "ESXi 9.1", CertTrue},
		{"L4 not listed for 8.0 U3", "10de", "27b8", "ESXi 8.0 U3", CertFalse},
		{"iLO5 VGA absent from guide", "102b", "0538", "ESXi 9.1", CertFalse},
		{"uppercase hex still matches", "10DE", "2235", "ESXi 9.1", CertTrue},
	}
	for _, tc := range cases {
		if got := g.verdict(tc.vid, tc.did, tc.rel); got != tc.want {
			t.Errorf("%s: verdict(%s,%s,%q) = %v, want %v", tc.name, tc.vid, tc.did, tc.rel, got, tc.want)
		}
	}
}

// TestParseGpuHCL parses a faithful sptg response: two rows with VID/DID and one
// SXM-style row without, which must be skipped (but still counted as a page row).
func TestParseGpuHCL(t *testing.T) {
	body := []byte(`{"data":{"count":3,"fieldValues":[
		{"deviceModel":[{"name":"NVIDIA A40"}],"esxVersion":["ESXi 9.1","ESXi 8.0 U3"],
		 "hoverData":[{"displayName":"VID","value":"10de"},{"displayName":"DID","value":"2235"},{"displayName":"SVID","value":"10de"},{"displayName":"SSID","value":"145a"}]},
		{"deviceModel":[{"name":"NVIDIA H100 NVL"}],"esxVersion":["ESXi 9.1"],
		 "hoverData":[{"displayName":"VID","value":"10de"},{"displayName":"DID","value":"2321"}]},
		{"deviceModel":[{"name":"NVIDIA H100 SXM5 80GB"}],"esxVersion":["ESXi 9.1"],
		 "hoverData":[{"displayName":"FormFactor","value":"SXM5"}]}
	]}}`)

	g, count, rows, err := parseGpuHCL(body)
	if err != nil {
		t.Fatalf("parseGpuHCL error: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	if rows != 3 {
		t.Errorf("rows = %d, want 3 (all page rows, incl. the VID/DID-less one)", rows)
	}
	if len(g) != 2 {
		t.Errorf("indexed entries = %d, want 2 (SXM row without VID/DID skipped)", len(g))
	}
	// The H100 NVL is the case the keyword approach got wrong; VID:DID nails it.
	if got := g.verdict("10de", "2321", "ESXi 9.1"); got != CertTrue {
		t.Errorf("H100 NVL verdict = %v, want CertTrue", got)
	}
	if got := g.verdict("10de", "2235", "ESXi 8.0 U3"); got != CertTrue {
		t.Errorf("A40 verdict = %v, want CertTrue", got)
	}
}

// TestParseGpuHCLMalformed ensures a non-JSON body is a real error (undetermined),
// not a silently-empty index that would falsely fail every GPU.
func TestParseGpuHCLMalformed(t *testing.T) {
	if _, _, _, err := parseGpuHCL([]byte("<html>gateway timeout</html>")); err == nil {
		t.Fatal("parseGpuHCL on non-JSON body returned nil error, want an error")
	}
}

// TestPerformHCLChecksGPUUsesSptgGuide is the regression test for #49: a GPU must
// be routed to the sptg GPU guide, while a real I/O card still goes to the io
// guide. Runs offline so no network is touched; the routing is asserted via the
// generated HCL link, which is set regardless of the offline verdict.
func TestPerformHCLChecksGPUUsesSptgGuide(t *testing.T) {
	inventory := []RawHostData{{
		Datacenter: "DC",
		Cluster:    "C",
		Hostname:   "host1",
		SysVendor:  "Dell Inc.",
		SysModel:   "PowerEdge R750",
		CpuModel:   "Intel Xeon",
		PCIDevices: []RawPCIDevice{
			{
				DeviceName: "NVIDIA A40",
				DeviceType: "GPU",
				VID:        0x10de, DID: 0x2235, SVID: 0x10de, SSID: 0x145a,
			},
			{
				DeviceName: "Emulex LPe35000 Fibre Channel Adapter",
				DeviceType: "io card (fc)",
				VID:        0x10df, DID: 0x0e00, SVID: 0x10df, SSID: 0x0e04,
				Firmware:  "14.0",
				DriverVer: "14.0.0.0",
			},
		},
	}}

	// Offline + a path that does not exist -> no vSAN DB, no network.
	ws := &warnSink{json: true}
	results := performHCLChecks(inventory, "ESXi 9.1", true, false, true, false, "does-not-exist.json", nil, ws)

	if len(results) != 1 {
		t.Fatalf("got %d host results, want 1", len(results))
	}

	var gpu, fc *HCLResult
	for i := range results[0].Results {
		switch results[0].Results[i].DeviceType {
		case "GPU":
			gpu = &results[0].Results[i]
		case "io card (fc)":
			fc = &results[0].Results[i]
		}
	}
	if gpu == nil {
		t.Fatal("no GPU result found")
	}
	if fc == nil {
		t.Fatal("no FC I/O card result found")
	}

	if !strings.Contains(gpu.HCLLink, "program=sptg") {
		t.Errorf("GPU HCL link = %q, want it to target program=sptg", gpu.HCLLink)
	}
	if strings.Contains(gpu.HCLLink, "program=io") {
		t.Errorf("GPU HCL link = %q must not target the I/O guide (program=io)", gpu.HCLLink)
	}
	if !strings.Contains(fc.HCLLink, "program=io") {
		t.Errorf("FC I/O card HCL link = %q, want it to target program=io", fc.HCLLink)
	}

	// Offline: the GPU (API-only) is SKIPPED, and driver/firmware stay N/A.
	if gpu.Certified != CertSkipped {
		t.Errorf("offline GPU verdict = %v, want CertSkipped", gpu.Certified)
	}
	if gpu.DriverCertified != CertNA || gpu.FirmwareCertified != CertNA {
		t.Errorf("GPU driver/firmware = %v/%v, want N/A/N/A", gpu.DriverCertified, gpu.FirmwareCertified)
	}
}
