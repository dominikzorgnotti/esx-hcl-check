package main

import "testing"

func TestParseESXiRelease(t *testing.T) {
	cases := []struct {
		in            string
		maj, min, upd int
		ok            bool
	}{
		{"ESXi 9.1", 9, 1, 0, true},
		{"ESXi 8.0 U3", 8, 0, 3, true},
		{"esxi 7.0 u2", 7, 0, 2, true},
		{"ESXi 9.0", 9, 0, 0, true},
		{"garbage", 0, 0, 0, false},
		{"ESXi", 0, 0, 0, false},
	}
	for _, c := range cases {
		maj, min, upd, ok := parseESXiRelease(c.in)
		if ok != c.ok || (ok && (maj != c.maj || min != c.min || upd != c.upd)) {
			t.Errorf("parseESXiRelease(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)",
				c.in, maj, min, upd, ok, c.maj, c.min, c.upd, c.ok)
		}
	}
}

func TestReleaseOrdering(t *testing.T) {
	// each should sort strictly before the next
	ordered := []string{"ESXi 7.0 U2", "ESXi 8.0", "ESXi 8.0 U3", "ESXi 9.0", "ESXi 9.1"}
	for i := 0; i+1 < len(ordered); i++ {
		if !releaseLess(ordered[i], ordered[i+1]) {
			t.Errorf("expected %q < %q", ordered[i], ordered[i+1])
		}
		if releaseLess(ordered[i+1], ordered[i]) {
			t.Errorf("expected NOT %q < %q", ordered[i+1], ordered[i])
		}
	}
}

func TestMaxSupportedRelease(t *testing.T) {
	releases := map[string]interface{}{
		"ESXi 8.0":    nil,
		"ESXi 8.0 U3": nil,
		"ESXi 9.0":    nil,
	}
	if got := maxSupportedRelease(releases); got != "ESXi 9.0" {
		t.Errorf("maxSupportedRelease = %q, want %q", got, "ESXi 9.0")
	}
	if got := maxSupportedRelease(map[string]interface{}{}); got != "" {
		t.Errorf("maxSupportedRelease(empty) = %q, want empty", got)
	}
}
