package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ironicbadger/proxmox-provisioner/internal/addons"
	"github.com/ironicbadger/proxmox-provisioner/internal/config"
	"github.com/ironicbadger/proxmox-provisioner/internal/render"
	"github.com/ironicbadger/proxmox-provisioner/internal/sshclient"
)

const probeScript = `
printf 'node=%s\n' "$(hostname -s)"
printf 'nextid=%s\n' "$(pvesh get /cluster/nextid)"
ip -o link show type bridge | awk -F': ' '{print "bridge=" $2}'
awk '
function flush() {
  if (name != "") {
    printf "storage=%s|%s|%s|%s\n", name, backend, path, content
  }
}
/^[[:space:]]*#/ { next }
/^[[:space:]]*$/ { next }
$0 ~ /^[^[:space:]].*: / {
  flush()
  split($0, parts, ": ")
  backend = parts[1]
  name = parts[2]
  path = ""
  content = ""
  next
}
$1 == "path" { path = $2; next }
$1 == "content" { content = $2; next }
END { flush() }
' /etc/pve/storage.cfg
for storage in $(pvesm status | awk 'NR>1 {print $1}'); do
  pvesm list "$storage" --content vztmpl 2>/dev/null | awk 'NR>1 && NF {print "lxc_template=" $1}'
done | sort -u
shopt -s nullglob
for conf in /etc/pve/lxc/*.conf /etc/pve/nodes/*/qemu-server/*.conf; do
  printf 'used_id=%s\n' "$(basename "$conf" .conf)"
done | sort -u
for conf in /etc/pve/nodes/*/qemu-server/*.conf; do
  if grep -q '^template: 1$' "$conf"; then
    node=$(printf '%s\n' "$conf" | awk -F/ '{print $(NF-2)}')
    vmid=$(basename "$conf" .conf)
    name=$(awk -F': ' '$1 == "name" {print $2; exit}' "$conf")
    printf 'vm_template=%s|%s|%s\n' "$node" "$vmid" "${name:-template-$vmid}"
  fi
done | sort
`

type Proxmox struct {
	ssh     *sshclient.Client
	baseDir string
}

type Storage struct {
	Name    string
	Backend string
	Path    string
	Content []string
}

type VMTemplate struct {
	Label string
	Value string
}

type ProbeReport struct {
	Node         string
	NextVMID     int
	UsedVMIDs    []int
	Bridges      []string
	Storages     []Storage
	LXCTemplates []string
	VMTemplates  []VMTemplate
}

type Request struct {
	Name         string
	Hostname     string
	VMID         int
	RootPassword string
	Locale       string
	IPMode       string
	IPCIDR       string
	Gateway      string
	Tailscale    *TailscaleLogin
}

type TailscaleLogin struct {
	AuthKey string
	SSH     bool
}

type CreateResult struct {
	Template string
	Bridge   string
	Storage  string
	Tag      string
	Commands []string
	Summary  *GuestSummary
}

type GuestSummary struct {
	Node             string
	Status           string
	OS               string
	Hostname         string
	IPv4             []string
	IPv6             []string
	DockerVersion    string
	TailscaleVersion string
	RootPasswordSet  bool
	Warning          string
}

type TailnetIdentity struct {
	Addresses []string
}

type LXCInfo struct {
	VMID     int
	Node     string
	Hostname string
	Status   string
	Tags     string
}

type DestroyResult struct {
	VMID     int
	Hostname string
	Status   string
	Commands []string
}

type ProgressEvent struct {
	Stage    string
	Phase    string
	Index    int
	Total    int
	Stream   string
	Line     string
	Replace  bool
	Duration time.Duration
}

type ProgressFunc func(ProgressEvent)

func NewProxmox(ssh *sshclient.Client, baseDir string) *Proxmox {
	return &Proxmox{ssh: ssh, baseDir: baseDir}
}

func (p *Proxmox) Probe(ctx context.Context) (*ProbeReport, error) {
	result, err := p.ssh.RunScript(ctx, probeScript, false)
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, result.Stderr)
	}
	return parseProbe(result.Stdout)
}

func BuildRequest(
	profile config.ProvisionProfile,
	connection config.Connection,
	report *ProbeReport,
	hostname,
	vmidFlag,
	ipOverride,
	gatewayOverride string,
	startIDFlag int,
) (Request, error) {
	startID := startIDFlag
	if startID == 0 {
		startID = profile.StartID
	}
	if startID == 0 {
		startID = connection.Defaults.StartID
	}
	vmid, err := resolveVMID(vmidFlag, report, startID)
	if err != nil {
		return Request{}, err
	}
	ipMode := profile.IPMode
	if ipMode == "" {
		ipMode = "dhcp"
	}
	request := Request{
		Name:     hostname,
		Hostname: hostname,
		VMID:     vmid,
		IPMode:   ipMode,
		IPCIDR:   profile.DefaultIP,
		Gateway:  profile.DefaultGateway,
	}
	if ipOverride != "" {
		request.IPMode = "static"
		request.IPCIDR = ipOverride
	}
	if gatewayOverride != "" {
		request.Gateway = gatewayOverride
	}
	if request.IPMode == "static" && request.IPCIDR == "" {
		return Request{}, fmt.Errorf("static mode requires an IP/CIDR")
	}
	return request, nil
}

func (p *Proxmox) Create(
	ctx context.Context,
	connection config.Connection,
	profile config.ProvisionProfile,
	report *ProbeReport,
	request Request,
	dryRun bool,
	progress ProgressFunc,
) (*CreateResult, error) {
	switch profile.Kind {
	case "lxc":
		return p.createLXC(ctx, connection, profile, report, request, dryRun, progress)
	case "vm":
		return p.createVM(ctx, connection, profile, report, request, dryRun, progress)
	default:
		return nil, fmt.Errorf("unsupported profile kind %q", profile.Kind)
	}
}

func (p *Proxmox) InspectLXC(ctx context.Context, vmid int) (*LXCInfo, error) {
	script := fmt.Sprintf(`vmid=%d
config="/etc/pve/lxc/${vmid}.conf"
if [ -f "$config" ]; then
  printf 'kind=lxc\n'
  printf 'hostname=%%s\n' "$(awk -F': ' '$1 == "hostname" {print $2; exit}' "$config")"
  printf 'status=%%s\n' "$(pct status "$vmid" | awk '{print $2}')"
  exit 0
fi
if ls /etc/pve/nodes/*/qemu-server/${vmid}.conf >/dev/null 2>&1; then
  printf 'kind=vm\n'
  exit 0
fi
printf 'kind=missing\n'
`, vmid)
	result, err := p.ssh.RunScript(ctx, script, false)
	if err != nil {
		return nil, err
	}
	info, kind := parseLXCInfo(result.Stdout)
	switch kind {
	case "lxc":
		info.VMID = vmid
		return info, nil
	case "vm":
		return nil, fmt.Errorf("vmid %d belongs to a VM, not an LXC", vmid)
	default:
		return nil, fmt.Errorf("no LXC exists with vmid %d", vmid)
	}
}

func (p *Proxmox) ListLXCs(ctx context.Context) ([]LXCInfo, error) {
	result, err := p.ssh.RunScript(ctx, "pvesh get /cluster/resources --type vm --output-format json\n", false)
	if err != nil {
		return nil, err
	}

	var resources []struct {
		Type   string `json:"type"`
		VMID   int    `json:"vmid"`
		Node   string `json:"node"`
		Status string `json:"status"`
		Name   string `json:"name"`
		Tags   string `json:"tags"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &resources); err != nil {
		return nil, err
	}

	lxcs := []LXCInfo{}
	for _, resource := range resources {
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
	sort.Slice(lxcs, func(i, j int) bool {
		return lxcs[i].VMID < lxcs[j].VMID
	})
	return lxcs, nil
}

func (p *Proxmox) InspectLXCTailnet(ctx context.Context, vmid int) (*TailnetIdentity, error) {
	script := fmt.Sprintf(`if pct status %[1]d | grep -q running; then
  pct exec %[1]d -- sh -lc '
if command -v tailscale >/dev/null 2>&1; then
  tailscale ip -4 2>/dev/null | awk "NF {print \"addr=\" \$1}"
  tailscale ip -6 2>/dev/null | awk "NF {print \"addr=\" \$1}"
fi
' 2>/dev/null || true
fi
`, vmid)
	result, err := p.ssh.RunScript(ctx, script, false)
	if err != nil {
		return nil, err
	}
	return parseTailnetIdentity(result.Stdout), nil
}

func (p *Proxmox) DestroyLXC(
	ctx context.Context,
	info *LXCInfo,
	dryRun bool,
	progress ProgressFunc,
) (*DestroyResult, error) {
	totalPhases := 1
	if info.Status == "running" {
		totalPhases++
	}
	phaseIndex := 0
	commands := []string{}

	if info.Status == "running" {
		stopCmd := []string{"pct", "stop", strconv.Itoa(info.VMID)}
		phaseIndex++
		if _, err := p.runPhase(ctx, "pct stop", shellScript(joinShell(stopCmd)), dryRun, progress, phaseIndex, totalPhases); err != nil {
			return nil, err
		}
		commands = append(commands, joinShell(stopCmd))
	}

	destroyCmd := []string{"pct", "destroy", strconv.Itoa(info.VMID), "--purge", "1"}
	phaseIndex++
	if _, err := p.runPhase(ctx, "pct destroy", shellScript(joinShell(destroyCmd)), dryRun, progress, phaseIndex, totalPhases); err != nil {
		return nil, err
	}
	commands = append(commands, joinShell(destroyCmd))

	return &DestroyResult{
		VMID:     info.VMID,
		Hostname: info.Hostname,
		Status:   info.Status,
		Commands: commands,
	}, nil
}

func (r *ProbeReport) DefaultBridge() string {
	for _, bridge := range r.Bridges {
		if bridge == "vmbr0" {
			return bridge
		}
	}
	if len(r.Bridges) > 0 {
		return r.Bridges[0]
	}
	return "vmbr0"
}

func (r *ProbeReport) DefaultLXCStorage() string {
	return r.pickStorage("rootdir", []string{"local-lvm", "local-zfs", "local"})
}

func (r *ProbeReport) DefaultVMStorage() string {
	return r.pickStorage("images", []string{"local-lvm", "local-zfs", "local"})
}

func (r *ProbeReport) DefaultSnippetStorage() string {
	return r.pickStorage("snippets", []string{"local"})
}

func (r *ProbeReport) DefaultSnippetDir() string {
	name := r.DefaultSnippetStorage()
	for _, storage := range r.Storages {
		if storage.Name == name && storage.Path != "" {
			return path.Join(storage.Path, "snippets")
		}
	}
	return ""
}

func (r *ProbeReport) IsVMIDUsed(vmid int) bool {
	for _, used := range r.UsedVMIDs {
		if used == vmid {
			return true
		}
	}
	return false
}

func (r *ProbeReport) NextAvailableVMID(startID int) (int, error) {
	candidate := startID
	if candidate <= 0 {
		candidate = r.NextVMID
	}
	if candidate <= 0 {
		return 0, fmt.Errorf("no start_id was configured and next vmid was not available from the probe")
	}
	for r.IsVMIDUsed(candidate) {
		candidate++
	}
	return candidate, nil
}

func (r *ProbeReport) pickStorage(contentType string, preferred []string) string {
	candidates := []Storage{}
	for _, storage := range r.Storages {
		for _, content := range storage.Content {
			if content == contentType {
				candidates = append(candidates, storage)
				break
			}
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	for _, want := range preferred {
		for _, storage := range candidates {
			if storage.Name == want {
				return storage.Name
			}
		}
	}
	return candidates[0].Name
}

func (p *Proxmox) createLXC(
	ctx context.Context,
	connection config.Connection,
	profile config.ProvisionProfile,
	report *ProbeReport,
	request Request,
	dryRun bool,
	progress ProgressFunc,
) (*CreateResult, error) {
	template, err := resolveLXCTemplate(profile, report)
	if err != nil {
		return nil, err
	}
	bridge := firstNonEmpty(profile.Bridge, connection.Defaults.Bridge, report.DefaultBridge())
	storage := firstNonEmpty(profile.Storage, connection.Defaults.LXCStorage, report.DefaultLXCStorage())

	createCmd := []string{
		"pct", "create", strconv.Itoa(request.VMID), template,
		"--hostname", request.Hostname,
		"--unprivileged", boolString(profile.Unprivileged, true),
		"--net0", buildLXCNet0(bridge, profile.VLAN, request),
	}
	displayCreateCmd := append([]string{}, createCmd...)
	if tag := normalizeTagString(profile.Tag); tag != "" {
		createCmd = append(createCmd, "--tags", tag)
		displayCreateCmd = append(displayCreateCmd, "--tags", tag)
	}
	if request.RootPassword != "" {
		createCmd = append(createCmd, "--password", request.RootPassword)
		displayCreateCmd = append(displayCreateCmd, "--password", "<hidden>")
	}
	if profile.RootFSGiB > 0 && storage != "" {
		createCmd = append(createCmd, "--rootfs", fmt.Sprintf("%s:%d", storage, profile.RootFSGiB))
		displayCreateCmd = append(displayCreateCmd, "--rootfs", fmt.Sprintf("%s:%d", storage, profile.RootFSGiB))
	} else if storage != "" {
		createCmd = append(createCmd, "--storage", storage)
		displayCreateCmd = append(displayCreateCmd, "--storage", storage)
	}
	if profile.Cores > 0 {
		createCmd = append(createCmd, "--cores", strconv.Itoa(profile.Cores))
		displayCreateCmd = append(displayCreateCmd, "--cores", strconv.Itoa(profile.Cores))
	}
	if profile.Memory > 0 {
		createCmd = append(createCmd, "--memory", strconv.Itoa(profile.Memory))
		displayCreateCmd = append(displayCreateCmd, "--memory", strconv.Itoa(profile.Memory))
	}
	if profile.Swap > 0 {
		createCmd = append(createCmd, "--swap", strconv.Itoa(profile.Swap))
		displayCreateCmd = append(displayCreateCmd, "--swap", strconv.Itoa(profile.Swap))
	}
	if value := boolString(profile.Onboot, true); value == "1" {
		createCmd = append(createCmd, "--onboot", value)
		displayCreateCmd = append(displayCreateCmd, "--onboot", value)
	}
	features := append([]string{}, profile.Features...)
	if len(features) > 0 {
		createCmd = append(createCmd, "--features", strings.Join(features, ","))
		displayCreateCmd = append(displayCreateCmd, "--features", strings.Join(features, ","))
	}
	appendExtraArgs(&createCmd, profile.ExtraCreateArgs)
	appendExtraArgs(&displayCreateCmd, profile.ExtraCreateArgs)

	lxcConfigLines := render.LXCConfigLines(profile.Addons)
	totalPhases := 1
	if len(lxcConfigLines) > 0 {
		totalPhases++
	}
	if boolString(profile.StartAfterCreate, true) == "1" {
		totalPhases++
	}

	ctxMap := render.Context{
		"profile":    request.Name,
		"guest_name": request.Name,
		"hostname":   request.Hostname,
		"vmid":       strconv.Itoa(request.VMID),
		"ip_cidr":    request.IPCIDR,
		"gateway":    request.Gateway,
	}
	renderedSnippet, err := render.File(pathFor(p.baseDir, profile.ShellSnippet), ctxMap)
	if err != nil {
		return nil, err
	}
	guestSteps := lxcGuestSteps(profile, renderedSnippet, request)
	if len(guestSteps) > 0 {
		totalPhases++
		totalPhases += len(guestSteps)
	}

	phaseIndex := 0
	commands := []string{}
	phaseIndex++
	if _, err := p.runPhase(ctx, "pct create", shellScript(joinShell(createCmd)), dryRun, progress, phaseIndex, totalPhases); err != nil {
		return nil, err
	}
	commands = append(commands, joinShell(displayCreateCmd))

	if len(lxcConfigLines) > 0 {
		script := fmt.Sprintf("config_file=/etc/pve/lxc/%d.conf\nwhile IFS= read -r line; do\n  grep -Fx -- \"$line\" \"$config_file\" >/dev/null || printf '%%s\\n' \"$line\" >> \"$config_file\"\ndone <<'EOF'\n%s\nEOF\n", request.VMID, strings.Join(lxcConfigLines, "\n"))
		phaseIndex++
		if _, err := p.runPhase(ctx, "append lxc config", script, dryRun, progress, phaseIndex, totalPhases); err != nil {
			return nil, err
		}
		commands = append(commands, "append lxc config lines")
	}

	if boolString(profile.StartAfterCreate, true) == "1" {
		startCmd := []string{"pct", "start", strconv.Itoa(request.VMID)}
		phaseIndex++
		if _, err := p.runPhase(ctx, "pct start", shellScript(joinShell(startCmd)), dryRun, progress, phaseIndex, totalPhases); err != nil {
			return nil, err
		}
		commands = append(commands, joinShell(startCmd))
	}

	if len(guestSteps) > 0 {
		waitScript := fmt.Sprintf("attempt=0\nwhile ! pct exec %d -- true >/dev/null 2>&1; do\n  attempt=$((attempt + 1))\n  if [ \"$attempt\" -ge 30 ]; then\n    echo 'container guest exec did not become ready in time' >&2\n    exit 1\n  fi\n  sleep 2\ndone\n", request.VMID)
		phaseIndex++
		if _, err := p.runPhase(ctx, "wait for guest exec", waitScript, dryRun, progress, phaseIndex, totalPhases); err != nil {
			return nil, err
		}
		commands = append(commands, fmt.Sprintf("wait for pct exec %d", request.VMID))
		for _, step := range guestSteps {
			script := fmt.Sprintf("pct exec %d -- bash -se <<'EOF'\n%sEOF\n", request.VMID, step.Script)
			phaseIndex++
			if _, err := p.runPhase(ctx, step.Name, script, dryRun, progress, phaseIndex, totalPhases); err != nil {
				return nil, err
			}
			commands = append(commands, fmt.Sprintf("pct exec %d -- bash -se < %s>", request.VMID, step.Name))
		}
	}

	var summary *GuestSummary
	if !dryRun {
		summary, err = p.inspectLXC(ctx, report.Node, request)
		if err != nil {
			summary = &GuestSummary{
				Node:            report.Node,
				Status:          "unknown",
				Hostname:        request.Hostname,
				RootPasswordSet: request.RootPassword != "",
				Warning:         err.Error(),
			}
		}
	}

	return &CreateResult{
		Template: template,
		Bridge:   bridge,
		Storage:  storage,
		Tag:      normalizeTagString(profile.Tag),
		Commands: commands,
		Summary:  summary,
	}, nil
}

func (p *Proxmox) createVM(
	ctx context.Context,
	connection config.Connection,
	profile config.ProvisionProfile,
	report *ProbeReport,
	request Request,
	dryRun bool,
	progress ProgressFunc,
) (*CreateResult, error) {
	template, err := resolveVMTemplate(profile, report)
	if err != nil {
		return nil, err
	}
	bridge := firstNonEmpty(profile.Bridge, connection.Defaults.Bridge, report.DefaultBridge())
	storage := firstNonEmpty(profile.Storage, connection.Defaults.VMStorage, report.DefaultVMStorage())
	totalPhases := 2
	if profile.DiskGiB > 0 {
		totalPhases++
	}
	if boolString(profile.StartAfterCreate, true) == "1" {
		totalPhases++
	}
	phaseIndex := 0

	cloneCmd := []string{"qm", "clone", template, strconv.Itoa(request.VMID), "--name", request.Hostname, "--full", boolString(profile.FullClone, true)}
	if storage != "" {
		cloneCmd = append(cloneCmd, "--storage", storage)
	}
	phaseIndex++
	if _, err := p.runPhase(ctx, "qm clone", shellScript(joinShell(cloneCmd)), dryRun, progress, phaseIndex, totalPhases); err != nil {
		return nil, err
	}
	commands := []string{joinShell(cloneCmd)}

	ctxMap := render.Context{
		"profile":    request.Name,
		"guest_name": request.Name,
		"hostname":   request.Hostname,
		"vmid":       strconv.Itoa(request.VMID),
		"ip_cidr":    request.IPCIDR,
		"gateway":    request.Gateway,
	}
	renderedBase, err := render.File(pathFor(p.baseDir, profile.CloudInitSnippet), ctxMap)
	if err != nil {
		return nil, err
	}
	guestScript := render.GuestScript(profile.Updated, request.Locale, profile.Addons, tailscaleUpScript(request))
	cloudInit, err := render.CloudInit(renderedBase, guestScript)
	if err != nil {
		return nil, err
	}
	cicustom := ""
	if cloudInit != "" {
		snippetStorage := firstNonEmpty(connection.Defaults.SnippetStorage, report.DefaultSnippetStorage())
		snippetDir := firstNonEmpty(connection.Defaults.SnippetDir, report.DefaultSnippetDir())
		if snippetStorage == "" || snippetDir == "" {
			return nil, fmt.Errorf("vm profile requires snippet storage for cloud-init, but none was detected")
		}
		filename := fmt.Sprintf("%s-%d-user-data.yaml", slugify(request.Name), request.VMID)
		remotePath := path.Join(snippetDir, filename)
		command, err := p.ssh.UploadText(ctx, remotePath, cloudInit, dryRun)
		if err != nil {
			return nil, err
		}
		commands = append(commands, command)
		cicustom = fmt.Sprintf("user=%s:snippets/%s", snippetStorage, filename)
	}

	setCmd := []string{"qm", "set", strconv.Itoa(request.VMID)}
	if profile.Cores > 0 {
		setCmd = append(setCmd, "--cores", strconv.Itoa(profile.Cores))
	}
	if profile.Memory > 0 {
		setCmd = append(setCmd, "--memory", strconv.Itoa(profile.Memory))
	}
	if boolString(profile.Onboot, true) == "1" {
		setCmd = append(setCmd, "--onboot", "1")
	}
	netModel := profile.VMNetModel
	if netModel == "" {
		netModel = "virtio"
	}
	setCmd = append(setCmd, "--net0", buildVMNet0(netModel, bridge, profile.VLAN))
	setCmd = append(setCmd, "--ipconfig0", buildVMIPConfig(request))
	if cicustom != "" {
		setCmd = append(setCmd, "--cicustom", cicustom)
	}
	appendExtraArgs(&setCmd, profile.ExtraSetArgs)
	phaseIndex++
	if _, err := p.runPhase(ctx, "qm set", shellScript(joinShell(setCmd)), dryRun, progress, phaseIndex, totalPhases); err != nil {
		return nil, err
	}
	commands = append(commands, joinShell(setCmd))

	if profile.DiskGiB > 0 {
		device := profile.VMDiskDevice
		if device == "" {
			device = "scsi0"
		}
		resizeCmd := []string{"qm", "resize", strconv.Itoa(request.VMID), device, fmt.Sprintf("%dG", profile.DiskGiB)}
		phaseIndex++
		if _, err := p.runPhase(ctx, "qm resize", shellScript(joinShell(resizeCmd)), dryRun, progress, phaseIndex, totalPhases); err != nil {
			return nil, err
		}
		commands = append(commands, joinShell(resizeCmd))
	}

	if boolString(profile.StartAfterCreate, true) == "1" {
		startCmd := []string{"qm", "start", strconv.Itoa(request.VMID)}
		phaseIndex++
		if _, err := p.runPhase(ctx, "qm start", shellScript(joinShell(startCmd)), dryRun, progress, phaseIndex, totalPhases); err != nil {
			return nil, err
		}
		commands = append(commands, joinShell(startCmd))
	}

	return &CreateResult{
		Template: template,
		Bridge:   bridge,
		Storage:  storage,
		Commands: commands,
	}, nil
}

func normalizeTagString(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	seen := map[string]struct{}{}
	tags := make([]string, 0, len(parts))
	for _, part := range parts {
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
	return strings.Join(tags, ";")
}

func parseProbe(stdout string) (*ProbeReport, error) {
	report := &ProbeReport{}
	for _, raw := range strings.Split(stdout, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "node="):
			report.Node = strings.TrimPrefix(line, "node=")
		case strings.HasPrefix(line, "nextid="):
			value := strings.TrimPrefix(line, "nextid=")
			vmid, err := strconv.Atoi(value)
			if err != nil {
				return nil, err
			}
			report.NextVMID = vmid
		case strings.HasPrefix(line, "bridge="):
			report.Bridges = appendUnique(report.Bridges, strings.TrimPrefix(line, "bridge="))
		case strings.HasPrefix(line, "used_id="):
			value := strings.TrimPrefix(line, "used_id=")
			vmid, err := strconv.Atoi(value)
			if err != nil {
				return nil, err
			}
			report.UsedVMIDs = appendIntUnique(report.UsedVMIDs, vmid)
		case strings.HasPrefix(line, "storage="):
			payload := strings.TrimPrefix(line, "storage=")
			parts := strings.SplitN(payload, "|", 4)
			if len(parts) != 4 {
				return nil, fmt.Errorf("invalid storage line %q", line)
			}
			content := []string{}
			if parts[3] != "" {
				content = strings.Split(parts[3], ",")
			}
			report.Storages = append(report.Storages, Storage{
				Name:    parts[0],
				Backend: parts[1],
				Path:    parts[2],
				Content: content,
			})
		case strings.HasPrefix(line, "lxc_template="):
			report.LXCTemplates = appendUnique(report.LXCTemplates, strings.TrimPrefix(line, "lxc_template="))
		case strings.HasPrefix(line, "vm_template="):
			payload := strings.TrimPrefix(line, "vm_template=")
			parts := strings.SplitN(payload, "|", 3)
			if len(parts) != 3 {
				return nil, fmt.Errorf("invalid vm template line %q", line)
			}
			report.VMTemplates = append(report.VMTemplates, VMTemplate{
				Label: fmt.Sprintf("%s (%s on %s)", parts[2], parts[1], parts[0]),
				Value: parts[1],
			})
		}
	}
	sort.Strings(report.Bridges)
	sort.Strings(report.LXCTemplates)
	sort.Ints(report.UsedVMIDs)
	return report, nil
}

func resolveVMID(value string, report *ProbeReport, startID int) (int, error) {
	if value == "" || value == "auto" {
		return report.NextAvailableVMID(startID)
	}
	vmid, err := strconv.Atoi(value)
	if err != nil {
		return 0, err
	}
	if report.IsVMIDUsed(vmid) {
		return 0, fmt.Errorf("vmid %d is already in use", vmid)
	}
	return vmid, nil
}

func resolveLXCTemplate(profile config.ProvisionProfile, report *ProbeReport) (string, error) {
	if profile.Template != "" {
		return profile.Template, nil
	}
	if profile.TemplateMatch == "" && len(report.LXCTemplates) == 1 {
		return report.LXCTemplates[0], nil
	}
	for _, template := range report.LXCTemplates {
		if strings.Contains(strings.ToLower(template), strings.ToLower(profile.TemplateMatch)) {
			return template, nil
		}
	}
	if len(report.LXCTemplates) == 1 {
		return report.LXCTemplates[0], nil
	}
	return "", fmt.Errorf("no lxc template matched %q; available: %s", profile.TemplateMatch, strings.Join(report.LXCTemplates, ", "))
}

func resolveVMTemplate(profile config.ProvisionProfile, report *ProbeReport) (string, error) {
	if profile.Template != "" {
		return profile.Template, nil
	}
	if profile.TemplateMatch == "" && len(report.VMTemplates) == 1 {
		return report.VMTemplates[0].Value, nil
	}
	for _, template := range report.VMTemplates {
		if strings.Contains(strings.ToLower(template.Label), strings.ToLower(profile.TemplateMatch)) {
			return template.Value, nil
		}
	}
	if len(report.VMTemplates) == 1 {
		return report.VMTemplates[0].Value, nil
	}
	labels := make([]string, 0, len(report.VMTemplates))
	for _, template := range report.VMTemplates {
		labels = append(labels, template.Label)
	}
	return "", fmt.Errorf("no vm template matched %q; available: %s", profile.TemplateMatch, strings.Join(labels, ", "))
}

func buildLXCNet0(bridge string, vlan int, request Request) string {
	parts := []string{"name=eth0", "bridge=" + bridge, "type=veth"}
	if vlan > 0 {
		parts = append(parts, fmt.Sprintf("tag=%d", vlan))
	}
	if request.IPMode == "static" {
		parts = append(parts, "ip="+request.IPCIDR)
		if request.Gateway != "" {
			parts = append(parts, "gw="+request.Gateway)
		}
	} else {
		parts = append(parts, "ip=dhcp")
	}
	return strings.Join(parts, ",")
}

func buildVMNet0(model, bridge string, vlan int) string {
	parts := []string{model, "bridge=" + bridge}
	if vlan > 0 {
		parts = append(parts, fmt.Sprintf("tag=%d", vlan))
	}
	return strings.Join(parts, ",")
}

func buildVMIPConfig(request Request) string {
	if request.IPMode == "static" {
		if request.Gateway != "" {
			return "ip=" + request.IPCIDR + ",gw=" + request.Gateway
		}
		return "ip=" + request.IPCIDR
	}
	return "ip=dhcp"
}

func boolString(value *bool, defaultValue bool) string {
	if value == nil {
		if defaultValue {
			return "1"
		}
		return "0"
	}
	if *value {
		return "1"
	}
	return "0"
}

func appendExtraArgs(cmd *[]string, extra map[string]string) {
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		*cmd = append(*cmd, "--"+key, extra[key])
	}
}

func joinShell(parts []string) string {
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		quoted = append(quoted, shellQuote(part))
	}
	return strings.Join(quoted, " ")
}

func shellScript(command string) string {
	return command + "\n"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendIntUnique(values []int, value int) []int {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

type lxcGuestStep struct {
	Name   string
	Script string
}

func lxcGuestSteps(profile config.ProvisionProfile, renderedSnippet string, request Request) []lxcGuestStep {
	steps := []lxcGuestStep{}
	if profile.Updated {
		steps = append(steps, lxcGuestStep{
			Name:   "base update",
			Script: render.GuestScript(true, request.Locale, nil, ""),
		})
	} else if strings.TrimSpace(request.Locale) != "" {
		steps = append(steps, lxcGuestStep{
			Name:   "guest locale",
			Script: render.GuestScript(false, request.Locale, nil, ""),
		})
	}
	for _, key := range profile.Addons {
		builtin, ok := addons.Builtins[key]
		if !ok || strings.TrimSpace(builtin.GuestScript) == "" {
			continue
		}
		steps = append(steps, lxcGuestStep{
			Name:   "addon " + key,
			Script: render.GuestScript(false, "", nil, builtin.GuestScript),
		})
		if key == "tailscale" {
			if script := tailscaleUpScript(request); strings.TrimSpace(script) != "" {
				steps = append(steps, lxcGuestStep{
					Name:   "tailscale up",
					Script: render.GuestScript(false, "", nil, script),
				})
			}
		}
	}
	if strings.TrimSpace(renderedSnippet) != "" {
		steps = append(steps, lxcGuestStep{
			Name:   "shell snippet",
			Script: render.GuestScript(false, "", nil, renderedSnippet),
		})
	}
	return steps
}

func tailscaleUpScript(request Request) string {
	if request.Tailscale == nil || strings.TrimSpace(request.Tailscale.AuthKey) == "" {
		return ""
	}

	args := []string{
		"tailscale", "up",
		"--auth-key", request.Tailscale.AuthKey,
		"--hostname", request.Hostname,
	}
	if request.Tailscale.SSH {
		args = append(args, "--ssh", "--accept-risk=lose-ssh")
	}

	return fmt.Sprintf(`if command -v systemctl >/dev/null 2>&1; then
  systemctl enable --now tailscaled >/dev/null 2>&1 || systemctl start tailscaled >/dev/null 2>&1 || true
elif command -v service >/dev/null 2>&1; then
  service tailscaled start >/dev/null 2>&1 || true
fi

if tailscale status --json 2>/dev/null | grep -q '"BackendState":"Running"'; then
  exit 0
fi

%s
`, joinShell(args))
}

func (p *Proxmox) runPhase(
	ctx context.Context,
	phase,
	script string,
	dryRun bool,
	progress ProgressFunc,
	index,
	total int,
) (sshclient.Result, error) {
	startedAt := time.Now()
	if progress != nil {
		progress(ProgressEvent{
			Stage: "start",
			Phase: phase,
			Index: index,
			Total: total,
		})
	}
	result, err := p.ssh.RunScriptStream(ctx, script, dryRun, sshclient.StreamCallbacks{
		Stdout: func(line sshclient.StreamLine) {
			if progress != nil && strings.TrimSpace(line.Text) != "" {
				progress(ProgressEvent{
					Stage:   "output",
					Phase:   phase,
					Index:   index,
					Total:   total,
					Stream:  "stdout",
					Line:    line.Text,
					Replace: line.Replace,
				})
			}
		},
		Stderr: func(line sshclient.StreamLine) {
			if progress != nil && strings.TrimSpace(line.Text) != "" {
				progress(ProgressEvent{
					Stage:   "output",
					Phase:   phase,
					Index:   index,
					Total:   total,
					Stream:  "stderr",
					Line:    line.Text,
					Replace: line.Replace,
				})
			}
		},
	})
	if err == nil {
		if progress != nil {
			progress(ProgressEvent{
				Stage:    "done",
				Phase:    phase,
				Index:    index,
				Total:    total,
				Duration: time.Since(startedAt),
			})
		}
		return result, nil
	}
	if progress != nil {
		progress(ProgressEvent{
			Stage:    "error",
			Phase:    phase,
			Index:    index,
			Total:    total,
			Duration: time.Since(startedAt),
		})
	}
	parts := []string{fmt.Sprintf("%s failed: %v", phase, err)}
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	if stdout := strings.TrimSpace(result.Stdout); stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	return result, errors.New(strings.Join(parts, "\n"))
}

func (p *Proxmox) inspectLXC(ctx context.Context, node string, request Request) (*GuestSummary, error) {
	script := fmt.Sprintf(`printf 'status=%%s\n' "$(pct status %d | awk '{print $2}')"
pct exec %d -- bash -se <<'EOF'
if [ -r /etc/os-release ]; then
  . /etc/os-release
  if [ -n "${PRETTY_NAME:-}" ]; then
    printf 'os=%%s\n' "$PRETTY_NAME"
  fi
fi
printf 'hostname=%%s\n' "$(hostname)"
ip -o -4 addr show scope global 2>/dev/null | awk '$2 !~ /^(docker[0-9]*|br-|veth|tailscale[0-9]*|lo)$/ {print "ipv4=" $4}'
ip -o -6 addr show scope global 2>/dev/null | awk '$2 !~ /^(docker[0-9]*|br-|veth|tailscale[0-9]*|lo)$/ {print "ipv6=" $4}'
if command -v docker >/dev/null 2>&1; then
  printf 'docker=%%s\n' "$(docker --version 2>/dev/null)"
fi
if command -v tailscale >/dev/null 2>&1; then
  printf 'tailscale=%%s\n' "$(tailscale version 2>/dev/null | head -n 1)"
fi
EOF
`, request.VMID, request.VMID)
	result, err := p.ssh.RunScript(ctx, script, false)
	if err != nil {
		return nil, err
	}
	summary := parseGuestSummary(result.Stdout)
	summary.Node = node
	summary.RootPasswordSet = request.RootPassword != ""
	return summary, nil
}

func parseGuestSummary(stdout string) *GuestSummary {
	summary := &GuestSummary{}
	for _, raw := range strings.Split(stdout, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "status="):
			summary.Status = strings.TrimPrefix(line, "status=")
		case strings.HasPrefix(line, "os="):
			summary.OS = strings.TrimPrefix(line, "os=")
		case strings.HasPrefix(line, "hostname="):
			summary.Hostname = strings.TrimPrefix(line, "hostname=")
		case strings.HasPrefix(line, "ipv4="):
			summary.IPv4 = appendUnique(summary.IPv4, strings.TrimPrefix(line, "ipv4="))
		case strings.HasPrefix(line, "ipv6="):
			summary.IPv6 = appendUnique(summary.IPv6, strings.TrimPrefix(line, "ipv6="))
		case strings.HasPrefix(line, "docker="):
			summary.DockerVersion = strings.TrimPrefix(line, "docker=")
		case strings.HasPrefix(line, "tailscale="):
			summary.TailscaleVersion = strings.TrimPrefix(line, "tailscale=")
		}
	}
	return summary
}

func parseTailnetIdentity(stdout string) *TailnetIdentity {
	identity := &TailnetIdentity{}
	for _, raw := range strings.Split(stdout, "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "addr=") {
			continue
		}
		identity.Addresses = appendUnique(identity.Addresses, strings.TrimPrefix(line, "addr="))
	}
	return identity
}

func parseLXCInfo(stdout string) (*LXCInfo, string) {
	info := &LXCInfo{}
	kind := ""
	for _, raw := range strings.Split(stdout, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "kind="):
			kind = strings.TrimPrefix(line, "kind=")
		case strings.HasPrefix(line, "hostname="):
			info.Hostname = strings.TrimPrefix(line, "hostname=")
		case strings.HasPrefix(line, "status="):
			info.Status = strings.TrimPrefix(line, "status=")
		}
	}
	return info, kind
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, "_", "-")
	value = strings.ReplaceAll(value, "/", "-")
	return strings.Trim(value, "-")
}

func pathFor(baseDir, relative string) string {
	if relative == "" {
		return ""
	}
	if strings.HasPrefix(relative, "/") {
		return relative
	}
	return path.Clean(path.Join(strings.ReplaceAll(baseDir, "\\", "/"), relative))
}
