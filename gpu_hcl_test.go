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
	results := performHCLChecks(inventory, "ESXi 9.1", true, false, true, "does-not-exist.json", nil, ws)

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
