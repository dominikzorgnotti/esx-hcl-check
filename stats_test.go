package main

import (
	"encoding/json"
	"testing"
)

// TestJSONOutputStatsPlacement locks the requirement that -stats adds a
// top-level "stats" key next to "results" (and "issues"), and that it is absent
// when stats is nil.
func TestJSONOutputStatsPlacement(t *testing.T) {
	withStats := buildJSONOutput([]HostComponents{{Hostname: "esx-01"}}, []string{"-offline mode active"}, &Stats{Hosts: 1}, true)
	b, err := json.Marshal(withStats)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["results"]; !ok {
		t.Error("expected top-level 'results' key")
	}
	if _, ok := m["warnings"]; !ok {
		t.Error("expected top-level 'warnings' key when warnings provided")
	}
	if _, ok := m["stats"]; !ok {
		t.Error("expected top-level 'stats' key when stats provided")
	}

	without := buildJSONOutput([]HostComponents{{Hostname: "esx-01"}}, nil, nil, true)
	b2, _ := json.Marshal(without)
	var m2 map[string]json.RawMessage
	json.Unmarshal(b2, &m2)
	if _, ok := m2["stats"]; ok {
		t.Error("did not expect 'stats' key when stats is nil")
	}
	if _, ok := m2["warnings"]; ok {
		t.Error("did not expect 'warnings' key when warnings is empty")
	}
}

func TestComputeInventoryStats(t *testing.T) {
	inv := []RawHostData{
		{
			Datacenter: "DC1", Cluster: "ClusterA",
			PCIDevices: []RawPCIDevice{
				{DeviceType: "io card (network)"},
				{DeviceType: "io card (raid)"},
				{DeviceType: "GPU"}, // not an io card
			},
			Disks: []RawDiskDevice{{DeviceType: "vSAN SSD"}, {DeviceType: "vSAN NVMe PCIe"}},
		},
		{
			Datacenter: "DC1", Cluster: "ClusterA",
			PCIDevices: []RawPCIDevice{{DeviceType: "io card (fc)"}},
		},
		{
			Datacenter: "DC1", Cluster: "ClusterB",
			SkipReason: "host not connected (state: disconnected)",
		},
	}

	s := computeInventoryStats(inv)

	if s.Datacenters != 1 {
		t.Errorf("Datacenters = %d, want 1", s.Datacenters)
	}
	if s.Clusters != 2 { // ClusterA + ClusterB (skipped cluster still counts)
		t.Errorf("Clusters = %d, want 2", s.Clusters)
	}
	if s.Hosts != 2 {
		t.Errorf("Hosts = %d, want 2", s.Hosts)
	}
	if s.HostsSkipped != 1 {
		t.Errorf("HostsSkipped = %d, want 1", s.HostsSkipped)
	}
	if s.IOCards != 3 { // host1: network + raid (GPU excluded); host2: fc
		t.Errorf("IOCards = %d, want 3", s.IOCards)
	}
	if s.StorageDevices != 2 {
		t.Errorf("StorageDevices = %d, want 2", s.StorageDevices)
	}
}
