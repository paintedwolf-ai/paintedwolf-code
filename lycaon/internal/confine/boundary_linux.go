//go:build linux

package confine

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/lineage"
	"github.com/lycaon/lycaon/internal/protectedpath"
)

// probeTimeout bounds the one helper run that establishes availability.
const probeTimeout = 15 * time.Second

var linuxHost struct {
	once sync.Once
	caps linuxsandbox.Capabilities
}

// Available reports whether confined commands can run: every kernel capability
// is present and one helper run applied and attested a boundary.
func Available() bool { return linuxCapabilities().Available() }

// LinuxCapabilities reports the facts behind Available, so the host can say
// why confinement is unavailable.
func LinuxCapabilities() (linuxsandbox.Capabilities, bool) { return linuxCapabilities(), true }

func linuxCapabilities() linuxsandbox.Capabilities {
	linuxHost.once.Do(func() {
		caps := linuxsandbox.Facts()
		if caps.FloorMet() {
			stderr, err := probeHelper()
			caps.RecordRun(err, stderr)
		}
		linuxHost.caps = caps
	})
	return linuxHost.caps
}

// probeHelper runs the helper once against a plan that only attests, so
// availability rests on a real application rather than on reading switches.
func probeHelper() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	plan := linuxsandbox.Plan{
		Version: linuxsandbox.PlanVersion,
		Probe:   true,
		Attest:  linuxsandbox.Attest{HostNamespaces: linuxsandbox.HostNamespaces()},
		Network: linuxsandbox.Network{Mode: linuxsandbox.NetworkDeny},
	}
	raw, err := plan.Encode()
	if err != nil {
		return "", err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, helperFlag, profileFDFlag, strconv.Itoa(helperProfileFD), "--", self)
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	cmd.ExtraFiles = []*os.File{pr}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write(raw)
	}()
	err = cmd.Run()
	_ = pr.Close()
	return stderr.String(), err
}

// renderProfile is the boundary plan the Linux helper applies.
func renderProfile(c Confinement) (string, error) {
	rules, err := filesystemRules(c)
	if err != nil {
		return "", err
	}
	fs, err := compileFilesystemPlan(rules)
	if err != nil {
		return "", err
	}
	plan := linuxsandbox.Plan{
		Version:        linuxsandbox.PlanVersion,
		ProcessControl: c.ProcessControl,
		Attest:         linuxsandbox.Attest{HostNamespaces: linuxsandbox.HostNamespaces()},
		Filesystem:     fs,
		Network:        networkPlan(c),
	}
	if err := plan.Validate(); err != nil {
		return "", err
	}
	raw, err := plan.Encode()
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// networkPlan carries the network axis with the same derivations BoundaryOf
// reports, so what the helper refuses is what the host would have claimed.
func networkPlan(c Confinement) linuxsandbox.Network {
	n := linuxsandbox.Network{
		Mode:                 c.Network.Name(),
		LoopbackConnect:      c.Network == NetworkDirectIP || c.LoopbackConnect,
		LoopbackConnectPorts: append([]uint16(nil), c.LoopbackConnectPorts...),
		Listen:               c.Network == NetworkDirectIP || c.Browser || c.LocalListen,
		ListenPorts:          append([]uint16(nil), c.LocalListenPorts...),
		ProxyAddr:            c.ProxyAddr,
		SocksProxyAddr:       c.SocksProxyAddr,
		Browser:              c.Browser,
	}
	for _, p := range c.DirectIPPermits {
		n.DirectIPPermits = append(n.DirectIPPermits, linuxsandbox.DirectIPPermit{Protocol: p.Protocol, Port: p.Port})
	}
	for _, g := range c.SocketGrants {
		n.SocketPaths = append(n.SocketPaths, g.ResolvedPath)
	}
	if c.Network == NetworkDirectIP {
		if conf, err := os.ReadFile("/etc/resolv.conf"); err == nil {
			n.Resolvers = linuxsandbox.HostResolvers(string(conf))
		}
	}
	return n
}

// applyAndExec runs the plan. Outside the namespaces it relaunches the helper
// inside them and relays for it; inside, it applies the boundary and
// supervises the target.
func applyAndExec(profile []byte, resolved string, target []string) error {
	plan, err := linuxsandbox.DecodePlan(profile)
	if err != nil {
		return err
	}
	if !linuxsandbox.Inside(plan) {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		code, sig, err := linuxsandbox.Relaunch(self, os.Args[1:], profile, plan.Network, lineageMarkerFile(), helperRefusalFile())
		if err != nil {
			return err
		}
		if sig != 0 {
			linuxsandbox.ExitWithSignal(sig)
		}
		exitHelper(code)
	}
	linuxsandbox.HoldSignals()
	if err := linuxsandbox.Enter(plan); err != nil {
		return err
	}
	if plan.Probe {
		exitHelper(0)
	}
	// Only the target's tree should hold the marker once it runs.
	releaseMarker := func() {
		if lineageMarkerFile() != nil {
			_ = syscall.Close(lineage.ChildFD)
		}
	}
	code, sig, err := linuxsandbox.Supervise(resolved, target, os.Environ(), releaseMarker)
	if err != nil {
		return ClassifyExecError(resolved, err)
	}
	if sig != 0 {
		linuxsandbox.ReportSignal(sig)
	}
	exitHelper(code)
	return nil
}

// lineageMarkerFile is the descendant marker at its documented descriptor,
// when this helper was given one.
func lineageMarkerFile() *os.File {
	if _, err := unix.FcntlInt(uintptr(lineage.ChildFD), unix.F_GETFD, 0); err != nil {
		return nil
	}
	return os.NewFile(uintptr(lineage.ChildFD), "lineage-marker")
}

// planCompiler expresses the ordered filesystem rules as Landlock grants and
// the mounts that carve denials out of them. Later rules override earlier
// ones, exactly as the rendered profile reads.
type planCompiler struct {
	fs linuxsandbox.Filesystem
	// grants are the write grants so far, which decide whether a later deny
	// needs a mount or is already refused by Landlock.
	grants   []string
	readOnly map[string]bool
	hidden   map[string]bool
	restored map[string]bool
}

func compileFilesystemPlan(rules FilesystemRules) (linuxsandbox.Filesystem, error) {
	pc := &planCompiler{readOnly: map[string]bool{}, hidden: map[string]bool{}, restored: map[string]bool{}}
	for _, rule := range rules.rules {
		switch rule.op {
		case opRead:
			pc.readRule(rule)
		case opWrite:
			if err := pc.writeRule(rule); err != nil {
				return linuxsandbox.Filesystem{}, err
			}
		case opReadMetadata:
			// A placeholder's own directories carry traversal into a restored path.
		}
	}
	for p := range pc.readOnly {
		pc.fs.ReadOnly = append(pc.fs.ReadOnly, p)
	}
	for p := range pc.hidden {
		pc.fs.Hidden = append(pc.fs.Hidden, p)
	}
	for p := range pc.restored {
		if coveredBy(pc.hidden, p) || coveredBy(pc.readOnly, p) {
			pc.fs.Restored = append(pc.fs.Restored, p)
		}
	}
	return pc.fs, nil
}

// readRule turns read denials into hidden placeholders and later read allows
// beneath them into restorations. Reads everywhere are Landlock's rule on /.
func (pc *planCompiler) readRule(rule fsRule) {
	if rule.everywhere {
		return
	}
	for _, m := range rule.matches {
		path := m.literal
		if path == "" {
			path = m.subpath
		}
		if path == "" {
			continue
		}
		switch {
		case !rule.allow:
			pc.hidden[path] = true
			delete(pc.restored, path)
		case pc.hidden[path]:
			delete(pc.hidden, path)
		case coveredBy(pc.hidden, path):
			pc.restored[path] = true
		}
	}
}

func (pc *planCompiler) writeRule(rule fsRule) error {
	for _, m := range rule.matches {
		if underCuratedDev(m) {
			continue
		}
		paths, err := writeMatchPaths(m)
		if err != nil {
			return err
		}
		for _, path := range paths {
			if rule.allow {
				pc.allowWrite(path)
			} else {
				pc.denyWrite(path, m.except)
			}
		}
	}
	return nil
}

// allowWrite grants the path to Landlock and restores it beneath any mask.
func (pc *planCompiler) allowWrite(path string) {
	if st, err := os.Stat(path); err == nil && !st.IsDir() {
		pc.fs.WriteFiles = append(pc.fs.WriteFiles, path)
	} else {
		pc.fs.WriteRoots = append(pc.fs.WriteRoots, path)
	}
	pc.grants = append(pc.grants, path)
	switch {
	case pc.readOnly[path]:
		delete(pc.readOnly, path)
	case coveredBy(pc.readOnly, path), coveredBy(pc.hidden, path):
		pc.restored[path] = true
	}
}

// denyWrite masks a path read-only when a grant would otherwise reach it; its
// exceptions are restored.
func (pc *planCompiler) denyWrite(path string, except []string) {
	if coveredBy(pc.hidden, path) || !coveredByList(pc.grants, path) {
		return
	}
	pc.readOnly[path] = true
	for _, e := range except {
		if strings.TrimSpace(e) != "" {
			pc.restored[e] = true
		}
	}
}

// writeMatchPaths lists the paths one write filter names. A basename family
// below a root is the one structural pattern the plan can express, by naming
// the files present at launch.
func writeMatchPaths(m fsMatch) ([]string, error) {
	switch {
	case m.regex != "" && m.subpath != "":
		for _, location := range protectedpath.AgentPolicyLocations() {
			if location.Name != "" && location.NameRegex() == m.regex {
				return namedFilesBelow(m.subpath, location.Name), nil
			}
		}
		return nil, &linuxsandbox.Unsupported{Code: linuxsandbox.UnsupportedFilesystemRule, Detail: m.regex}
	case m.regex != "":
		return nil, &linuxsandbox.Unsupported{Code: linuxsandbox.UnsupportedFilesystemRule, Detail: m.regex}
	case m.literal != "":
		return []string{m.literal}, nil
	case m.subpath != "":
		return []string{m.subpath}, nil
	}
	return nil, nil
}

// underCuratedDev reports a device sink or device denial; the helper builds
// /dev itself, so none of them needs a rule.
func underCuratedDev(m fsMatch) bool {
	return strings.HasPrefix(m.literal, "/dev/") || strings.HasPrefix(m.subpath, "/dev/") || strings.HasPrefix(m.regex, "^/dev/")
}

// agentPolicyWalkBudget bounds the entries one launch inspects for loader
// files named at any depth. A tree past it protects only what was seen.
const agentPolicyWalkBudget = 200_000

// namedFilesBelow lists the regular files below root whose name folds to name.
func namedFilesBelow(root, name string) []string {
	var out []string
	budget := agentPolicyWalkBudget
	var walk func(dir string) bool
	walk = func(dir string) bool {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return true
		}
		for _, e := range entries {
			if budget--; budget < 0 {
				return false
			}
			switch {
			case e.Type().IsRegular() && strings.EqualFold(e.Name(), name):
				out = append(out, filepath.Join(dir, e.Name()))
			case e.IsDir():
				if !walk(filepath.Join(dir, e.Name())) {
					return false
				}
			}
		}
		return true
	}
	if !walk(root) {
		slog.Warn("agent policy write floor covers only part of the tree",
			"component", "sandbox", "root", root, "entries", agentPolicyWalkBudget)
	}
	return out
}

func coveredBy(set map[string]bool, path string) bool {
	for ancestor := range set {
		if PathAtOrUnder(path, ancestor) {
			return true
		}
	}
	return false
}

func coveredByList(list []string, path string) bool {
	for _, ancestor := range list {
		if PathAtOrUnder(path, ancestor) {
			return true
		}
	}
	return false
}
