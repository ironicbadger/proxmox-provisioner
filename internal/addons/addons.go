package addons

type Definition struct {
	Key            string
	GuestScript    string
	LXCConfigLines []string
}

var Builtins = map[string]Definition{
	"bootstrap-tools": {
		Key: "bootstrap-tools",
		GuestScript: `apt-get install -y sudo ethtool
`,
	},
	"docker": {
		Key: "docker",
		GuestScript: `curl -fsSL https://get.docker.com | sh
`,
	},
	"tailscale": {
		Key: "tailscale",
		GuestScript: `curl -fsSL https://tailscale.com/install.sh | sh
`,
	},
	"ip-forwarding": {
		Key: "ip-forwarding",
		GuestScript: `cat >/etc/sysctl.d/99-tailscale.conf <<'EOF'
net.ipv4.ip_forward = 1
net.ipv6.conf.all.forwarding = 1
EOF
sysctl -p /etc/sysctl.d/99-tailscale.conf
`,
	},
	"udp-gro-forwarding": {
		Key: "udp-gro-forwarding",
		GuestScript: `NETDEV=$(ip -o route get 8.8.8.8 | awk '{print $5; exit}')
if [ -n "$NETDEV" ]; then
  ethtool -K "$NETDEV" rx-udp-gro-forwarding on rx-gro-list off || true
fi
`,
	},
	"lxc-tun": {
		Key: "lxc-tun",
		LXCConfigLines: []string{
			"lxc.cgroup2.devices.allow: c 10:200 rwm",
			"lxc.mount.entry: /dev/net/tun dev/net/tun none bind,create=file",
		},
	},
	"zaphod-user": {
		Key: "zaphod-user",
		GuestScript: `id -u zaphod >/dev/null 2>&1 || useradd -m -s /bin/bash zaphod
getent group docker >/dev/null 2>&1 && usermod -aG docker zaphod || true
`,
	},
}
