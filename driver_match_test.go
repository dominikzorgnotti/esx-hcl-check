package main

import "testing"

// TestParseHBAFirmwareFromSOAP verifies the SOAP parser extracts the driver
// module name (the <driver> element) in addition to firmware/driver versions.
// RAID/SAS HBAs report driverVersion but the module name lives in a separate
// <driver> element; without it the HCL matcher can't judge driver cert.
func TestParseHBAFirmwareFromSOAP(t *testing.T) {
	soap := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">
  <soapenv:Body>
    <RetrievePropertiesExResponse>
      <returnval>
        <objects>
          <propSet>
            <val>
              <HostSerialAttachedHba>
                <key>key-vim.host.SerialAttachedHba-vmhba0</key>
                <device>vmhba0</device>
                <driver>smartpqi</driver>
                <pci>0000:5c:00.0</pci>
                <driverVersion>90.4900.0.5000</driverVersion>
                <firmwareVersion>5.32</firmwareVersion>
              </HostSerialAttachedHba>
            </val>
          </propSet>
        </objects>
      </returnval>
    </RetrievePropertiesExResponse>
  </soapenv:Body>
</soapenv:Envelope>`)

	got := parseHBAFirmwareFromSOAP(soap)
	info, ok := got["0000:5c:00.0"]
	if !ok {
		t.Fatalf("expected pci 0000:5c:00.0 in result, got %+v", got)
	}
	if info.DriverName != "smartpqi" {
		t.Errorf("DriverName = %q, want %q", info.DriverName, "smartpqi")
	}
	if info.Driver != "90.4900.0.5000" {
		t.Errorf("Driver (version) = %q, want %q", info.Driver, "90.4900.0.5000")
	}
	if info.Firmware != "5.32" {
		t.Errorf("Firmware = %q, want %q", info.Firmware, "5.32")
	}
}

// makeControllerDB builds a minimal offline DB with a single controller entry
// whose 9.1 release lists the smartpqi driver at 90.4900.0.5000-3vmw.910.
func makeControllerDB() *VsanOfflineDB {
	db := &VsanOfflineDB{}
	db.Data.Controller = []map[string]interface{}{
		{
			"vid":     "9005",
			"did":     "028f",
			"svid":    "1590",
			"ssid":    "0294",
			"vcglink": "https://example/detail?productId=41777",
			"releases": map[string]interface{}{
				"ESXi 9.1": map[string]interface{}{
					"smartpqi": map[string]interface{}{
						"90.4900.0.5000-3vmw.910": map[string]interface{}{
							"firmwares": []interface{}{
								map[string]interface{}{"firmware": "5.32"},
							},
						},
					},
				},
			},
		},
	}
	return db
}

// TestDriverMatchVersionOnlyFallback is the regression test for the reported
// false-negative: a RAID controller reports a driver *version* that matches the
// HCL, but no driver *name*. The matcher must still certify the driver.
func TestDriverMatchVersionOnlyFallback(t *testing.T) {
	db := makeControllerDB()

	// DriverName intentionally empty — the host did not report a module name.
	res := &HCLResult{
		DriverVer: "90.4900.0.5000",
		Firmware:  "5.32",
	}
	if !evaluateVsanPCI(db, "9005", "028f", "1590", "0294", "ESXi 9.1", res) {
		t.Fatal("expected device to match by PCI ID")
	}
	if res.DriverCertified != CertTrue {
		t.Errorf("DriverCertified = %v, want TRUE (version matches despite empty name)", res.DriverCertified)
	}
	if res.FirmwareCertified != CertTrue {
		t.Errorf("FirmwareCertified = %v, want TRUE", res.FirmwareCertified)
	}
}

// TestDriverMatchVersionMismatch guards against the fallback becoming a blanket
// pass: an empty driver name with a non-matching version must stay FALSE.
func TestDriverMatchVersionMismatch(t *testing.T) {
	db := makeControllerDB()

	res := &HCLResult{DriverVer: "1.2.3.4"}
	if !evaluateVsanPCI(db, "9005", "028f", "1590", "0294", "ESXi 9.1", res) {
		t.Fatal("expected device to match by PCI ID")
	}
	if res.DriverCertified != CertFalse {
		t.Errorf("DriverCertified = %v, want FALSE (version does not match)", res.DriverCertified)
	}
}

// TestDriverMatchNameStillEnforced confirms that when the host DOES report a
// driver name, a mismatched name still prevents a version-only false positive.
func TestDriverMatchNameStillEnforced(t *testing.T) {
	db := makeControllerDB()

	res := &HCLResult{DriverName: "nvme", DriverVer: "90.4900.0.5000"}
	if !evaluateVsanPCI(db, "9005", "028f", "1590", "0294", "ESXi 9.1", res) {
		t.Fatal("expected device to match by PCI ID")
	}
	if res.DriverCertified != CertFalse {
		t.Errorf("DriverCertified = %v, want FALSE (name 'nvme' != 'smartpqi')", res.DriverCertified)
	}
}
