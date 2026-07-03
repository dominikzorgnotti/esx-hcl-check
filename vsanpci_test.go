package main

import "testing"

// TestEvaluateVsanPCIExcludesRdmaNic locks the fix for the E810-C false negative:
// a NIC present only in the vSAN RDMA NIC category must NOT match (so it falls
// through to the live I/O device API), while a controller with the same-style
// entry still matches.
func TestEvaluateVsanPCIExcludesRdmaNic(t *testing.T) {
	db := &VsanOfflineDB{}
	// An RDMA NIC entry, as it appears in data.nic.
	db.Data.Nic = []map[string]interface{}{{
		"vid": "8086", "did": "1593", "svid": "8086", "ssid": "0005",
		"vcglink":  "https://compatibilityguide.broadcom.com/detail?program=rdmanic&productId=50085&persona=live",
		"releases": map[string]interface{}{"ESXi 7.0 U3": nil, "ESXi 7.0 U2": nil},
	}}
	// A controller entry that should still match.
	db.Data.Controller = []map[string]interface{}{{
		"vid": "1000", "did": "00d1", "svid": "1000", "ssid": "0001",
		"vcglink":  "https://compatibilityguide.broadcom.com/detail?program=vsanio&productId=1&persona=live",
		"releases": map[string]interface{}{"ESXi 9.1": nil},
	}}

	// The NIC's IDs must NOT be found (RDMA NIC category excluded).
	var nicRes HCLResult
	if evaluateVsanPCI(db, "8086", "1593", "8086", "0005", "ESXi 9.1", &nicRes) {
		t.Errorf("NIC unexpectedly matched the vSAN RDMA NIC category; expected fall-through to the live API")
	}
	if nicRes.MaxSupportedRelease != "" {
		t.Errorf("NIC max_supported_release should be empty (not from RDMA HCL), got %q", nicRes.MaxSupportedRelease)
	}

	// The controller must still match.
	var ctrlRes HCLResult
	if !evaluateVsanPCI(db, "1000", "00d1", "1000", "0001", "ESXi 9.1", &ctrlRes) {
		t.Errorf("controller should still match in the vSAN DB")
	}
}
