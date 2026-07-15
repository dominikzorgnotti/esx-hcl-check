package main

import "testing"

// twoNICHostInventory returns one host carrying two identical NICs (same PCI
// identity, so normal aggregation collapses them) plus one storage adapter,
// each with a distinct ESX device name and link state.
func twoNICHostInventory() []RawHostData {
	nic := RawPCIDevice{
		DeviceName: "Acme 10G NIC", DeviceType: "io card (network)",
		VID: 0x14e4, DID: 0x168e, SVID: 0x103c, SSID: 0x1930,
		Firmware: "7.16", DriverVer: "1.4", DriverName: "qfle3",
	}
	nic0, nic1 := nic, nic
	nic0.EsxName, nic0.LinkState = "vmnic0", "UP"
	nic1.EsxName, nic1.LinkState = "vmnic1", "DOWN"

	hba := RawPCIDevice{
		DeviceName: "Acme SAS HBA", DeviceType: "io card (raid)",
		VID: 0x1000, DID: 0x00ac, SVID: 0x1028, SSID: 0x1f4d,
		EsxName: "vmhba1", LinkState: "UP",
	}

	return []RawHostData{{
		Datacenter: "DC1", Cluster: "ClusterA", Hostname: "esx-01",
		SysVendor: "Acme", SysModel: "Server 9000", CpuModel: "Acme CPU",
		PCIDevices: []RawPCIDevice{nic0, nic1, hba},
	}}
}

func findByDevice(results []HCLResult, esxName string) *HCLResult {
	for i := range results {
		if results[i].EsxName == esxName {
			return &results[i]
		}
	}
	return nil
}

// Without -map, the two identical NICs aggregate into a single row with
// number_of_instances = 2 and no ESX name / link state.
func TestPerformHCLChecks_NoMapAggregates(t *testing.T) {
	ws := &warnSink{}
	res := performHCLChecks(twoNICHostInventory(), "ESXi 9.1", false, false, true, false, "does-not-exist.json", nil, ws)
	if len(res) != 1 {
		t.Fatalf("got %d hosts, want 1", len(res))
	}
	var nicRows int
	for _, r := range res[0].Results {
		if r.DeviceType == "io card (network)" {
			nicRows++
			if r.Instances != 2 {
				t.Errorf("aggregated NIC instances = %d, want 2", r.Instances)
			}
			if r.EsxName != "" || r.LinkState != "" {
				t.Errorf("non-map NIC leaked esx/link: %q %q", r.EsxName, r.LinkState)
			}
		}
	}
	if nicRows != 1 {
		t.Fatalf("got %d NIC rows without -map, want 1 (aggregated)", nicRows)
	}
}

// With -map, each adapter becomes its own row carrying its ESX device name and
// link state, and number_of_instances is dropped (Instances 0).
func TestPerformHCLChecks_MapDeaggregates(t *testing.T) {
	ws := &warnSink{}
	res := performHCLChecks(twoNICHostInventory(), "ESXi 9.1", false, false, true, true, "does-not-exist.json", nil, ws)
	if len(res) != 1 {
		t.Fatalf("got %d hosts, want 1", len(res))
	}

	nic0 := findByDevice(res[0].Results, "vmnic0")
	nic1 := findByDevice(res[0].Results, "vmnic1")
	hba := findByDevice(res[0].Results, "vmhba1")
	if nic0 == nil || nic1 == nil || hba == nil {
		t.Fatalf("missing mapped adapters: nic0=%v nic1=%v hba=%v", nic0 != nil, nic1 != nil, hba != nil)
	}

	if nic0.LinkState != "UP" || nic1.LinkState != "DOWN" || hba.LinkState != "UP" {
		t.Errorf("link states = %q/%q/%q, want UP/DOWN/UP", nic0.LinkState, nic1.LinkState, hba.LinkState)
	}
	for _, r := range []*HCLResult{nic0, nic1, hba} {
		if r.Instances != 0 {
			t.Errorf("%s instances = %d, want 0 (dropped under -map)", r.EsxName, r.Instances)
		}
	}
}

// Storage link state is reported only for fabric/target-attached adapters;
// internal RAID/SAS controllers (unknown/unbound) omit it rather than reading
// a misleading DOWN.
func TestHBALinkState(t *testing.T) {
	cases := map[string]string{
		"online":   "UP",
		"Online":   "UP",
		" online ": "UP",
		"offline":  "DOWN",
		"unknown":  "",
		"unbound":  "",
		"":         "",
	}
	for status, want := range cases {
		if got := hbaLinkState(status); got != want {
			t.Errorf("hbaLinkState(%q) = %q, want %q", status, got, want)
		}
	}
}

// CSV -map mode adds the esx_device_name/link_state columns, blanks
// number_of_instances on per-adapter rows, and keeps it on aggregated rows.
func TestCSVRows_MapMode(t *testing.T) {
	data := []HostComponents{{
		Datacenter: "DC1", Cluster: "ClusterA", Hostname: "esx-01",
		Results: []HCLResult{
			{Device: "Acme CPU", DeviceType: "CPU", Instances: 1, Certified: CertTrue},
			{Device: "Acme 10G NIC", DeviceType: "io card (network)", EsxName: "vmnic0", LinkState: "UP", Certified: CertTrue},
		},
	}}

	rows := csvRows(data, true)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (header + 2)", len(rows))
	}
	for i, r := range rows {
		if len(r) != len(csvHeaderMap) {
			t.Fatalf("row %d width %d, want %d", i, len(r), len(csvHeaderMap))
		}
	}

	col := func(name string) int {
		for i, h := range csvHeaderMap {
			if h == name {
				return i
			}
		}
		t.Fatalf("unknown column %q", name)
		return -1
	}

	cpu, nic := rows[1], rows[2]
	if cpu[col("number_of_instances")] != "1" {
		t.Errorf("CPU number_of_instances = %q, want 1", cpu[col("number_of_instances")])
	}
	if cpu[col("esx_device_name")] != "" {
		t.Errorf("CPU esx_device_name = %q, want empty", cpu[col("esx_device_name")])
	}
	if nic[col("number_of_instances")] != "" {
		t.Errorf("mapped NIC number_of_instances = %q, want empty", nic[col("number_of_instances")])
	}
	if nic[col("esx_device_name")] != "vmnic0" || nic[col("link_state")] != "UP" {
		t.Errorf("mapped NIC esx/link = %q/%q, want vmnic0/UP", nic[col("esx_device_name")], nic[col("link_state")])
	}
}
