package render

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/ironicbadger/proxmox-provisioner/internal/addons"
	"gopkg.in/yaml.v3"
)

type Context map[string]string

const updatedGuestScript = `export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get upgrade -y
apt-get install -y curl%s
`

func File(path string, ctx Context) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Text(string(data), ctx), nil
}

func Text(text string, ctx Context) string {
	replacer := make([]string, 0, len(ctx)*2)
	keys := make([]string, 0, len(ctx))
	for key := range ctx {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		replacer = append(replacer, "{"+key+"}", ctx[key])
	}
	return strings.NewReplacer(replacer...).Replace(text)
}

func GuestScript(updated bool, locale string, addonKeys []string, renderedExtra string) string {
	parts := []string{}
	if updated {
		parts = append(parts, strings.TrimSpace(baseGuestScript(true, locale)))
	} else if strings.TrimSpace(locale) != "" {
		parts = append(parts, strings.TrimSpace(baseGuestScript(false, locale)))
	}
	for _, key := range addonKeys {
		if builtin, ok := addons.Builtins[key]; ok && strings.TrimSpace(builtin.GuestScript) != "" {
			parts = append(parts, strings.TrimSpace(builtin.GuestScript))
		}
	}
	if strings.TrimSpace(renderedExtra) != "" {
		parts = append(parts, strings.TrimSpace(renderedExtra))
	}
	if len(parts) == 0 {
		return ""
	}
	return "#!/usr/bin/env bash\nset -euo pipefail\n\n" + strings.Join(parts, "\n\n") + "\n"
}

func baseGuestScript(updated bool, locale string) string {
	parts := []string{}
	locale = strings.TrimSpace(locale)

	if updated {
		extraPackages := ""
		if locale != "" {
			extraPackages = " locales"
		}
		parts = append(parts, fmt.Sprintf(updatedGuestScript, extraPackages))
	}
	if locale != "" {
		parts = append(parts, localeSetupScript(locale, !updated))
	}
	return strings.Join(parts, "\n\n")
}

func localeSetupScript(locale string, installPackages bool) string {
	commands := []string{}
	if installPackages {
		commands = append(commands, "export DEBIAN_FRONTEND=noninteractive\napt-get update\napt-get install -y locales")
	}

	localeQuoted := shellQuote(locale)
	pattern := regexpQuote(locale)
	commands = append(commands, fmt.Sprintf(`if [ -f /etc/locale.gen ]; then
  if grep -Eq '^[[:space:]]*#?[[:space:]]*%s([[:space:]]|$)' /etc/locale.gen; then
    sed -i -E 's/^[[:space:]]*#?[[:space:]]*(%s([[:space:]].*)?)$/\1/' /etc/locale.gen
  else
    printf '%%s UTF-8\n' %s >> /etc/locale.gen
  fi
fi
locale-gen %s
update-locale LANG=%s
`, pattern, pattern, localeQuoted, localeQuoted, localeQuoted))

	return strings.Join(commands, "\n\n")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func regexpQuote(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`.`, `\.`,
		`+`, `\+`,
		`*`, `\*`,
		`?`, `\?`,
		`(`, `\(`,
		`)`, `\)`,
		`[`, `\[`,
		`]`, `\]`,
		`{`, `\{`,
		`}`, `\}`,
		`^`, `\^`,
		`$`, `\$`,
		`|`, `\|`,
	)
	return replacer.Replace(value)
}

func LXCConfigLines(addonKeys []string) []string {
	seen := map[string]bool{}
	lines := []string{}
	for _, key := range addonKeys {
		builtin, ok := addons.Builtins[key]
		if !ok {
			continue
		}
		for _, line := range builtin.LXCConfigLines {
			if !seen[line] {
				seen[line] = true
				lines = append(lines, line)
			}
		}
	}
	return lines
}

func CloudInit(renderedBase, guestScript string) (string, error) {
	if strings.TrimSpace(renderedBase) == "" && strings.TrimSpace(guestScript) == "" {
		return "", nil
	}
	payload := map[string]any{}
	if strings.TrimSpace(renderedBase) != "" {
		raw := strings.TrimSpace(renderedBase)
		raw = strings.TrimPrefix(raw, "#cloud-config")
		if strings.TrimSpace(raw) != "" {
			if err := yaml.Unmarshal([]byte(raw), &payload); err != nil {
				return "", fmt.Errorf("cloud-init snippet must be a mapping: %w", err)
			}
		}
	}
	if strings.TrimSpace(guestScript) != "" {
		writeFiles, _ := payload["write_files"].([]any)
		writeFiles = append(writeFiles, map[string]any{
			"path":        "/var/lib/cloud/scripts/per-instance/pprov.sh",
			"permissions": "0755",
			"content":     guestScript,
		})
		payload["write_files"] = writeFiles

		runcmd, _ := payload["runcmd"].([]any)
		runcmd = append(runcmd, "bash /var/lib/cloud/scripts/per-instance/pprov.sh")
		payload["runcmd"] = runcmd
	}
	encoded, err := yaml.Marshal(payload)
	if err != nil {
		return "", err
	}
	return "#cloud-config\n" + string(encoded), nil
}
