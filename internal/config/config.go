package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Tailnet     TailnetConfig               `yaml:"tailnet"`
	Connections map[string]Connection       `yaml:"connections"`
	Profiles    map[string]ProvisionProfile `yaml:"profiles"`
	BaseDir     string                      `yaml:"-"`
}

type TailnetConfig struct {
	APIKey    string             `yaml:"api_key"`
	APIKeyEnv string             `yaml:"api_key_env"`
	OAuth     TailnetOAuthConfig `yaml:"oauth"`
}

type TailnetOAuthConfig struct {
	ClientID        string   `yaml:"client_id"`
	ClientIDEnv     string   `yaml:"client_id_env"`
	ClientSecret    string   `yaml:"client_secret"`
	ClientSecretEnv string   `yaml:"client_secret_env"`
	Tags            []string `yaml:"tags"`
	Preauthorized   *bool    `yaml:"preauthorized"`
	Reusable        *bool    `yaml:"reusable"`
	Ephemeral       *bool    `yaml:"ephemeral"`
	SSH             *bool    `yaml:"ssh"`
	BaseURL         string   `yaml:"base_url"`
}

type Connection struct {
	SSH      SSHConfig          `yaml:"ssh"`
	Defaults ConnectionDefaults `yaml:"defaults"`
}

type ConnectionDefaults struct {
	Bridge         string `yaml:"bridge"`
	LXCStorage     string `yaml:"lxc_storage"`
	VMStorage      string `yaml:"vm_storage"`
	SnippetStorage string `yaml:"snippet_storage"`
	SnippetDir     string `yaml:"snippet_dir"`
	StartID        int    `yaml:"start_id"`
}

type SSHConfig struct {
	Target      string `yaml:"target"`
	Auth        string `yaml:"auth"`
	PasswordEnv string `yaml:"password_env"`
}

type ProvisionProfile struct {
	Connection       string            `yaml:"connection"`
	Kind             string            `yaml:"kind"`
	Locale           string            `yaml:"locale"`
	Tag              string            `yaml:"tag"`
	Template         string            `yaml:"template"`
	TemplateMatch    string            `yaml:"template_match"`
	Storage          string            `yaml:"storage"`
	Bridge           string            `yaml:"bridge"`
	VLAN             int               `yaml:"vlan"`
	Cores            int               `yaml:"cores"`
	Memory           int               `yaml:"memory"`
	Swap             int               `yaml:"swap"`
	RootFSGiB        int               `yaml:"rootfs_gb"`
	DiskGiB          int               `yaml:"disk_gb"`
	Features         []string          `yaml:"features"`
	FullClone        *bool             `yaml:"full_clone"`
	Unprivileged     *bool             `yaml:"unprivileged"`
	Onboot           *bool             `yaml:"onboot"`
	StartAfterCreate *bool             `yaml:"start_after_create"`
	IPMode           string            `yaml:"ip_mode"`
	DefaultIP        string            `yaml:"default_ip"`
	DefaultGateway   string            `yaml:"default_gateway"`
	Updated          bool              `yaml:"updated"`
	StartID          int               `yaml:"start_id"`
	RootPasswordEnv  string            `yaml:"root_password_env"`
	Addons           []string          `yaml:"addons"`
	ShellSnippet     string            `yaml:"shell_snippet"`
	CloudInitSnippet string            `yaml:"cloud_init_snippet"`
	ExtraCreateArgs  map[string]string `yaml:"extra_create_args"`
	ExtraSetArgs     map[string]string `yaml:"extra_set_args"`
	VMDiskDevice     string            `yaml:"vm_disk_device"`
	VMNetModel       string            `yaml:"vm_net_model"`
}

func DefaultPath() string {
	return "pprov.yaml"
}

func Load(path string) (*Config, error) {
	absPath, err := filepath.Abs(expandHome(path))
	if err != nil {
		return nil, err
	}
	if err := loadDotEnv(filepath.Dir(absPath)); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	cfg.BaseDir = filepath.Dir(absPath)
	if cfg.Connections == nil {
		cfg.Connections = map[string]Connection{}
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]ProvisionProfile{}
	}
	return &cfg, nil
}

func (c *Config) Connection(name string) (Connection, error) {
	connection, ok := c.Connections[name]
	if !ok {
		return Connection{}, fmt.Errorf("unknown connection %q", name)
	}
	if connection.SSH.Target == "" {
		return Connection{}, fmt.Errorf("connection %q is missing ssh.target", name)
	}
	if connection.SSH.Auth == "" {
		connection.SSH.Auth = "auto"
	}
	return connection, nil
}

func (c *Config) Profile(name string) (ProvisionProfile, error) {
	profile, ok := c.Profiles[name]
	if !ok {
		return ProvisionProfile{}, fmt.Errorf("unknown profile %q", name)
	}
	if profile.Connection == "" {
		return ProvisionProfile{}, fmt.Errorf("profile %q is missing connection", name)
	}
	if profile.Kind == "" {
		return ProvisionProfile{}, fmt.Errorf("profile %q is missing kind", name)
	}
	if profile.Kind == "lxc" && strings.TrimSpace(profile.Tag) == "" {
		profile.Tag = "pprov"
	}
	if profile.IPMode == "" {
		profile.IPMode = "dhcp"
	}
	if profile.VMDiskDevice == "" {
		profile.VMDiskDevice = "scsi0"
	}
	if profile.VMNetModel == "" {
		profile.VMNetModel = "virtio"
	}
	return profile, nil
}

func (c *Config) ResolvePath(path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(c.BaseDir, path)
}

func expandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func loadDotEnv(baseDir string) error {
	paths := []string{filepath.Join(baseDir, ".env")}
	if cwd, err := os.Getwd(); err == nil {
		cwdEnv := filepath.Join(cwd, ".env")
		if cwdEnv != paths[0] {
			paths = append(paths, cwdEnv)
		}
	}
	for _, path := range paths {
		if err := loadDotEnvFile(path); err != nil {
			return err
		}
	}
	return nil
}

func loadDotEnvFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func ExampleConfig() string {
	return `tailnet:
  # api_key: tskey-api-...
  # api_key_env: TS_TAILSCALE_API_KEY
  # oauth:
  #   client_id: tskey-client-id
  #   client_secret: tskey-client-secret
  #   client_id_env: TS_TAILSCALE_OAUTH_CLIENT_ID
  #   client_secret_env: TS_TAILSCALE_OAUTH_CLIENT_SECRET
  #   tags:
  #     - tag:container
  #   preauthorized: true
  #   ssh: true

connections:
  fwd:
    ssh:
      target: root@proxmox.example.ts.net
      auth: auto
    defaults:
      start_id: 2000

profiles:
  debian-docker-lxc:
    connection: fwd
    kind: lxc
    # locale: en_US.UTF-8
    tag: pprov
    template_match: debian
    updated: true
    root_password_env: PPROV_LXC_ROOT_PASSWORD
    cores: 2
    memory: 2048
    rootfs_gb: 16
    features:
      - nesting=1
    addons:
      - bootstrap-tools
      - docker
      - tailscale
      - lxc-tun
      - ip-forwarding
      - udp-gro-forwarding
      - zaphod-user
    shell_snippet: examples/snippets/post-install/homelab-base.sh

  ubuntu-cloudinit-vm:
    connection: fwd
    kind: vm
    # locale: en_US.UTF-8
    template_match: ubuntu
    updated: true
    cores: 2
    memory: 4096
    disk_gb: 32
    addons:
      - bootstrap-tools
      - docker
      - tailscale
    cloud_init_snippet: examples/snippets/cloud-init/base-user-data.yaml
`
}
