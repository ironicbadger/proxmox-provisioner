package tailnet

import (
	"testing"

	tsapi "tailscale.com/client/tailscale"
)

func TestSelectCleanupDevicePrefersAddressMatch(t *testing.T) {
	t.Parallel()

	devices := []*tsapi.Device{
		{DeviceID: "dev-1", Name: "app.example-tailnet.ts.net", Hostname: "app", Addresses: []string{"100.64.0.10"}},
		{DeviceID: "dev-2", Name: "db.example-tailnet.ts.net", Hostname: "db", Addresses: []string{"100.64.0.11"}},
	}

	match, mode, err := selectCleanupDevice(devices, CleanupQuery{
		Hostname:  "app",
		Addresses: []string{"100.64.0.10"},
	})
	if err != nil {
		t.Fatalf("selectCleanupDevice returned error: %v", err)
	}
	if match == nil || match.DeviceID != "dev-1" {
		t.Fatalf("unexpected device match: %#v", match)
	}
	if mode != "tailscale-ip" {
		t.Fatalf("unexpected match mode: %s", mode)
	}
}

func TestSelectCleanupDeviceFallsBackToHostname(t *testing.T) {
	t.Parallel()

	devices := []*tsapi.Device{
		{DeviceID: "dev-1", Name: "app.example-tailnet.ts.net", Hostname: "app"},
	}

	match, mode, err := selectCleanupDevice(devices, CleanupQuery{Hostname: "app"})
	if err != nil {
		t.Fatalf("selectCleanupDevice returned error: %v", err)
	}
	if match == nil || match.DeviceID != "dev-1" {
		t.Fatalf("unexpected device match: %#v", match)
	}
	if mode != "hostname" {
		t.Fatalf("unexpected match mode: %s", mode)
	}
}

func TestSelectCleanupDeviceRejectsAmbiguousHostname(t *testing.T) {
	t.Parallel()

	devices := []*tsapi.Device{
		{DeviceID: "dev-1", Name: "app.example-tailnet.ts.net", Hostname: "app"},
		{DeviceID: "dev-2", Name: "app.other-tailnet.ts.net", Hostname: "app"},
	}

	match, _, err := selectCleanupDevice(devices, CleanupQuery{Hostname: "app"})
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if match != nil {
		t.Fatalf("expected no match, got %#v", match)
	}
}
