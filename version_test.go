package main

import (
	"strings"
	"testing"
)

func TestVersionString(t *testing.T) {
	got := versionString()
	if !strings.HasPrefix(got, "esx-hcl-check ") {
		t.Errorf("versionString() = %q, want it to start with the program name", got)
	}
	// The current version value (default "dev" locally, or the ldflags value) must appear.
	if !strings.Contains(got, version) {
		t.Errorf("versionString() = %q, want it to contain version %q", got, version)
	}
	for _, part := range []string{commit, date} {
		if !strings.Contains(got, part) {
			t.Errorf("versionString() = %q, want it to contain %q", got, part)
		}
	}
}
