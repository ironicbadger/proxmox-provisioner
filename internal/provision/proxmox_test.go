package provision

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ironicbadger/proxmox-provisioner/internal/config"
	"github.com/ironicbadger/proxmox-provisioner/internal/render"
)

func TestParseProbe(t *testing.T) {
	report, err := parseProbe(stringsJoin(
		"node=fwd",
		"nextid=107",
		"used_id=1000",
		"used_id=1001",
		"bridge=vmbr0",
		"bridge=vmbr20",
		"storage=local|dir|/var/lib/vz|backup,iso,vztmpl,snippets",
		"storage=local-lvm|lvmthin||images,rootdir",
		"lxc_template=local:vztmpl/debian-12-standard.tar.zst",
		"vm_template=fwd|9000|ubuntu-template",
	))
	if err != nil {
		t.Fatalf("parseProbe returned error: %v", err)
	}
	if report.DefaultBridge() != "vmbr0" {
		t.Fatalf("unexpected bridge: %s", report.DefaultBridge())
	}
	if report.DefaultLXCStorage() != "local-lvm" {
		t.Fatalf("unexpected lxc storage: %s", report.DefaultLXCStorage())
	}
	if report.DefaultSnippetDir() != "/var/lib/vz/snippets" {
		t.Fatalf("unexpected snippet dir: %s", report.DefaultSnippetDir())
	}
	if len(report.LXCTemplates) != 1 || len(report.VMTemplates) != 1 {
		t.Fatalf("unexpected template counts")
	}
	if got, err := report.NextAvailableVMID(1000); err != nil || got != 1002 {
		t.Fatalf("unexpected next available vmid: got=%d err=%v", got, err)
	}
}

func TestRenderCloudInit(t *testing.T) {
	base := "runcmd:\n  - echo base\n"
	guest := render.GuestScript(true, "en_US.UTF-8", []string{"tailscale"}, "")
	out, err := render.CloudInit(base, guest)
	if err != nil {
		t.Fatalf("render cloud-init: %v", err)
	}
	if !strings.Contains(out, "echo base") || !strings.Contains(out, "tailscale.com/install.sh") || !strings.Contains(out, "apt-get update") || !strings.Contains(out, "locale-gen 'en_US.UTF-8'") {
		t.Fatalf("unexpected cloud-init output: %s", out)
	}
}

func TestResolveLXCTemplate(t *testing.T) {
	report := &ProbeReport{LXCTemplates: []string{"local:vztmpl/debian-12-standard.tar.zst"}}
	profile := config.ProvisionProfile{TemplateMatch: "debian-12-standard"}
	got, err := resolveLXCTemplate(profile, report)
	if err != nil {
		t.Fatalf("resolveLXCTemplate returned error: %v", err)
	}
	if got == "" {
		t.Fatal("expected template")
	}
}

func TestBuildRequestUsesConfiguredStartID(t *testing.T) {
	profile := config.ProvisionProfile{
		Kind:    "lxc",
		IPMode:  "dhcp",
		StartID: 2000,
	}
	connection := config.Connection{}
	report := &ProbeReport{
		NextVMID: 100,
		UsedVMIDs: []int{
			2000,
			2001,
		},
	}

	request, err := BuildRequest(profile, connection, report, "demo", "auto", "", "", 0)
	if err != nil {
		t.Fatalf("BuildRequest returned error: %v", err)
	}
	if request.VMID != 2002 {
		t.Fatalf("unexpected vmid: %d", request.VMID)
	}
}

func TestBuildRequestRejectsUsedExplicitVMID(t *testing.T) {
	profile := config.ProvisionProfile{Kind: "lxc", IPMode: "dhcp"}
	connection := config.Connection{}
	report := &ProbeReport{UsedVMIDs: []int{1234}}

	_, err := BuildRequest(profile, connection, report, "demo", "1234", "", "", 0)
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("expected collision error, got %v", err)
	}
}

func TestParseGuestSummary(t *testing.T) {
	summary := parseGuestSummary(stringsJoin(
		"status=running",
		"os=Debian GNU/Linux 13 (trixie)",
		"hostname=testbox",
		"ipv4=192.168.1.44/24",
		"ipv4=192.168.1.44/24",
		"ipv6=fd7a:115c:a1e0::1/64",
		"docker=Docker version 29.3.1, build c2be9cc",
		"tailscale=1.96.4",
	))
	if summary.Status != "running" || summary.Hostname != "testbox" || summary.OS == "" {
		t.Fatalf("unexpected summary basics: %#v", summary)
	}
	if len(summary.IPv4) != 1 || summary.IPv4[0] != "192.168.1.44/24" {
		t.Fatalf("unexpected ipv4 summary: %#v", summary.IPv4)
	}
	if summary.DockerVersion == "" || summary.TailscaleVersion == "" {
		t.Fatalf("expected addon versions in summary: %#v", summary)
	}
}

func TestParseLXCInfo(t *testing.T) {
	info, kind := parseLXCInfo(stringsJoin(
		"kind=lxc",
		"hostname=destroy-me",
		"status=running",
	))
	if kind != "lxc" {
		t.Fatalf("unexpected kind: %s", kind)
	}
	if info.Hostname != "destroy-me" || info.Status != "running" {
		t.Fatalf("unexpected lxc info: %#v", info)
	}
}

func TestParseTailnetIdentity(t *testing.T) {
	identity := parseTailnetIdentity(stringsJoin(
		"addr=100.64.0.10",
		"addr=fd7a:115c:a1e0::10",
		"addr=100.64.0.10",
	))
	if len(identity.Addresses) != 2 {
		t.Fatalf("unexpected tailnet addresses: %#v", identity.Addresses)
	}
}

func TestListLXCsFromClusterResources(t *testing.T) {
	resources := `[{"type":"lxc","vmid":1000,"node":"fwd","status":"running","name":"boop","tags":"pprov"},{"type":"qemu","vmid":9000,"node":"fwd","status":"running","name":"ubuntu-template"},{"type":"lxc","vmid":1010,"node":"fwd","status":"stopped","name":"forgejo","tags":"pprov;docker"}]`
	var parsed []struct {
		Type   string `json:"type"`
		VMID   int    `json:"vmid"`
		Node   string `json:"node"`
		Status string `json:"status"`
		Name   string `json:"name"`
		Tags   string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(resources), &parsed); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	lxcs := []LXCInfo{}
	for _, resource := range parsed {
		if resource.Type != "lxc" {
			continue
		}
		lxcs = append(lxcs, LXCInfo{
			VMID:     resource.VMID,
			Node:     resource.Node,
			Hostname: resource.Name,
			Status:   resource.Status,
			Tags:     normalizeTagString(resource.Tags),
		})
	}
	if len(lxcs) != 2 {
		t.Fatalf("expected 2 lxcs, got %d", len(lxcs))
	}
	if lxcs[0].Hostname != "boop" || lxcs[1].Hostname != "forgejo" {
		t.Fatalf("unexpected lxc list: %#v", lxcs)
	}
	if lxcs[0].Tags != "pprov" || lxcs[1].Tags != "pprov;docker" {
		t.Fatalf("unexpected tag list: %#v", lxcs)
	}
}

func TestNormalizeTagString(t *testing.T) {
	t.Parallel()

	got := normalizeTagString("pprov, docker;pprov ; tailscale")
	if got != "pprov;docker;tailscale" {
		t.Fatalf("unexpected normalized tags: %q", got)
	}
}

func TestTailscaleUpScriptEmptyWithoutGuestLogin(t *testing.T) {
	t.Parallel()

	if got := tailscaleUpScript(Request{Hostname: "demo"}); got != "" {
		t.Fatalf("expected empty tailscale up script, got %q", got)
	}
}

func TestTailscaleUpScriptWaitsForRunningBackendState(t *testing.T) {
	t.Parallel()

	got := tailscaleUpScript(Request{
		Hostname: "demo",
		Tailscale: &TailscaleLogin{
			AuthKey: "tskey-auth-demo",
			SSH:     true,
		},
	})
	if !strings.Contains(got, `"BackendState":"Running"`) {
		t.Fatalf("tailscale up guard missing running state check: %s", got)
	}
	if strings.Contains(got, "tailscale status --json >/dev/null 2>&1") {
		t.Fatalf("tailscale up script still uses the old generic status guard: %s", got)
	}
	if !strings.Contains(got, "--ssh") {
		t.Fatalf("tailscale up script missing --ssh: %s", got)
	}
	if !strings.Contains(got, "--accept-risk=lose-ssh") {
		t.Fatalf("tailscale up script missing --accept-risk=lose-ssh: %s", got)
	}
}

func stringsJoin(values ...string) string {
	result := ""
	for _, value := range values {
		result += value + "\n"
	}
	return result
}
