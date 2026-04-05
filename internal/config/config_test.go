package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProfileDefaultsLXCToPprovTag(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Profiles: map[string]ProvisionProfile{
			"demo": {
				Connection: "fwd",
				Kind:       "lxc",
			},
		},
	}

	profile, err := cfg.Profile("demo")
	if err != nil {
		t.Fatalf("Profile returned error: %v", err)
	}
	if profile.Tag != "pprov" {
		t.Fatalf("unexpected default tag: %q", profile.Tag)
	}
}

func TestProfileKeepsCustomTag(t *testing.T) {
	t.Parallel()

	cfg := &Config{
		Profiles: map[string]ProvisionProfile{
			"demo": {
				Connection: "fwd",
				Kind:       "lxc",
				Tag:        "homelab",
			},
		},
	}

	profile, err := cfg.Profile("demo")
	if err != nil {
		t.Fatalf("Profile returned error: %v", err)
	}
	if profile.Tag != "homelab" {
		t.Fatalf("unexpected custom tag: %q", profile.Tag)
	}
}

func TestLoadReadsDotEnvBesideConfig(t *testing.T) {
	if err := os.Unsetenv("TS_TAILSCALE_OAUTH_CLIENT_ID"); err != nil {
		t.Fatalf("unset client id env: %v", err)
	}
	if err := os.Unsetenv("TS_TAILSCALE_OAUTH_CLIENT_SECRET"); err != nil {
		t.Fatalf("unset client secret env: %v", err)
	}

	dir := t.TempDir()
	configPath := filepath.Join(dir, "pprov.yaml")
	if err := os.WriteFile(configPath, []byte("tailnet:\n  oauth:\n    client_id_env: TS_TAILSCALE_OAUTH_CLIENT_ID\n    client_secret_env: TS_TAILSCALE_OAUTH_CLIENT_SECRET\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TS_TAILSCALE_OAUTH_CLIENT_ID=client-id\nTS_TAILSCALE_OAUTH_CLIENT_SECRET=tskey-client-secret\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	if _, err := Load(configPath); err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if got := os.Getenv("TS_TAILSCALE_OAUTH_CLIENT_ID"); got != "client-id" {
		t.Fatalf("unexpected dotenv client id: %q", got)
	}
	if got := os.Getenv("TS_TAILSCALE_OAUTH_CLIENT_SECRET"); got != "tskey-client-secret" {
		t.Fatalf("unexpected dotenv client secret: %q", got)
	}
}
