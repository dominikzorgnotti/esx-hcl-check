package main

import "testing"

func TestCSVRows(t *testing.T) {
	data := []HostComponents{
		{
			Source: "vc1", Datacenter: "DC1", Cluster: "ClusterA", Hostname: "esx-01",
			Results: []HCLResult{
				{
					Device: "Some NIC", DeviceType: "io card (network)", Instances: 2,
					Firmware: "1.0", DriverVer: "2.0", DriverName: "nenic",
					Certified: CertTrue, DriverCertified: CertFalse, FirmwareCertified: CertNA,
					MaxSupportedRelease: "ESXi 9.0",
					SupportedDrivers:    []string{"nenic 2.1", "nenic 2.2"},
					VID:                 "8086", HCLLink: "https://example/hcl",
				},
			},
		},
		{
			Source: "vc1", Datacenter: "DC1", Cluster: "ClusterB", Hostname: "",
			SkipReason: "could not enumerate hosts: permission denied",
		},
	}

	rows := csvRows(data)

	// header + 1 device row + 1 skip row
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if len(rows[0]) != len(csvHeader) {
		t.Fatalf("header width %d, want %d", len(rows[0]), len(csvHeader))
	}
	// every row must have the same column count as the header
	for i, r := range rows {
		if len(r) != len(csvHeader) {
			t.Fatalf("row %d width %d, want %d", i, len(r), len(csvHeader))
		}
	}

	// spot-check the device row against header positions
	col := func(name string) int {
		for i, h := range csvHeader {
			if h == name {
				return i
			}
		}
		t.Fatalf("unknown column %q", name)
		return -1
	}
	dev := rows[1]
	if dev[col("hostname")] != "esx-01" {
		t.Errorf("hostname = %q", dev[col("hostname")])
	}
	if dev[col("hw_certified")] != "TRUE" {
		t.Errorf("hw_certified = %q, want TRUE", dev[col("hw_certified")])
	}
	if dev[col("driver_certified")] != "FALSE" {
		t.Errorf("driver_certified = %q, want FALSE", dev[col("driver_certified")])
	}
	if dev[col("max_supported_release")] != "ESXi 9.0" {
		t.Errorf("max_supported_release = %q", dev[col("max_supported_release")])
	}
	if dev[col("supported_drivers")] != "nenic 2.1;nenic 2.2" {
		t.Errorf("supported_drivers = %q", dev[col("supported_drivers")])
	}
	if dev[col("number_of_instances")] != "2" {
		t.Errorf("number_of_instances = %q", dev[col("number_of_instances")])
	}

	// skip row carries the reason and leaves device columns empty
	skip := rows[2]
	if skip[col("skip_reason")] == "" || skip[col("device")] != "" {
		t.Errorf("skip row malformed: reason=%q device=%q", skip[col("skip_reason")], skip[col("device")])
	}
}
