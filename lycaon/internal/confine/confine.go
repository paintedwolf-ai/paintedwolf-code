// Package confine applies per-process filesystem and network boundaries.
package confine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/lineage"
)

// helperFlag marks the confined re-exec path.
const helperFlag = "__confine-exec"

func init() {
	// Helper argv is handled here, before main.
	runHelperIfInvoked()
}

// helperProfileFD receives the inherited profile stream.
const (
	profileFDFlag   = "--profile-fd"
	helperProfileFD = 3
	// lineageFDFlag names the descendant marker the helper moves into place
	// before exec.
	lineageFDFlag   = "--lineage-fd"
	helperLineageFD = 4
)

// NetworkMode is the egress policy for a confined command.
type NetworkMode int

const (
	// NetworkDeny permits no public TCP/IP outbound. LoopbackConnect may add localhost.
	NetworkDeny NetworkMode = iota
	// NetworkProxyOnly permits authenticated mediated egress.
	NetworkProxyOnly
	// NetworkDirectIP permits IP egress plus exact socket grants.
	NetworkDirectIP
)

// Name is the closed observation token OAR publishes as paintedwolf.network_mode.
func (m NetworkMode) Name() string {
	switch m {
	case NetworkDeny:
		return "deny"
	case NetworkProxyOnly:
		return "proxy_only"
	case NetworkDirectIP:
		return "direct_ip"
	default:
		return ""
	}
}

// EgressOffCause distinguishes reasons for a denied network boundary.
type EgressOffCause int

const (
	// EgressOffUnknown carries no observed cause.
	EgressOffUnknown EgressOffCause = iota
	// EgressOffOverride is an explicit network deny.
	EgressOffOverride
	// EgressOffBrowser denies public TCP/IP; localhost remains available.
	EgressOffBrowser
)

type Boundary struct {
	ProcessControl bool
	HostExecution  bool
	Applied        bool
	Network        NetworkMode
	// EgressOff is why Network is NetworkDeny. Meaningless for any other mode.
	EgressOff EgressOffCause
	// SocksProxyEnv records mediated non-HTTP routing.
	SocksProxyEnv bool
	// WriteRoots are the resolved write capabilities.
	WriteRoots []string
	// LocalListen records whether the profile carried local listener authority.
	LocalListen bool
	// LoopbackConnect records whether the profile can connect to local TCP/UDP services.
	LoopbackConnect bool
	// Filesystem is the rule list the profile rendered, for host verdicts.
	Filesystem FilesystemRules
}

// BoundaryOf projects applied confinement into a boundary fact.
func BoundaryOf(c *Confinement) Boundary {
	if c != nil && c.HostExecution {
		return Boundary{HostExecution: true, Network: NetworkDirectIP}
	}
	if c == nil {
		return Boundary{}
	}
	writeRoots, err := validatedWriteRoots(c.ProjectID, c.Roots, c.GrantedWriteRoots, c.SessionScratchRoot)
	if err != nil {
		return Boundary{}
	}
	filesystem, err := filesystemRules(*c)
	if err != nil {
		return Boundary{}
	}
	return Boundary{
		ProcessControl: c.ProcessControl,
		Filesystem:     filesystem,
		Applied:        true, Network: c.Network, EgressOff: c.EgressOff,
		SocksProxyEnv:   c.SocksProxyEnv && c.Network == NetworkProxyOnly && c.SocksProxyAddr != "",
		WriteRoots:      writeRoots,
		LocalListen:     c.Network == NetworkDirectIP || c.Browser || c.LocalListen,
		LoopbackConnect: c.Network == NetworkDirectIP || c.LoopbackConnect,
	}
}

// DeniesDirectSockets reports whether direct network access is blocked.
func (b Boundary) DeniesDirectSockets() bool {
	return b.Applied && (b.Network == NetworkProxyOnly || b.Network == NetworkDeny)
}

// Confinement describes the box for one command.
type Confinement struct {
	ProcessControl bool
	HostExecution  bool
	// ProjectID routes project-scoped grants.
	ProjectID string
	// Roots are attached write locations.
	Roots []string
	// GrantedWriteRoots are per-action reviewed write-root leases.
	GrantedWriteRoots []string
	// SessionScratchRoot is the session-owned scratch root for this execution.
	SessionScratchRoot string
	// ReadRoots are read-only carve-outs.
	ReadRoots []string
	// ReadDenyPaths are per-action read exclusions.
	ReadDenyPaths []string
	// EgressOff records the observed deny cause.
	EgressOff EgressOffCause
	// SocketGrants are exact AF_UNIX connect literals validated at the choke point.
	SocketGrants []SocketGrant
	// DroppedSocketGrants records rejected socket grants.
	DroppedSocketGrants []SocketGrantDrop
	// ProtectedWriteGrants are approved protected-path write transactions.
	ProtectedWriteGrants []ProtectedPathGrant
	PolicyWriteGrants    []ProtectedPathGrant
	// DroppedProtectedWriteGrants records rejected write grants.
	DroppedProtectedWriteGrants []ProtectedPathDrop
	// ProtectedReadGrants are approved protected-path read transactions.
	ProtectedReadGrants []ProtectedPathGrant
	// DroppedProtectedReadGrants records rejected read grants.
	DroppedProtectedReadGrants []ProtectedPathDrop
	// Network is the egress policy.
	Network NetworkMode
	// ProxyAddr is the action lease's HTTP endpoint.
	ProxyAddr string
	// SocksProxyAddr is the action lease's authenticated TCP endpoint.
	SocksProxyAddr string
	// LineageID names the descendants this action answers for. It attributes
	// mediated connections and never appears in a child environment.
	LineageID string
	// lineage is the live descendant handle; BindAction opens it.
	lineage *lineage.Lineage
	// refusalTag names the bound action in every deny rule, so the kernel's
	// refusal reports reach its lease. BindAction mints it.
	refusalTag string
	// SocksProxyEnv enables mediated non-HTTP TCP routing.
	SocksProxyEnv bool
	// DirectIPPermits narrows direct IP by transport and port.
	DirectIPPermits []DirectIPPermit
	// LocalListen grants bind and accept independently of outbound authority.
	LocalListen bool
	// LocalListenPorts limits LocalListen to these ports; empty permits any local port.
	LocalListenPorts []uint16
	// LoopbackConnect permits local outbound connections without changing external egress.
	LoopbackConnect bool
	// LoopbackConnectPorts optionally limits local destination ports.
	LoopbackConnectPorts []uint16
	// Browser permits local listeners independently of LocalListen.
	Browser bool
}

var (
	autoConfineMu sync.RWMutex
	autoConfine   bool
)

func autoConfineEnabled() bool {
	autoConfineMu.RLock()
	defer autoConfineMu.RUnlock()
	return autoConfine
}

func setAutoConfine(on bool) {
	autoConfineMu.Lock()
	autoConfine = on
	autoConfineMu.Unlock()
}

// EnableAutoConfine turns confinement on for this process.
func EnableAutoConfine() { setAutoConfine(true) }

// TestingSetAutoConfine turns confinement on for this test and restores it.
func TestingSetAutoConfine(t interface {
	Helper()
	Cleanup(func())
}) {
	t.Helper()
	prev := autoConfineEnabled()
	setAutoConfine(true)
	t.Cleanup(func() { setAutoConfine(prev) })
}

// BypassEnabled reports the explicit host-wide bypass.
func BypassEnabled() bool {
	return configdir.EnvTruthy(os.Getenv("LYCAON_BYPASS_APPROVALS"))
}

// DefaultConfinement builds the boundary for a valid rooted request.
func DefaultConfinement(req Request) (*Confinement, bool) {
	if req.HostExecution {
		return &Confinement{HostExecution: true, Network: NetworkDirectIP, Roots: append([]string(nil), req.Roots...)}, true
	}
	prepared, err := prepareRequest(req)
	if err != nil {
		logConfinementPrepareError(err)
		return nil, false
	}
	if !autoConfineEnabled() || BypassEnabled() || SandboxDisabled() || !Available() || len(prepared.Roots) == 0 {
		return nil, false
	}
	c := &Confinement{
		ProcessControl:              req.ProcessControl,
		ProjectID:                   req.ProjectID,
		Roots:                       prepared.Roots,
		GrantedWriteRoots:           append([]string(nil), prepared.GrantedWriteRoots...),
		SessionScratchRoot:          prepared.SessionScratchRoot,
		ReadRoots:                   prepared.ReadRoots,
		ReadDenyPaths:               prepared.ReadDenyPaths,
		SocketGrants:                prepared.SocketGrants,
		DroppedSocketGrants:         prepared.Dropped,
		ProtectedWriteGrants:        prepared.ProtectedWrites,
		PolicyWriteGrants:           prepared.PolicyWrites,
		DroppedProtectedWriteGrants: prepared.DroppedProtected,
		ProtectedReadGrants:         prepared.ProtectedReads,
		DroppedProtectedReadGrants:  prepared.DroppedProtectedReads,
		LocalListen:                 prepared.LocalListen,
		LocalListenPorts:            prepared.LocalListenPorts,
		LoopbackConnect:             prepared.LoopbackConnect,
		LoopbackConnectPorts:        prepared.LoopbackConnectPorts,
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LYCAON_SANDBOX_NETWORK"))) {
	case "deny", "block", "none":
		c.Network = NetworkDeny
		c.EgressOff = EgressOffOverride
		return c, true
	}
	if prepared.Egress == EgressDeny {
		c.Network = NetworkDeny
		c.EgressOff = EgressOffOverride
		return c, true
	}
	if prepared.Egress == EgressDirectIP {
		c.Network = NetworkDirectIP
		c.DirectIPPermits = prepared.DirectIPPermits
		return c, true
	}
	c.Network = NetworkProxyOnly
	c.SocksProxyEnv = req.SocksProxyEnv
	return c, true
}

// BrowserConfinement permits local connections within the requested port scope.
// Public TCP/IP is denied; an empty port list permits every local port.
func BrowserConfinement(req Request) (*Confinement, bool) {
	c, ok := DefaultConfinement(req)
	if !ok || c == nil {
		return nil, false
	}
	out := *c
	out.Network = NetworkDeny
	out.EgressOff = EgressOffBrowser
	out.ProxyAddr = ""
	out.SocksProxyAddr = ""
	out.SocksProxyEnv = false
	out.Browser = true
	// Browser confinement permits only local network reach.
	out.LoopbackConnect = true
	return &out, true
}

// Enforcing reports whether the host applies confinement.
func Enforcing() bool {
	return autoConfineEnabled() && !BypassEnabled() && !SandboxDisabled() && Available()
}

// ErrNotConfined rejects a missing active boundary.
var ErrNotConfined = errors.New("confine: refusing to run unconfined — sandbox is enabled " +
	"but no confinement could be built for this action (no write-jail roots, or a rejected " +
	"capability grant)")

// ErrNestedHelper rejects wrapping from a process that is already the confine helper.
var ErrNestedHelper = errors.New("confine: this process is already applying a sandbox profile")

// RequireApplied rejects a missing boundary while enforcement is active.
func RequireApplied(applied bool) error {
	if applied || !Enforcing() {
		return nil
	}
	return ErrNotConfined
}

// SandboxDisabled reports the explicit confinement off-switch.
func SandboxDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LYCAON_SANDBOX"))) {
	case "off", "0", "false", "no", "disable", "disabled":
		return true
	}
	return false
}

// Command builds the process for the selected confinement boundary.
func Command(
	ctx context.Context,
	self, name string,
	args []string,
	c Confinement,
) (*exec.Cmd, func(), error) {
	if IsHelperInvocation(os.Args) {
		return nil, func() {}, ErrNestedHelper
	}
	if c.HostExecution {
		return exec.CommandContext(ctx, name, args...), func() {}, nil
	}
	profile, err := BuildProfile(c)
	if err != nil {
		return nil, func() {}, err
	}
	// An inherited pipe keeps the profile outside writable pathnames.
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, func() {}, err
	}
	helperArgs := []string{helperFlag, profileFDFlag, strconv.Itoa(helperProfileFD)}
	// ExtraFiles[0] lands on helperProfileFD in the child.
	extra := []*os.File{pr}
	if marker := c.lineage.ChildFile(); marker != nil {
		helperArgs = append(helperArgs, lineageFDFlag, strconv.Itoa(helperLineageFD))
		extra = append(extra, marker)
	}
	helperArgs = append(append(helperArgs, "--", name), args...)
	cmd := exec.CommandContext(ctx, self, helperArgs...)
	cmd.ExtraFiles = extra
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = io.WriteString(pw, profile)
	}()
	// Releasing the host's own end lets the marker report end-of-file once the
	// last descendant exits.
	cleanup := func() {
		_ = pr.Close()
		c.lineage.Started()
	}
	return cmd, cleanup, nil
}

// BuildProfile renders the platform confinement profile.
func BuildProfile(c Confinement) (string, error) {
	if c.HostExecution {
		return "", fmt.Errorf("host execution has no sandbox profile")
	}
	policy, err := ValidatePolicyWriteGrants(c.PolicyWriteGrants, c.Roots)
	if err != nil {
		return "", err
	}
	c.PolicyWriteGrants = policy
	normalized, err := normalizeSocketGrantList(c.SocketGrants)
	if err != nil {
		return "", err
	}
	applied, dropped, err := validateSocketGrants(normalized)
	if err != nil {
		return "", err
	}
	c.SocketGrants = applied
	c.DroppedSocketGrants = append(append([]SocketGrantDrop(nil), c.DroppedSocketGrants...), dropped...)
	var b strings.Builder
	deny := denyReport(c.refusalTag)
	b.WriteString("(version 1)\n")
	b.WriteString("(deny default" + deny + ")\n")
	writeProcessAllows(&b, c.ProcessControl)
	if c.Browser {
		writeBrowserAllows(&b)
	}
	if err := writeFilesystemRules(&b, c, deny); err != nil {
		return "", err
	}
	if err := writeNetworkRules(&b, c); err != nil {
		return "", err
	}
	writeHostCapabilityDenials(&b, deny)
	return b.String(), nil
}

// denyReport is the modifier a deny rule carries so the kernel's report of
// what it refused names the action; empty for an unbound confinement.
func denyReport(tag string) string {
	if tag == "" {
		return ""
	}
	return " (with message " + sbplString(refusalTagPrefix+tag) + ")"
}

func writeProcessAllows(b *strings.Builder, processControl bool) {
	b.WriteString("(allow process-fork)\n")
	b.WriteString("(allow process-exec*)\n")
	// Child signaling stays within the same boundary.
	if processControl {
		b.WriteString("(allow signal)\n")
	} else {
		b.WriteString("(allow signal (target same-sandbox))\n")
	}
	b.WriteString("(allow sysctl-read)\n")
	b.WriteString("(allow mach-lookup)\n")
	b.WriteString("(allow ipc-posix-shm)\n")
	// Interactive terminals require ioctl access to pseudo-terminals.
	b.WriteString("(allow file-ioctl)\n")
	b.WriteString("(allow pseudo-tty)\n")
}

func writeBrowserAllows(b *strings.Builder) {
	b.WriteString("(allow process*)\n")
	b.WriteString("(allow process-info-pidinfo)\n")
	b.WriteString("(allow sysctl*)\n")
	b.WriteString("(allow mach*)\n")
	b.WriteString("(allow iokit*)\n")
	b.WriteString("(allow ipc*)\n")
	b.WriteString("(allow system-socket)\n")
	b.WriteString("(allow user-preference-read)\n")
	b.WriteString("(allow distributed-notification-post)\n")
	b.WriteString("(allow dynamic-code-generation)\n")
	b.WriteString("(allow hid-control)\n")
	b.WriteString("(allow file-map-executable)\n")
	b.WriteString("(allow file-clone)\n")
	b.WriteString("(allow nvram-get)\n")
	b.WriteString("(allow appleevent-send)\n")
	b.WriteString("(allow device-microphone)\n")
	b.WriteString("(allow device-camera)\n")
}

func writeFilesystemRules(b *strings.Builder, c Confinement, deny string) error {
	rules, err := filesystemRules(c)
	if err != nil {
		return err
	}
	rules.render(b, deny)
	return nil
}

// filesystemRules builds the ordered filesystem policy for a confinement.
// Later rules override earlier decisions.
func filesystemRules(c Confinement) (FilesystemRules, error) {
	rules := FilesystemRules{roots: append([]string(nil), c.Roots...)}
	writeRoots, err := validatedWriteRoots(c.ProjectID, c.Roots, c.GrantedWriteRoots, c.SessionScratchRoot)
	if err != nil {
		return rules, err
	}
	// The read floor protects control-plane and key material.
	if err := requireReadFloorResolvable(); err != nil {
		return rules, err
	}
	rules.add(fsRule{op: opRead, allow: true, everywhere: true})
	deny := readDenyMatches(c)
	if len(deny) > 0 {
		rules.add(fsRule{op: opRead, matches: deny})
		// Managed read roots and approved read grants override denied ancestors.
		allow := SecretReadAllowBackRoots()
		allow = append(allow, resolveEach(c.ReadRoots)...)
		if c.SessionScratchRoot != "" {
			allow = append(allow, fspath.CanonicalPath(c.SessionScratchRoot))
		}
		var literals []string
		for _, g := range c.ProtectedReadGrants {
			if g.Subtree {
				allow = append(allow, g.ResolvedPath)
			} else {
				literals = append(literals, g.ResolvedPath)
			}
		}
		rules.add(fsRule{op: opRead, allow: true, matches: append(subpathMatches(allow, ""), literalMatches(literals, "")...)})
		// Ancestor metadata permits traversal into allowed descendants.
		if len(allow) > 0 || len(literals) > 0 {
			ancestors := traversalAncestors(matchPaths(deny), append(append([]string(nil), allow...), literals...))
			rules.add(fsRule{op: opReadMetadata, allow: true, matches: literalMatches(ancestors, "")})
		}
	}
	addControlPlaneReadFloor(&rules, c.ReadRoots, c.SessionScratchRoot)
	// Writes confined to the project plus temp and the standard device sinks.
	sinks := subpathMatches(writeRoots, "")
	sinks = append(sinks,
		fsMatch{literal: "/dev/null"},
		fsMatch{subpath: "/dev/fd"},
		fsMatch{literal: "/dev/tty"},
		fsMatch{literal: "/dev/ptmx"},
		fsMatch{literal: "/dev/dtracehelper"},
		// Terminal allocation writes its ephemeral slave node.
		fsMatch{regex: "^/dev/ttys"},
	)
	rules.add(fsRule{op: opWrite, allow: true, matches: sinks})

	specs, err := HardDenyWriteSpecs(c)
	if err != nil {
		return rules, err
	}
	rules.add(denySpecRule(specs, FloorBaseline, FloorProtected))
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches(resolveEach(c.GrantedWriteRoots), "")})
	rules.add(fsRule{op: opWrite, allow: true, matches: subpathMatches(workspaceAllowBacks(c), "")})
	rules.add(denySpecRule(specs, FloorProtected))
	rules.add(protectedGrantRule(c.ProtectedWriteGrants))
	rules.add(denySpecRule(specs, FloorAgentPolicy))
	rules.add(protectedGrantRule(c.PolicyWriteGrants))
	rules.add(denySpecRule(specs, FloorControlPlane))
	// Raw disk devices stay unwritable under every grant.
	rules.add(fsRule{op: opWrite, matches: []fsMatch{
		{regex: "^/dev/disk", layer: FloorControlPlane},
		{regex: "^/dev/rdisk", layer: FloorControlPlane},
	}})
	if err := rules.checkStringLengths(); err != nil {
		return rules, err
	}
	err = rules.compilePatterns()
	return rules, err
}

// readDenyMatches lists the read floor with the layer each entry protects.
func readDenyMatches(c Confinement) []fsMatch {
	var out []fsMatch
	if !secretReadDenyDisabled() {
		out = append(out, subpathMatches(resolveEach(controlPlaneReadDenyRoots()), FloorReadControlPlane)...)
		for _, e := range filepath.SplitList(strings.TrimSpace(os.Getenv("LYCAON_SANDBOX_DENY_READ"))) {
			if strings.TrimSpace(e) != "" {
				out = append(out, subpathMatches(resolveEach([]string{e}), FloorReadConfigured)...)
			}
		}
	}
	out = append(out, subpathMatches(KeyMaterialReadDenyPaths(), FloorReadKeyMaterial)...)
	return append(out, subpathMatches(resolveEach(c.ReadDenyPaths), FloorReadConfigured)...)
}

func matchPaths(matches []fsMatch) []string {
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m.subpath)
	}
	return out
}

func denySpecRule(specs []HardDenyWriteSpec, layers ...FloorLayer) fsRule {
	rule := fsRule{op: opWrite}
	for _, spec := range specs {
		if !slices.Contains(layers, spec.Layer) {
			continue
		}
		rule.matches = append(rule.matches, fsMatch{
			literal: spec.Literal, regex: spec.Regex, subpath: spec.Subpath,
			except: spec.ExceptSubpaths, layer: spec.Layer,
		})
	}
	return rule
}

// workspaceAllowBacks re-allows validated control-plane workspaces.
func workspaceAllowBacks(c Confinement) []string {
	dir, err := configdir.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return nil
	}
	control := fspath.CanonicalPath(dir)
	sep := string(filepath.Separator)
	var backs []string
	candidates := append(append([]string(nil), c.Roots...), c.GrantedWriteRoots...)
	if c.SessionScratchRoot != "" {
		candidates = append(candidates, c.SessionScratchRoot)
	}
	for _, r := range candidates {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		if p := fspath.CanonicalPath(r); strings.HasPrefix(p, control+sep) {
			backs = append(backs, p)
		}
	}
	sort.Strings(backs)
	return backs
}

// protectedGrantRule emits approved protected-path write exceptions.
func protectedGrantRule(grants []ProtectedPathGrant) fsRule {
	rule := fsRule{op: opWrite, allow: true}
	for _, g := range grants {
		if g.Subtree {
			rule.matches = append(rule.matches, fsMatch{subpath: g.ResolvedPath})
		} else {
			rule.matches = append(rule.matches, fsMatch{literal: g.ResolvedPath})
		}
	}
	return rule
}

func writeHostCapabilityDenials(b *strings.Builder, deny string) {
	b.WriteString("(deny mach-lookup\n")
	b.WriteString("  (global-name \"com.apple.system.powerd\")\n")
	b.WriteString("  (global-name \"com.apple.PowerManagement.control\")\n")
	if deny != "" {
		b.WriteString("  " + strings.TrimSpace(deny) + "\n")
	}
	b.WriteString(")\n")
}

// pathUnder reports whether path is root or lies beneath it, with the remainder
// after root.
func pathUnder(path, root string) (string, bool) {
	if path == root {
		return "", true
	}
	if strings.HasPrefix(path, root+"/") {
		return strings.TrimPrefix(path, root), true
	}
	return "", false
}

// sbplString renders a Scheme string literal for SBPL, escaping backslashes and quotes.
func sbplString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return `"` + s + `"`
}

// IsHelperInvocation reports whether argv requests helper dispatch.
func IsHelperInvocation(argv []string) bool {
	return len(argv) >= 2 && argv[1] == helperFlag
}
