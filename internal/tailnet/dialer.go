package tailnet

import (
	"context"
	"net"
	"os"
	"strings"

	"github.com/ironicbadger/proxmox-provisioner/internal/config"
)

type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Close() error
}

type systemDialer struct {
	d net.Dialer
}

func (s *systemDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return s.d.DialContext(ctx, network, address)
}

func (s *systemDialer) Close() error {
	return nil
}

func NewDialer(_ config.TailnetConfig) (Dialer, error) {
	return &systemDialer{}, nil
}

func ResolveAPIKey(cfg config.TailnetConfig) (string, bool) {
	if value := strings.TrimSpace(cfg.APIKey); value != "" {
		return resolveInlineOrEnvValue(value, "tskey-api-"), true
	}
	if value := strings.TrimSpace(cfg.APIKeyEnv); value != "" {
		return resolveInlineOrEnvValue(value, "tskey-api-"), true
	}
	return "", false
}

func resolveInlineOrEnvValue(value string, secretPrefixes ...string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if looksLikeSecret(trimmed, secretPrefixes...) {
		return trimmed
	}
	if resolved := strings.TrimSpace(os.Getenv(trimmed)); resolved != "" {
		return resolved
	}
	if looksLikeEnvName(trimmed) {
		return ""
	}
	return trimmed
}

func looksLikeSecret(value string, secretPrefixes ...string) bool {
	trimmed := strings.TrimSpace(value)
	for _, prefix := range secretPrefixes {
		if strings.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

func looksLikeEnvName(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	hasUnderscore := false
	for _, r := range trimmed {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
			hasUnderscore = true
		default:
			return false
		}
	}
	return hasUnderscore
}
