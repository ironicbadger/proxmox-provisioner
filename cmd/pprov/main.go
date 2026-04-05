package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ironicbadger/proxmox-provisioner/internal/config"
	"github.com/ironicbadger/proxmox-provisioner/internal/provision"
	"github.com/ironicbadger/proxmox-provisioner/internal/sshclient"
	"github.com/ironicbadger/proxmox-provisioner/internal/tailnet"
	"golang.org/x/term"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}

	switch args[0] {
	case "init":
		return runInit(args[1:])
	case "probe":
		return runProbe(args[1:])
	case "create":
		return runCreate(args[1:])
	case "list":
		return runList(args[1:])
	case "destroy":
		return runDestroy(args[1:])
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		return usageError()
	}
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	var writePath string
	fs.StringVar(&writePath, "write", "", "write the example config to this path")
	if err := fs.Parse(args); err != nil {
		return err
	}

	example := config.ExampleConfig()
	if writePath == "" {
		fmt.Print(example)
		return nil
	}
	path, err := filepath.Abs(writePath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(example), 0o644); err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func runProbe(args []string) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "path to config file")
	timeout := fs.Duration("timeout", 45*time.Second, "probe timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: pprov probe [--config path] <connection>")
	}
	connectionName := fs.Arg(0)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	connection, err := cfg.Connection(connectionName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	dialer, err := tailnet.NewDialer(cfg.Tailnet)
	if err != nil {
		return err
	}
	defer dialer.Close()

	sshConn, err := sshclient.New(connection.SSH, dialer)
	if err != nil {
		return err
	}
	defer sshConn.Close()

	pve := provision.NewProxmox(sshConn, cfg.BaseDir)
	report, err := pve.Probe(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("connection: %s\n", connectionName)
	fmt.Printf("ssh target: %s\n", connection.SSH.Target)
	fmt.Printf("node: %s\n", report.Node)
	fmt.Printf("next vmid: %d\n", report.NextVMID)
	fmt.Printf("bridge: %s\n", report.DefaultBridge())
	fmt.Printf("lxc storage: %s\n", valueOrUnset(report.DefaultLXCStorage()))
	fmt.Printf("vm storage: %s\n", valueOrUnset(report.DefaultVMStorage()))
	fmt.Printf("snippet storage: %s\n", valueOrUnset(report.DefaultSnippetStorage()))
	fmt.Printf("snippet dir: %s\n", valueOrUnset(report.DefaultSnippetDir()))
	fmt.Println("")
	fmt.Println("lxc templates:")
	if len(report.LXCTemplates) == 0 {
		fmt.Println("  - none")
	} else {
		for _, item := range report.LXCTemplates {
			fmt.Printf("  - %s\n", item)
		}
	}
	fmt.Println("")
	fmt.Println("vm templates:")
	if len(report.VMTemplates) == 0 {
		fmt.Println("  - none")
	} else {
		for _, item := range report.VMTemplates {
			fmt.Printf("  - %s (%s)\n", item.Label, item.Value)
		}
	}
	return nil
}

func runCreate(args []string) error {
	args, err := normalizeFlagArgs(args, map[string]bool{
		"--config":   false,
		"--timeout":  false,
		"--vmid":     false,
		"--start-id": false,
		"--ip":       false,
		"--gateway":  false,
		"--dry-run":  true,
		"--tslogin":  true,
	})
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "path to config file")
	timeout := fs.Duration("timeout", 2*time.Minute, "operation timeout")
	vmidFlag := fs.String("vmid", "auto", "vmid or auto")
	startIDFlag := fs.Int("start-id", 0, "first cluster VMID to consider when --vmid=auto")
	ipFlag := fs.String("ip", "", "static IP/CIDR override")
	gatewayFlag := fs.String("gateway", "", "gateway override")
	dryRun := fs.Bool("dry-run", false, "print remote commands without executing them")
	tsLogin := fs.Bool("tslogin", false, "after installing tailscale in the guest, mint an auth key via tailnet.oauth and run tailscale up --ssh")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return fmt.Errorf("usage: pprov create [flags] <profile> <hostname>")
	}
	profileName := fs.Arg(0)
	hostname := fs.Arg(1)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	profile, err := cfg.Profile(profileName)
	if err != nil {
		return err
	}
	connection, err := cfg.Connection(profile.Connection)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	dialer, err := tailnet.NewDialer(cfg.Tailnet)
	if err != nil {
		return err
	}
	defer dialer.Close()

	sshConn, err := sshclient.New(connection.SSH, dialer)
	if err != nil {
		return err
	}
	defer sshConn.Close()

	pve := provision.NewProxmox(sshConn, cfg.BaseDir)
	fmt.Fprintf(os.Stderr, "==> probing %s via %s\n", profile.Connection, connection.SSH.Target)
	report, err := pve.Probe(ctx)
	if err != nil {
		return err
	}

	request, err := provision.BuildRequest(profile, connection, report, hostname, *vmidFlag, *ipFlag, *gatewayFlag, *startIDFlag)
	if err != nil {
		return err
	}
	request.Locale = resolveGuestLocale(profile)
	fmt.Fprintf(os.Stderr, "==> resolved node=%s vmid=%d kind=%s\n", report.Node, request.VMID, profile.Kind)
	rootPassword, err := resolveLXCRootPassword(profile, *dryRun)
	if err != nil {
		return err
	}
	request.RootPassword = rootPassword
	if *tsLogin {
		if !profileHasAddon(profile, "tailscale") {
			return errors.New("--tslogin requires the profile to include the tailscale add-on")
		}
		login, err := tailnet.PrepareGuestLogin(ctx, cfg.Tailnet, *dryRun)
		if err != nil {
			return err
		}
		if login == nil {
			return errors.New("--tslogin requires tailnet.oauth client credentials in the config")
		}
		request.Tailscale = &provision.TailscaleLogin{
			AuthKey: login.AuthKey,
			SSH:     login.SSH,
		}
		fmt.Fprintf(os.Stderr, "==> prepared guest tailscale login for %s\n", request.Hostname)
	}
	result, err := pve.Create(ctx, connection, profile, report, request, *dryRun, cliProgress(os.Stderr))
	if err != nil {
		return err
	}

	if *dryRun {
		fmt.Printf("profile: %s\n", profileName)
		fmt.Printf("kind: %s\n", profile.Kind)
		fmt.Printf("target: %s\n", connection.SSH.Target)
		fmt.Printf("vmid: %d\n", request.VMID)
		fmt.Printf("hostname: %s\n", request.Hostname)
		if profile.Kind == "lxc" {
			fmt.Printf("tag: %s\n", valueOrUnset(result.Tag))
		}
		fmt.Printf("template: %s\n", result.Template)
		fmt.Printf("bridge: %s\n", result.Bridge)
		fmt.Printf("storage: %s\n", result.Storage)
		fmt.Println("")
		fmt.Println("commands:")
		for _, command := range result.Commands {
			fmt.Printf("  - %s\n", strings.TrimSpace(command))
		}
		return nil
	}
	printCreateSummary(profileName, profile, connection, request, result)
	return nil
}

func runList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "path to config file")
	connectionFlag := fs.String("connection", "", "connection name; optional when the config has exactly one connection")
	timeout := fs.Duration("timeout", 45*time.Second, "operation timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: pprov list [flags]")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	connectionName, err := resolveConnectionName(cfg, *connectionFlag)
	if err != nil {
		return err
	}
	connection, err := cfg.Connection(connectionName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	dialer, err := tailnet.NewDialer(cfg.Tailnet)
	if err != nil {
		return err
	}
	defer dialer.Close()

	sshConn, err := sshclient.New(connection.SSH, dialer)
	if err != nil {
		return err
	}
	defer sshConn.Close()

	pve := provision.NewProxmox(sshConn, cfg.BaseDir)
	lxcs, err := pve.ListLXCs(ctx)
	if err != nil {
		return err
	}

	fmt.Printf("connection: %s\n", connectionName)
	fmt.Printf("target: %s\n", connection.SSH.Target)
	if len(lxcs) == 0 {
		fmt.Println("")
		fmt.Println("no LXCs found")
		return nil
	}

	fmt.Println("")
	fmt.Printf("%-6s  %-8s  %-10s  %-24s  %s\n", "VMID", "NODE", "STATUS", "HOSTNAME", "TAGS")
	for _, lxc := range lxcs {
		fmt.Printf("%-6d  %-8s  %-10s  %-24s  %s\n", lxc.VMID, valueOrUnset(lxc.Node), valueOrUnset(lxc.Status), valueOrUnset(lxc.Hostname), valueOrUnset(lxc.Tags))
	}
	return nil
}

func runDestroy(args []string) error {
	fs := flag.NewFlagSet("destroy", flag.ContinueOnError)
	cfgPath := fs.String("config", config.DefaultPath(), "path to config file")
	connectionFlag := fs.String("connection", "", "connection name; optional when the config has exactly one connection")
	timeout := fs.Duration("timeout", 2*time.Minute, "operation timeout")
	dryRun := fs.Bool("dry-run", false, "print remote commands without executing them")
	force := fs.Bool("force", false, "destroy without confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: pprov destroy [flags] <vmid>")
	}
	vmid, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("invalid vmid %q: %w", fs.Arg(0), err)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	connectionName, err := resolveConnectionName(cfg, *connectionFlag)
	if err != nil {
		return err
	}
	connection, err := cfg.Connection(connectionName)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	dialer, err := tailnet.NewDialer(cfg.Tailnet)
	if err != nil {
		return err
	}
	defer dialer.Close()

	sshConn, err := sshclient.New(connection.SSH, dialer)
	if err != nil {
		return err
	}
	defer sshConn.Close()

	pve := provision.NewProxmox(sshConn, cfg.BaseDir)
	fmt.Fprintf(os.Stderr, "==> inspecting vmid=%d via %s\n", vmid, connection.SSH.Target)
	info, err := pve.InspectLXC(ctx, vmid)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "==> resolved hostname=%s status=%s\n", valueOrUnset(info.Hostname), valueOrUnset(info.Status))

	var tailnetIdentity *provision.TailnetIdentity
	if info.Status == "running" {
		tailnetIdentity, err = pve.InspectLXCTailnet(ctx, vmid)
		if err != nil {
			fmt.Fprintf(os.Stderr, "==> tailnet identity lookup skipped: %v\n", err)
		}
	}
	if !*dryRun && !*force {
		if err := confirmDestroy(connection.SSH.Target, info); err != nil {
			return err
		}
	}

	cleanupResult, cleanupErr := destroyTailnetDevice(ctx, cfg.Tailnet, info, tailnetIdentity, *dryRun)
	if cleanupErr != nil {
		fmt.Fprintf(os.Stderr, "==> tailnet cleanup warning: %v\n", cleanupErr)
	} else if cleanupResult != nil && cleanupResult.Message != "" {
		fmt.Fprintf(os.Stderr, "==> tailnet cleanup: %s\n", cleanupResult.Message)
	}

	result, err := pve.DestroyLXC(ctx, info, *dryRun, cliProgress(os.Stderr))
	if err != nil {
		return err
	}

	if *dryRun {
		fmt.Printf("target: %s\n", connection.SSH.Target)
		fmt.Printf("vmid: %d\n", result.VMID)
		fmt.Printf("hostname: %s\n", valueOrUnset(result.Hostname))
		fmt.Printf("status: %s\n", valueOrUnset(result.Status))
		if cleanupResult != nil {
			fmt.Printf("tailnet cleanup: %s\n", valueOrUnset(cleanupResult.Message))
		}
		fmt.Println("")
		fmt.Println("commands:")
		for _, command := range result.Commands {
			fmt.Printf("  - %s\n", strings.TrimSpace(command))
		}
		return nil
	}

	fmt.Printf("target: %s\n", connection.SSH.Target)
	fmt.Printf("vmid: %d\n", result.VMID)
	fmt.Printf("hostname: %s\n", valueOrUnset(result.Hostname))
	fmt.Printf("previous status: %s\n", valueOrUnset(result.Status))
	if cleanupResult != nil {
		fmt.Printf("tailnet cleanup: %s\n", valueOrUnset(cleanupResult.Message))
	}
	fmt.Printf("result: destroyed\n")
	return nil
}

func usageError() error {
	printUsage()
	return errors.New("missing or unknown command")
}

func printUsage() {
	fmt.Println("pprov init [--write pprov.yaml]")
	fmt.Println("pprov probe [--config pprov.yaml] <connection>")
	fmt.Println("pprov create [--config pprov.yaml] [--vmid auto] [--start-id 2000] [--ip cidr] [--gateway ip] [--dry-run] [--tslogin] <profile> <hostname>")
	fmt.Println("pprov list [--config pprov.yaml] [--connection name]")
	fmt.Println("pprov destroy [--config pprov.yaml] [--connection name] [--dry-run] [--force] <vmid>")
}

func valueOrUnset(value string) string {
	if value == "" {
		return "unset"
	}
	return value
}

const phaseOutputLimit = 8

type progressRenderer struct {
	stream  *os.File
	isTTY   bool
	width   int
	mu      sync.Mutex
	current progressPhaseState
}

type progressPhaseState struct {
	phase         string
	recentLines   []string
	activeLine    string
	totalLines    int
	renderedLines int
}

func cliProgress(stream *os.File) provision.ProgressFunc {
	width := 120
	if term.IsTerminal(int(stream.Fd())) {
		if w, _, err := term.GetSize(int(stream.Fd())); err == nil && w > 0 {
			width = w
		}
	}
	renderer := &progressRenderer{
		stream: stream,
		isTTY:  term.IsTerminal(int(stream.Fd())),
		width:  width,
	}
	return renderer.handle
}

func (r *progressRenderer) handle(event provision.ProgressEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch event.Stage {
	case "start":
		r.current = progressPhaseState{phase: event.Phase}
		fmt.Fprintf(r.stream, "==> [%d/%d] %s\n", event.Index, event.Total, event.Phase)
	case "output":
		r.printOutput(event.Line, event.Replace)
	case "done":
		r.finalizeCurrentBox()
		fmt.Fprintf(r.stream, "==> [%d/%d] %s complete (%s)\n", event.Index, event.Total, event.Phase, event.Duration.Round(time.Second))
		r.current = progressPhaseState{}
	case "error":
		r.finalizeCurrentBox()
		fmt.Fprintf(r.stream, "==> [%d/%d] %s failed after %s\n", event.Index, event.Total, event.Phase, event.Duration.Round(time.Second))
		r.current = progressPhaseState{}
	}
}

func (r *progressRenderer) printOutput(raw string, replace bool) {
	line := sanitizeOutputLine(raw)
	if line == "" {
		return
	}
	if replace {
		r.advanceActiveLine(line)
	} else {
		r.commitOutputLine(line)
	}
	if r.isTTY {
		r.renderRollingBox()
	}
}

func (r *progressRenderer) finalizeCurrentBox() {
	updated := r.commitActiveLine()
	if !r.isTTY {
		r.flushRecentOutputBox()
		return
	}
	if updated {
		r.renderRollingBox()
	}
	r.current.renderedLines = 0
}

func (r *progressRenderer) flushRecentOutputBox() {
	lines := r.visibleRecentLines()
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(r.stream, "    +-- recent output\n")
	for _, line := range lines {
		fmt.Fprintf(r.stream, "    | %s\n", r.clipLine(line, len("    | ")))
	}
	if total := r.visibleLineCount(); total > len(lines) {
		summary := fmt.Sprintf("... showing most recent %d of %d lines", len(lines), total)
		fmt.Fprintf(r.stream, "    | %s\n", r.clipLine(summary, len("    | ")))
	}
	fmt.Fprintf(r.stream, "    +-- end output\n")
}

func (r *progressRenderer) renderRollingBox() {
	r.clearRenderedBox()

	lines := []string{"    +-- recent output"}
	for _, line := range r.visibleRecentLines() {
		lines = append(lines, "    | "+r.clipLine(line, len("    | ")))
	}
	if visibleLines := r.visibleRecentLines(); r.visibleLineCount() > len(visibleLines) {
		summary := fmt.Sprintf("... showing most recent %d of %d lines", len(visibleLines), r.visibleLineCount())
		lines = append(lines, "    | "+r.clipLine(summary, len("    | ")))
	}
	lines = append(lines, "    +-- end output")

	fmt.Fprintln(r.stream, strings.Join(lines, "\n"))
	r.current.renderedLines = len(lines)
}

func (r *progressRenderer) clearRenderedBox() {
	if r.current.renderedLines == 0 {
		return
	}
	for i := 0; i < r.current.renderedLines; i++ {
		fmt.Fprintf(r.stream, "\x1b[1A\r\x1b[2K")
	}
}

func sanitizeOutputLine(raw string) string {
	line := strings.ReplaceAll(raw, "\r", "")
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	return line
}

func (r *progressRenderer) appendCommittedLine(line string) {
	if line == "" {
		return
	}
	if len(r.current.recentLines) > 0 && r.current.recentLines[len(r.current.recentLines)-1] == line {
		r.current.activeLine = ""
		return
	}
	r.current.totalLines++
	r.current.recentLines = append(r.current.recentLines, line)
	if len(r.current.recentLines) > phaseOutputLimit {
		r.current.recentLines = append([]string{}, r.current.recentLines[len(r.current.recentLines)-phaseOutputLimit:]...)
	}
	r.current.activeLine = ""
}

func (r *progressRenderer) commitActiveLine() bool {
	if r.current.activeLine == "" {
		return false
	}
	line := r.current.activeLine
	r.appendCommittedLine(line)
	return r.current.activeLine == ""
}

func (r *progressRenderer) advanceActiveLine(line string) {
	if r.current.activeLine == line {
		return
	}
	if r.current.activeLine != "" {
		r.appendCommittedLine(r.current.activeLine)
	}
	r.current.activeLine = line
}

func (r *progressRenderer) commitOutputLine(line string) {
	if r.current.activeLine != "" && r.current.activeLine != line {
		r.appendCommittedLine(r.current.activeLine)
	}
	r.current.activeLine = ""
	r.appendCommittedLine(line)
}

func (r *progressRenderer) visibleRecentLines() []string {
	lines := append([]string{}, r.current.recentLines...)
	if r.current.activeLine != "" {
		lines = append(lines, r.current.activeLine)
	}
	if len(lines) > phaseOutputLimit {
		lines = append([]string{}, lines[len(lines)-phaseOutputLimit:]...)
	}
	return lines
}

func (r *progressRenderer) visibleLineCount() int {
	total := r.current.totalLines
	if r.current.activeLine != "" {
		total++
	}
	return total
}

func (r *progressRenderer) clipLine(line string, prefixWidth int) string {
	maxWidth := r.width - prefixWidth - 1
	if maxWidth < 24 {
		maxWidth = 24
	}
	runes := []rune(line)
	if len(runes) <= maxWidth {
		return line
	}
	head := maxWidth / 3
	tail := maxWidth - head - 3
	if tail < 8 {
		tail = 8
		head = maxWidth - tail - 3
	}
	return string(runes[:head]) + "..." + string(runes[len(runes)-tail:])
}

func printCreateSummary(
	profileName string,
	profile config.ProvisionProfile,
	connection config.Connection,
	request provision.Request,
	result *provision.CreateResult,
) {
	fmt.Printf("profile: %s\n", profileName)
	fmt.Printf("kind: %s\n", profile.Kind)
	fmt.Printf("target: %s\n", connection.SSH.Target)
	fmt.Printf("vmid: %d\n", request.VMID)
	fmt.Printf("hostname: %s\n", request.Hostname)
	if profile.Kind == "lxc" {
		fmt.Printf("tag: %s\n", valueOrUnset(result.Tag))
	}
	if result.Summary != nil {
		fmt.Printf("node: %s\n", valueOrUnset(result.Summary.Node))
		fmt.Printf("status: %s\n", valueOrUnset(result.Summary.Status))
		fmt.Printf("os: %s\n", valueOrUnset(result.Summary.OS))
		fmt.Printf("ipv4: %s\n", valueOrUnset(joinOrUnset(result.Summary.IPv4)))
		fmt.Printf("ipv6: %s\n", valueOrUnset(joinOrUnset(result.Summary.IPv6)))
		fmt.Printf("docker: %s\n", valueOrUnset(result.Summary.DockerVersion))
		fmt.Printf("tailscale: %s\n", valueOrUnset(result.Summary.TailscaleVersion))
		if result.Summary.RootPasswordSet {
			fmt.Printf("root login: password set during provisioning\n")
		} else {
			fmt.Printf("root login: no password was set by pprov\n")
		}
		if result.Summary.Warning != "" {
			fmt.Printf("warning: %s\n", result.Summary.Warning)
		}
		return
	}
	fmt.Printf("template: %s\n", result.Template)
	fmt.Printf("bridge: %s\n", result.Bridge)
	fmt.Printf("storage: %s\n", result.Storage)
}

func joinOrUnset(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.Join(values, ", ")
}

func resolveConnectionName(cfg *config.Config, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	if len(cfg.Connections) == 0 {
		return "", errors.New("no connections are configured")
	}
	if len(cfg.Connections) == 1 {
		for name := range cfg.Connections {
			return name, nil
		}
	}
	names := make([]string, 0, len(cfg.Connections))
	for name := range cfg.Connections {
		names = append(names, name)
	}
	sort.Strings(names)
	return "", fmt.Errorf("multiple connections are configured; pass --connection (%s)", strings.Join(names, ", "))
}

func profileHasAddon(profile config.ProvisionProfile, want string) bool {
	for _, addon := range profile.Addons {
		if addon == want {
			return true
		}
	}
	return false
}

func normalizeFlagArgs(args []string, known map[string]bool) ([]string, error) {
	flags := []string{}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}

		name := arg
		if idx := strings.Index(name, "="); idx >= 0 {
			name = name[:idx]
		}
		isBool, ok := known[name]
		if !ok {
			positionals = append(positionals, arg)
			continue
		}

		flags = append(flags, arg)
		if idx := strings.Index(arg, "="); idx >= 0 || isBool {
			continue
		}
		if i+1 >= len(args) {
			return nil, fmt.Errorf("flag %s requires a value", name)
		}
		i++
		flags = append(flags, args[i])
	}
	return append(flags, positionals...), nil
}

func destroyTailnetDevice(
	ctx context.Context,
	tailnetCfg config.TailnetConfig,
	info *provision.LXCInfo,
	identity *provision.TailnetIdentity,
	dryRun bool,
) (*tailnet.CleanupResult, error) {
	api, err := tailnet.NewAPI(tailnetCfg)
	if err != nil {
		return nil, err
	}
	if api == nil {
		return &tailnet.CleanupResult{
			Message: "skipped (no tailnet API key configured)",
		}, nil
	}

	query := tailnet.CleanupQuery{Hostname: info.Hostname}
	if identity != nil {
		query.Addresses = append(query.Addresses, identity.Addresses...)
	}

	matchLabel := valueOrUnset(info.Hostname)
	if len(query.Addresses) > 0 {
		matchLabel = strings.Join(query.Addresses, ", ")
	}
	if dryRun {
		return &tailnet.CleanupResult{
			Configured: true,
			Message:    fmt.Sprintf("would remove matching tailnet device for %s", matchLabel),
		}, nil
	}
	return api.DeleteDeviceForCleanup(ctx, query)
}

func confirmDestroy(target string, info *provision.LXCInfo) error {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return errors.New("destroy requires confirmation; rerun interactively or pass --force")
	}

	hostname := valueOrUnset(info.Hostname)
	fmt.Fprintf(os.Stderr, "Destroy LXC %d (%s) on %s? [y/N]: ", info.VMID, hostname, target)
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return errors.New("destroy aborted")
	}
	return nil
}

func resolveLXCRootPassword(profile config.ProvisionProfile, dryRun bool) (string, error) {
	if profile.Kind != "lxc" {
		return "", nil
	}

	envName := profile.RootPasswordEnv
	if envName == "" {
		envName = "PPROV_LXC_ROOT_PASSWORD"
	}
	if value := os.Getenv(envName); value != "" {
		return value, nil
	}
	if dryRun {
		return "", nil
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		if profile.RootPasswordEnv != "" {
			return "", fmt.Errorf("lxc root password is required; set %s or run create interactively", profile.RootPasswordEnv)
		}
		return "", fmt.Errorf("lxc root password is required; set %s or add root_password_env to the profile", envName)
	}

	fmt.Fprint(os.Stderr, "LXC root password: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(first)) == "" {
		return "", errors.New("lxc root password cannot be empty")
	}

	fmt.Fprint(os.Stderr, "Confirm root password: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(first) != string(second) {
		return "", errors.New("lxc root password confirmation did not match")
	}
	return string(first), nil
}

func resolveGuestLocale(profile config.ProvisionProfile) string {
	if locale := normalizeLocale(profile.Locale); locale != "" {
		return locale
	}
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if locale := normalizeLocale(os.Getenv(key)); locale != "" {
			return locale
		}
	}
	return "en_US.UTF-8"
}

func normalizeLocale(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if idx := strings.Index(trimmed, "@"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	switch {
	case strings.EqualFold(trimmed, "UTF-8"), strings.EqualFold(trimmed, "UTF8"):
		return "en_US.UTF-8"
	case strings.EqualFold(trimmed, "C"), strings.EqualFold(trimmed, "POSIX"), strings.EqualFold(trimmed, "C.UTF-8"), strings.EqualFold(trimmed, "C.UTF8"):
		return "en_US.UTF-8"
	}
	trimmed = strings.ReplaceAll(trimmed, ".UTF8", ".UTF-8")
	trimmed = strings.ReplaceAll(trimmed, ".utf8", ".UTF-8")
	trimmed = strings.ReplaceAll(trimmed, ".Utf8", ".UTF-8")
	return trimmed
}
