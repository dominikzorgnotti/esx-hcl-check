package main

import "testing"

func TestAggregateUniqueSource(t *testing.T) {
	t.Run("single vCenter", func(t *testing.T) {
		in := []HostComponents{
			{Source: "vc1.example.com", Hostname: "esx-01", Results: []HCLResult{{Device: "NIC", DeviceType: "io card (network)", Instances: 1}}},
			{Source: "vc1.example.com", Hostname: "esx-02", Results: []HCLResult{{Device: "NIC", DeviceType: "io card (network)", Instances: 1}}},
		}
		got := aggregateUnique(in)
		if len(got) != 1 {
			t.Fatalf("expected 1 aggregated record, got %d", len(got))
		}
		if got[0].Source != "vc1.example.com" {
			t.Errorf("source = %q, want %q", got[0].Source, "vc1.example.com")
		}
	})

	t.Run("multi vCenter is sorted and joined", func(t *testing.T) {
		in := []HostComponents{
			{Source: "vc2.example.com", Hostname: "esx-a"},
			{Source: "vc1.example.com", Hostname: "esx-b"},
			{Source: "vc2.example.com", Hostname: "esx-c"}, // duplicate source collapses
		}
		got := aggregateUnique(in)
		if got[0].Source != "vc1.example.com, vc2.example.com" {
			t.Errorf("source = %q, want %q", got[0].Source, "vc1.example.com, vc2.example.com")
		}
	})
}
