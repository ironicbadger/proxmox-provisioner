package tailnet

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/ironicbadger/proxmox-provisioner/internal/config"
	"golang.org/x/oauth2/clientcredentials"
	tsapi "tailscale.com/client/tailscale"
)

type GuestLogin struct {
	AuthKey string
	SSH     bool
	Tags    []string
}

func PrepareGuestLogin(ctx context.Context, cfg config.TailnetConfig, dryRun bool) (*GuestLogin, error) {
	oauth, configured, err := resolveOAuthConfig(cfg)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, nil
	}

	login := &GuestLogin{
		SSH:  oauth.SSH,
		Tags: append([]string{}, oauth.Tags...),
	}
	if dryRun {
		login.AuthKey = "<generated-tailnet-auth-key>"
		return login, nil
	}

	tsapi.I_Acknowledge_This_API_Is_Unstable = true
	client := tsapi.NewClient("-", nil)
	client.UserAgent = "pprov"
	client.HTTPClient = oauth.HTTPClient(ctx)
	if oauth.BaseURL != "" {
		client.BaseURL = oauth.BaseURL
	}

	key, _, err := client.CreateKey(ctx, tsapi.KeyCapabilities{
		Devices: tsapi.KeyDeviceCapabilities{
			Create: tsapi.KeyDeviceCreateCapabilities{
				Reusable:      oauth.Reusable,
				Ephemeral:     oauth.Ephemeral,
				Preauthorized: oauth.Preauthorized,
				Tags:          oauth.Tags,
			},
		},
	})
	if err != nil {
		return nil, err
	}

	login.AuthKey = key
	return login, nil
}

type oauthConfig struct {
	ClientID      string
	ClientSecret  string
	Tags          []string
	Preauthorized bool
	Reusable      bool
	Ephemeral     bool
	SSH           bool
	BaseURL       string
}

func (o oauthConfig) HTTPClient(ctx context.Context) *http.Client {
	credentials := clientcredentials.Config{
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		TokenURL:     firstNonEmpty(strings.TrimRight(o.BaseURL, "/"), "https://api.tailscale.com") + "/api/v2/oauth/token",
	}
	return credentials.Client(ctx)
}

func resolveOAuthConfig(cfg config.TailnetConfig) (oauthConfig, bool, error) {
	configured := oauthConfigured(cfg.OAuth)
	if !configured {
		return oauthConfig{}, false, nil
	}

	clientID := resolveConfigOrEnvValue(cfg.OAuth.ClientID)
	if clientID == "" && cfg.OAuth.ClientIDEnv != "" {
		clientID = resolveConfigOrEnvValue(cfg.OAuth.ClientIDEnv)
	}
	clientSecret := resolveInlineOrEnvValue(cfg.OAuth.ClientSecret, "tskey-client-")
	if clientSecret == "" && cfg.OAuth.ClientSecretEnv != "" {
		clientSecret = resolveInlineOrEnvValue(cfg.OAuth.ClientSecretEnv, "tskey-client-")
	}

	if clientID == "" {
		if env := strings.TrimSpace(cfg.OAuth.ClientID); env != "" && looksLikeEnvName(env) {
			return oauthConfig{}, true, fmt.Errorf("tailnet.oauth is configured but no client ID was resolved; set env %q or use tailnet.oauth.client_id for an inline value", env)
		}
		if env := strings.TrimSpace(cfg.OAuth.ClientIDEnv); env != "" {
			return oauthConfig{}, true, fmt.Errorf("tailnet.oauth is configured but no client ID was resolved; set env %q or use tailnet.oauth.client_id", env)
		}
		return oauthConfig{}, true, fmt.Errorf("tailnet.oauth is configured but no client ID was resolved; use tailnet.oauth.client_id or tailnet.oauth.client_id_env")
	}
	if clientSecret == "" {
		if env := strings.TrimSpace(cfg.OAuth.ClientSecret); env != "" && !looksLikeSecret(env, "tskey-client-") {
			return oauthConfig{}, true, fmt.Errorf("tailnet.oauth is configured but no client secret was resolved; set env %q or use tailnet.oauth.client_secret for an inline value", env)
		}
		if env := strings.TrimSpace(cfg.OAuth.ClientSecretEnv); env != "" && !looksLikeSecret(env, "tskey-client-") {
			return oauthConfig{}, true, fmt.Errorf("tailnet.oauth is configured but no client secret was resolved; set env %q or use tailnet.oauth.client_secret", env)
		}
		return oauthConfig{}, true, fmt.Errorf("tailnet.oauth is configured but no client secret was resolved; use tailnet.oauth.client_secret or tailnet.oauth.client_secret_env")
	}

	tags := normalizeOAuthTags(cfg.OAuth.Tags)
	if len(tags) == 0 {
		tags = []string{"tag:container"}
	}

	return oauthConfig{
		ClientID:      clientID,
		ClientSecret:  clientSecret,
		Tags:          tags,
		Preauthorized: boolOrDefault(cfg.OAuth.Preauthorized, true),
		Reusable:      boolOrDefault(cfg.OAuth.Reusable, false),
		Ephemeral:     boolOrDefault(cfg.OAuth.Ephemeral, false),
		SSH:           boolOrDefault(cfg.OAuth.SSH, true),
		BaseURL:       strings.TrimSpace(cfg.OAuth.BaseURL),
	}, true, nil
}

func oauthConfigured(cfg config.TailnetOAuthConfig) bool {
	return strings.TrimSpace(cfg.ClientID) != "" ||
		cfg.ClientIDEnv != "" ||
		strings.TrimSpace(cfg.ClientSecret) != "" ||
		cfg.ClientSecretEnv != "" ||
		len(cfg.Tags) > 0 ||
		cfg.Preauthorized != nil ||
		cfg.Reusable != nil ||
		cfg.Ephemeral != nil ||
		cfg.SSH != nil ||
		strings.TrimSpace(cfg.BaseURL) != ""
}

func normalizeOAuthTags(values []string) []string {
	seen := map[string]struct{}{}
	tags := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			tag := strings.TrimSpace(part)
			if tag == "" {
				continue
			}
			if _, ok := seen[tag]; ok {
				continue
			}
			seen[tag] = struct{}{}
			tags = append(tags, tag)
		}
	}
	return tags
}

func boolOrDefault(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func resolveConfigOrEnvValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if resolved := strings.TrimSpace(os.Getenv(trimmed)); resolved != "" {
		return resolved
	}
	if looksLikeEnvName(trimmed) {
		return ""
	}
	return trimmed
}
