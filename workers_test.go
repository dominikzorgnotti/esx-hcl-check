package main

import "testing"

func TestNormalizeWorkers(t *testing.T) {
	cases := []struct {
		in      int
		want    int
		wantErr bool
	}{
		{-1, 0, true}, // negative rejected (the bug that slipped through before)
		{0, 0, true},  // zero rejected
		{1, 1, false}, // minimum valid
		{4, 4, false}, // default
		{8, 8, false}, // hard maximum
		{9, 8, false}, // above max -> capped
		{100, 8, false},
	}
	for _, c := range cases {
		got, err := normalizeWorkers(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("normalizeWorkers(%d): expected error, got %d", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("normalizeWorkers(%d): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("normalizeWorkers(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
