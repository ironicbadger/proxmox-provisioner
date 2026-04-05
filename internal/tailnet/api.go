package tailnet

import (
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/ironicbadger/proxmox-provisioner/internal/config"
	tsapi "tailscale.com/client/tailscale"
)

type API struct {
	client *tsapi.Client
}

var tailscaleDeleteLogMu sync.Mutex

type CleanupQuery struct {
	Hostname  string
	Addresses []string
}

type CleanupResult struct {
	Configured bool
	Removed    bool
	DeviceID   string
	DeviceName string
	Match      string
	Message    string
}

func NewAPI(cfg config.TailnetConfig) (*API, error) {
	apiKey, configured := ResolveAPIKey(cfg)
	if !configured {
		return nil, nil
	}
	if apiKey == "" {
		if env := strings.TrimSpace(cfg.APIKey); env != "" && !looksLikeSecret(env, "tskey-api-") {
			return nil, fmt.Errorf("tailnet.api_key is configured but no API key was resolved; set env %q or use tailnet.api_key for an inline key", env)
		}
		if env := strings.TrimSpace(cfg.APIKeyEnv); env != "" && !looksLikeSecret(env, "tskey-api-") {
			return nil, fmt.Errorf("tailnet.api_key is configured but no API key was resolved; set env %q or use tailnet.api_key for an inline key", env)
		}
		return nil, fmt.Errorf("tailnet.api_key is configured but no API key was resolved; use tailnet.api_key or tailnet.api_key_env")
	}

	tsapi.I_Acknowledge_This_API_Is_Unstable = true
	client := tsapi.NewClient("-", tsapi.APIKey(apiKey))
	return &API{client: client}, nil
}

func (a *API) DeleteDeviceForCleanup(ctx context.Context, query CleanupQuery) (*CleanupResult, error) {
	devices, err := a.client.Devices(ctx, tsapi.DeviceDefaultFields)
	if err != nil {
		return nil, err
	}

	match, matchMode, err := selectCleanupDevice(devices, query)
	if err != nil {
		return &CleanupResult{
			Configured: true,
			Message:    err.Error(),
		}, nil
	}
	if match == nil {
		return &CleanupResult{
			Configured: true,
			Message:    fmt.Sprintf("no matching tailnet device found for hostname=%s", valueOrUnknown(query.Hostname)),
		}, nil
	}
	if err := deleteDeviceQuietly(ctx, a.client, match.DeviceID); err != nil {
		return nil, err
	}

	return &CleanupResult{
		Configured: true,
		Removed:    true,
		DeviceID:   match.DeviceID,
		DeviceName: deviceLabel(match),
		Match:      matchMode,
		Message:    fmt.Sprintf("removed %s via %s", deviceLabel(match), matchMode),
	}, nil
}

func deleteDeviceQuietly(ctx context.Context, client *tsapi.Client, deviceID string) error {
	tailscaleDeleteLogMu.Lock()
	defer tailscaleDeleteLogMu.Unlock()

	logger := log.Default()
	originalWriter := logger.Writer()
	logger.SetOutput(io.Discard)
	defer logger.SetOutput(originalWriter)

	return client.DeleteDevice(ctx, deviceID)
}

func selectCleanupDevice(devices []*tsapi.Device, query CleanupQuery) (*tsapi.Device, string, error) {
	if len(devices) == 0 {
		return nil, "", nil
	}

	addresses := normalizeAddresses(query.Addresses)
	if len(addresses) > 0 {
		if match, err := uniqueDeviceMatch(filterByAddress(devices, addresses), "Tailscale IP"); match != nil || err != nil {
			return match, "tailscale-ip", err
		}
	}

	hostname := normalizeHostname(query.Hostname)
	if hostname == "" {
		return nil, "", nil
	}

	if match, err := uniqueDeviceMatch(filterByHostname(devices, hostname), "hostname"); match != nil || err != nil {
		return match, "hostname", err
	}
	if match, err := uniqueDeviceMatch(filterByShortName(devices, hostname), "MagicDNS name"); match != nil || err != nil {
		return match, "magicdns-name", err
	}
	return nil, "", nil
}

func filterByAddress(devices []*tsapi.Device, want map[string]struct{}) []*tsapi.Device {
	matches := []*tsapi.Device{}
	seen := map[string]struct{}{}
	for _, device := range devices {
		for _, addr := range device.Addresses {
			if _, ok := want[strings.TrimSpace(addr)]; !ok {
				continue
			}
			if _, ok := seen[device.DeviceID]; ok {
				break
			}
			seen[device.DeviceID] = struct{}{}
			matches = append(matches, device)
			break
		}
	}
	return matches
}

func filterByHostname(devices []*tsapi.Device, hostname string) []*tsapi.Device {
	matches := []*tsapi.Device{}
	for _, device := range devices {
		if normalizeHostname(device.Hostname) == hostname {
			matches = append(matches, device)
		}
	}
	return matches
}

func filterByShortName(devices []*tsapi.Device, hostname string) []*tsapi.Device {
	matches := []*tsapi.Device{}
	for _, device := range devices {
		full := normalizeHostname(device.Name)
		if full == hostname {
			matches = append(matches, device)
			continue
		}
		short := full
		if idx := strings.Index(short, "."); idx >= 0 {
			short = short[:idx]
		}
		if short == hostname {
			matches = append(matches, device)
		}
	}
	return matches
}

func uniqueDeviceMatch(matches []*tsapi.Device, matchLabel string) (*tsapi.Device, error) {
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) == 1 {
		return matches[0], nil
	}

	names := make([]string, 0, len(matches))
	for _, device := range matches {
		names = append(names, deviceLabel(device))
	}
	sort.Strings(names)
	return nil, fmt.Errorf("multiple tailnet devices matched by %s: %s", matchLabel, strings.Join(names, ", "))
}

func normalizeAddresses(values []string) map[string]struct{} {
	addresses := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		addresses[trimmed] = struct{}{}
	}
	return addresses
}

func normalizeHostname(value string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(value)), ".")
}

func deviceLabel(device *tsapi.Device) string {
	if name := strings.TrimSpace(device.Name); name != "" {
		return name
	}
	if name := strings.TrimSpace(device.Hostname); name != "" {
		return name
	}
	return device.DeviceID
}

func valueOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
