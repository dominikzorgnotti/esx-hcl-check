package main

import "testing"

func TestComputeExitCode(t *testing.T) {
	cases := []struct {
		name string
		data []HostComponents
		want int
	}{
		{
			name: "all certified -> 0",
			data: []HostComponents{{Results: []HCLResult{{Certified: CertTrue}, {Certified: CertNA}}}},
			want: 0,
		},
		{
			name: "empty -> 0",
			data: nil,
			want: 0,
		},
		{
			name: "one uncertified -> 1",
			data: []HostComponents{{Results: []HCLResult{{Certified: CertTrue}, {Certified: CertFalse}}}},
			want: 1,
		},
		{
			name: "lookup error takes precedence over uncertified -> 2",
			data: []HostComponents{{Results: []HCLResult{{Certified: CertFalse}, {Certified: CertError}}}},
			want: 2,
		},
		{
			name: "skipped check (offline) -> 2",
			data: []HostComponents{{Results: []HCLResult{{Certified: CertTrue}, {Certified: CertSkipped}}}},
			want: 2,
		},
		{
			name: "skipped host -> 2",
			data: []HostComponents{{SkipReason: "host not connected (state: disconnected)"}},
			want: 2,
		},
		{
			name: "skipped host outweighs a clean host -> 2",
			data: []HostComponents{
				{Results: []HCLResult{{Certified: CertTrue}}},
				{SkipReason: "could not enumerate hosts: permission denied"},
			},
			want: 2,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := computeExitCode(c.data); got != c.want {
				t.Errorf("computeExitCode() = %d, want %d", got, c.want)
			}
		})
	}
}
