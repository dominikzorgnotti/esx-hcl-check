package main

import (
	"context"
	"strings"
	"testing"
)

// TestConnectToVCDoesNotLeakPassword locks the guarantee that a connection
// failure never echoes GOVC_PASSWORD back to the user (e.g. via an embedded
// URL in a wrapped error).
func TestConnectToVCDoesNotLeakPassword(t *testing.T) {
	const secret = "SuperSecret!Passw0rd-do-not-print"
	t.Setenv("GOVC_URL", "https://esx-hcl-check-nonexistent-host.invalid")
	t.Setenv("GOVC_USERNAME", "administrator@vsphere.local")
	t.Setenv("GOVC_PASSWORD", secret)
	t.Setenv("GOVC_INSECURE", "1")

	_, err := connectToVC(context.Background())
	if err == nil {
		t.Fatal("expected a connection error against a bogus host")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("connection error leaked the password: %v", err)
	}
}
