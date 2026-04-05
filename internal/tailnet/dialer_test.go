package tailnet

import (
	"context"
	"testing"

	"github.com/ironicbadger/proxmox-provisioner/internal/config"
)

func TestNewDialerAlwaysUsesSystemDialer(t *testing.T) {
	t.Parallel()

	dialer, err := NewDialer(config.TailnetConfig{})
	if err != nil {
		t.Fatalf("NewDialer returned error: %v", err)
	}
	if _, ok := dialer.(*systemDialer); !ok {
		t.Fatalf("expected systemDialer, got %T", dialer)
	}
	if err := dialer.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestSystemDialerUsesStandardNetDialer(t *testing.T) {
	t.Parallel()

	dialer, err := NewDialer(config.TailnetConfig{})
	if err != nil {
		t.Fatalf("NewDialer returned error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dialer.DialContext(ctx, "tcp", "127.0.0.1:22"); err == nil {
		t.Fatal("expected canceled context error")
	}
}

func TestResolveAPIKey(t *testing.T) {
	t.Setenv("TS_API_TOKEN", "tskey-api-from-env")

	if key, ok := ResolveAPIKey(config.TailnetConfig{APIKey: "tskey-api-inline"}); !ok || key != "tskey-api-inline" {
		t.Fatalf("unexpected inline API key: key=%q ok=%v", key, ok)
	}
	if key, ok := ResolveAPIKey(config.TailnetConfig{APIKey: "TS_API_TOKEN"}); !ok || key != "tskey-api-from-env" {
		t.Fatalf("unexpected env API key in direct field: key=%q ok=%v", key, ok)
	}
	if key, ok := ResolveAPIKey(config.TailnetConfig{APIKeyEnv: "TS_API_TOKEN"}); !ok || key != "tskey-api-from-env" {
		t.Fatalf("unexpected env API key: key=%q ok=%v", key, ok)
	}
	if key, ok := ResolveAPIKey(config.TailnetConfig{APIKeyEnv: "tskey-api-inline-env"}); !ok || key != "tskey-api-inline-env" {
		t.Fatalf("unexpected literal API key in env field: key=%q ok=%v", key, ok)
	}
}
