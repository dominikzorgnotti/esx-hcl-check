package main

import (
	"encoding/json"
	"testing"
)

func TestCertStatusStringAndMarshal(t *testing.T) {
	cases := []struct {
		status CertStatus
		want   string
	}{
		{CertTrue, "TRUE"},
		{CertFalse, "FALSE"},
		{CertNA, "N/A"},
		{CertError, "ERROR"},
		{CertStatus(999), "N/A"}, // out-of-range value falls back to N/A
	}
	for _, c := range cases {
		if got := c.status.String(); got != c.want {
			t.Errorf("String(%d) = %q, want %q", c.status, got, c.want)
		}
		b, err := json.Marshal(c.status)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", c.status, err)
		}
		if string(b) != `"`+c.want+`"` {
			t.Errorf("Marshal(%v) = %s, want %q", c.status, b, `"`+c.want+`"`)
		}
	}
}

func TestCertStatusUnmarshal(t *testing.T) {
	cases := map[string]CertStatus{
		`"TRUE"`:  CertTrue,
		`"FALSE"`: CertFalse,
		`"N/A"`:   CertNA,
		`"ERROR"`: CertError,
		`"true"`:  CertTrue,  // case-insensitive
		`"weird"`: CertNA,    // unknown token -> N/A
		`""`:      CertNA,    // empty -> N/A
		`null`:    CertNA,    // null -> N/A
	}
	for in, want := range cases {
		var got CertStatus
		if err := json.Unmarshal([]byte(in), &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", in, err)
		}
		if got != want {
			t.Errorf("Unmarshal(%s) = %v, want %v", in, got, want)
		}
	}
}

// TestHCLResultWireFormatPreserved locks the whole point of CertStatus: the
// -json output must keep emitting the historical "TRUE"/"N/A"/"ERROR" strings
// under the existing keys, so switching from string to a typed enum is not a
// wire-visible change for downstream consumers.
func TestHCLResultWireFormatPreserved(t *testing.T) {
	r := HCLResult{Certified: CertTrue, DriverCertified: CertNA, FirmwareCertified: CertError}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"hw_certified":       "TRUE",
		"driver_certified":   "N/A",
		"firmware_certified": "ERROR",
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("field %s = %v, want %q", k, m[k], v)
		}
	}
}
