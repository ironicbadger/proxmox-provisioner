# pprov

`pprov` is a small Go CLI for creating Proxmox LXCs and VMs over Tailscale SSH.

Install:

```bash
go install github.com/ironicbadger/proxmox-provisioner/cmd/pprov@latest
```

It exists to make the common path fast:

- probe a node
- pick a live template
- create a guest from a named profile
- run post-install steps like Docker and Tailscale
- tear the guest down again later

## Assumptions

- You run `pprov` on your laptop, not on the Proxmox host.
- The Proxmox node is reachable over SSH, usually through Tailscale.
- `root` on the Proxmox node can run `pct`, `qm`, `pvesh`, and `pvesm`.
- LXC root passwords are either provided by env var or entered interactively.
- Guest Tailscale login is optional and only happens when `--tslogin` is used.

`pprov` does not use the Proxmox API for create, list, probe, or destroy. It drives the node over SSH.

## Quick Start

```bash
go run ./cmd/pprov init > pprov.yaml
go run ./cmd/pprov probe fwd
go run ./cmd/pprov list --connection fwd
PPROV_LXC_ROOT_PASSWORD='changeme' go run ./cmd/pprov create debian-docker-lxc testbox
PPROV_LXC_ROOT_PASSWORD='changeme' go run ./cmd/pprov create debian-docker-lxc testbox --tslogin
go run ./cmd/pprov destroy --connection fwd --force 5000
```

## Config

Minimal config with `.env` values:

```yaml
tailnet:
  api_key: TS_API_KEY
  oauth:
    client_id: TS_OAUTH_CLIENT_ID
    client_secret: TS_OAUTH_CLIENT_SECRET
    tags:
      - tag:container

connections:
  fwd:
    ssh:
      target: root@proxmox.example.ts.net
      auth: auto
    defaults:
      start_id: 5000

profiles:
  debian-docker-lxc:
    connection: fwd
    kind: lxc
    locale: en_US.UTF-8
    template_match: debian
    tag: pprov
    updated: true
    root_password_env: PPROV_LXC_ROOT_PASSWORD
    cores: 4
    memory: 4096
    rootfs_gb: 16
    features:
      - nesting=1
    addons:
      - bootstrap-tools
      - docker
      - tailscale
      - lxc-tun
      - zaphod-user
```

`.env` is loaded from the config directory and the current working directory.

See [examples/pprov.yaml](examples/pprov.yaml) for a fuller example.

## Notes

- `probe` and `create` resolve live templates, storage, bridge, and IDs from the node.
- `list` only shows LXCs.
- `destroy` stops a running LXC, destroys it, and can also remove its Tailscale node if an API key is configured.
- `updated: true` runs `apt-get update`, `apt-get upgrade -y`, installs `curl`, and generates a usable UTF-8 locale.
- Built-in add-ons are: `bootstrap-tools`, `docker`, `tailscale`, `ip-forwarding`, `udp-gro-forwarding`, `lxc-tun`, and `zaphod-user`.
- Tagged releases publish Linux and macOS tarballs in GitHub Releases.
