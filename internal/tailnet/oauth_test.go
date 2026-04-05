package tailnet

import (
	"context"
	"testing"

	"github.com/ironicbadger/proxmox-provisioner/internal/config"
)

func TestResolveOAuthConfigAcceptsInlineValues(t *testing.T) {
	t.Parallel()

	cfg := config.TailnetConfig{
		OAuth: config.TailnetOAuthConfig{
			ClientID:     "client-id",
			ClientSecret: "tskey-client-secret",
		},
	}

	oauth, configured, err := resolveOAuthConfig(cfg)
	if err != nil {
		t.Fatalf("resolveOAuthConfig returned error: %v", err)
	}
	if !configured {
		t.Fatal("expected oauth to be configured")
	}
	if oauth.ClientID != "client-id" || oauth.ClientSecret != "tskey-client-secret" {
		t.Fatalf("unexpected oauth config: %#v", oauth)
	}
	if len(oauth.Tags) != 1 || oauth.Tags[0] != "tag:container" {
		t.Fatalf("unexpected oauth tags: %#v", oauth.Tags)
	}
	if !oauth.Preauthorized || oauth.Reusable || oauth.Ephemeral || !oauth.SSH {
		t.Fatalf("unexpected oauth defaults: %#v", oauth)
	}
}

func TestResolveOAuthConfigAcceptsLiteralValuesInEnvFields(t *testing.T) {
	t.Parallel()

	cfg := config.TailnetConfig{
		OAuth: config.TailnetOAuthConfig{
			ClientIDEnv:     "inline-client-id",
			ClientSecretEnv: "tskey-client-inline",
		},
	}

	oauth, configured, err := resolveOAuthConfig(cfg)
	if err != nil {
		t.Fatalf("resolveOAuthConfig returned error: %v", err)
	}
	if !configured {
		t.Fatal("expected oauth to be configured")
	}
	if oauth.ClientID != "inline-client-id" || oauth.ClientSecret != "tskey-client-inline" {
		t.Fatalf("unexpected oauth config: %#v", oauth)
	}
}

func TestResolveOAuthConfigReadsEnvVars(t *testing.T) {
	t.Setenv("TS_OAUTH_ID", "client-id")
	t.Setenv("TS_OAUTH_SECRET", "tskey-client-secret")

	cfg := config.TailnetConfig{
		OAuth: config.TailnetOAuthConfig{
			ClientIDEnv:     "TS_OAUTH_ID",
			ClientSecretEnv: "TS_OAUTH_SECRET",
			Tags:            []string{"tag:container, tag:server"},
		},
	}

	oauth, configured, err := resolveOAuthConfig(cfg)
	if err != nil {
		t.Fatalf("resolveOAuthConfig returned error: %v", err)
	}
	if !configured {
		t.Fatal("expected oauth to be configured")
	}
	if oauth.ClientID != "client-id" || oauth.ClientSecret != "tskey-client-secret" {
		t.Fatalf("unexpected oauth config: %#v", oauth)
	}
	if len(oauth.Tags) != 2 || oauth.Tags[1] != "tag:server" {
		t.Fatalf("unexpected oauth tags: %#v", oauth.Tags)
	}
}

func TestResolveOAuthConfigReadsEnvVarsFromDirectFields(t *testing.T) {
	t.Setenv("TS_OAUTH_ID", "client-id")
	t.Setenv("TS_OAUTH_SECRET", "tskey-client-secret")

	cfg := config.TailnetConfig{
		OAuth: config.TailnetOAuthConfig{
			ClientID:     "TS_OAUTH_ID",
			ClientSecret: "TS_OAUTH_SECRET",
		},
	}

	oauth, configured, err := resolveOAuthConfig(cfg)
	if err != nil {
		t.Fatalf("resolveOAuthConfig returned error: %v", err)
	}
	if !configured {
		t.Fatal("expected oauth to be configured")
	}
	if oauth.ClientID != "client-id" || oauth.ClientSecret != "tskey-client-secret" {
		t.Fatalf("unexpected oauth config: %#v", oauth)
	}
}

func TestPrepareGuestLoginDryRunReturnsPlaceholder(t *testing.T) {
	t.Parallel()

	login, err := PrepareGuestLogin(context.Background(), config.TailnetConfig{
		OAuth: config.TailnetOAuthConfig{
			ClientID:     "client-id",
			ClientSecret: "tskey-client-secret",
		},
	}, true)
	if err != nil {
		t.Fatalf("PrepareGuestLogin returned error: %v", err)
	}
	if login == nil || login.AuthKey != "<generated-tailnet-auth-key>" || !login.SSH {
		t.Fatalf("unexpected guest login: %#v", login)
	}
}
