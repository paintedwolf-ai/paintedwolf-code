// Package viewconfine enforces direct-storage denial for mounted worker views. Everything it claims
// comes from a process started through the real launcher under the boundary a worker gets, reporting
// what the kernel answered, so a host that cannot enforce falls back with a typed reason.
package viewconfine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/denialprobe"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// probeBound ends a probe stuck on a mount. Running out proves nothing, so it counts as unavailable.
const probeBound = 60 * time.Second

const maxProbeOutput = 1 << 20

// Enforcer is the production adapter.ConfinementEnforcer.
type Enforcer struct {
	mu sync.Mutex
	// proven keeps a successful availability proof for the process's lifetime, as confine keeps an
	// applied boundary. Each view is still proven on its own; a failure is probed again.
	proven bool
}

// New returns an Enforcer with nothing yet proven.
func New() *Enforcer { return &Enforcer{} }

// Boundary is what the processes of view must not reach.
func Boundary(view *adapter.PreparedView) confine.ViewBoundary {
	if view == nil {
		return confine.ViewBoundary{}
	}
	var denied []string
	if view.BackingDir != "" {
		denied = append(denied, view.BackingDir)
	}
	return confine.ViewBoundary{DeniedPaths: append(denied, view.SourcePaths...), DeniedLoopbackPorts: append([]uint16(nil), view.ListenerPorts...)}
}

// Available proves, in a private scratch directory, that a confined process reaches a write root
// and is refused a denied directory beside it, and that it cannot mount a filesystem.
func (e *Enforcer) Available(ctx context.Context, probeCfg adapter.ProbeConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.proven {
		return nil
	}
	if err := proveInScratch(ctx, probeCfg.RuntimeDir); err != nil {
		return err
	}
	e.proven = true
	return nil
}

func proveInScratch(ctx context.Context, runtimeDir string) error {
	if runtimeDir != "" {
		if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
			return unavailable(err)
		}
	}
	base, err := os.MkdirTemp(runtimeDir, "confinement-proof-*")
	if err != nil {
		return unavailable(err)
	}
	defer func() { _ = os.RemoveAll(base) }()
	reach, denied := filepath.Join(base, "reach"), filepath.Join(base, "denied")
	for _, dir := range []string{reach, denied} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return unavailable(err)
		}
	}
	canary := filepath.Join(denied, "canary")
	if err := os.WriteFile(canary, []byte{0}, 0o600); err != nil {
		return unavailable(err)
	}
	box, err := workerBox([]string{reach}, confine.ViewBoundary{DeniedPaths: []string{denied}})
	if err != nil {
		return err
	}
	results, err := Launch(ctx, box, reach, []denialprobe.Step{
		{Kind: denialprobe.CreateFile, Path: filepath.Join(reach, probeName)},
		{Kind: denialprobe.ReadFile, Path: canary},
		{Kind: denialprobe.CreateFile, Path: filepath.Join(denied, probeName)},
		{Kind: denialprobe.Mount, Path: reach},
	})
	if err != nil {
		return unavailable(err)
	}
	return judge(results, 1)
}

// VerifyView proves enforcement against one prepared view: its roots work through the kernel client
// while the process is refused the view's storage, its sources, its listeners and its own unmount.
func (e *Enforcer) VerifyView(ctx context.Context, view *adapter.PreparedView) error {
	return verify(ctx, view, Boundary(view))
}

func verify(ctx context.Context, view *adapter.PreparedView, boundary confine.ViewBoundary) error {
	if view == nil || view.PrimaryWorkPath == "" {
		return unavailable(errors.New("no prepared view to verify"))
	}
	roots := make([]string, 0, len(view.Roots))
	for _, root := range view.Roots {
		roots = append(roots, root.WorkspacePath)
	}
	box, err := workerBox(roots, boundary)
	if err != nil {
		return err
	}
	// Only the primary root takes a write: a stat of the others shows they are reached.
	steps := []denialprobe.Step{{Kind: denialprobe.CreateFile, Path: filepath.Join(view.PrimaryWorkPath, probeName)}}
	for _, root := range roots {
		steps = append(steps, denialprobe.Step{Kind: denialprobe.Stat, Path: root})
	}
	if len(view.ListenerPorts) > 0 {
		control, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return unavailable(err)
		}
		defer func() { _ = control.Close() }()
		steps = append(steps, denialprobe.Step{Kind: denialprobe.Connect, Port: uint16(control.Addr().(*net.TCPAddr).Port)}) //nolint:gosec // a listener's own port.
	}
	allowed := len(steps)
	for _, path := range boundary.DeniedPaths {
		if entry := FirstEntry(path); entry != "" {
			steps = append(steps, denialprobe.Step{Kind: denialprobe.Stat, Path: entry})
		}
		steps = append(steps, denialprobe.Step{Kind: denialprobe.CreateFile, Path: filepath.Join(path, probeName)})
	}
	for _, port := range view.ListenerPorts {
		steps = append(steps, denialprobe.Step{Kind: denialprobe.Connect, Port: port})
	}
	steps = append(steps, denialprobe.Step{Kind: denialprobe.Unmount, Path: view.PrimaryWorkPath})

	results, err := Launch(ctx, box, view.PrimaryWorkPath, steps)
	if err != nil {
		return unavailable(err)
	}
	if err := judge(results, allowed); err != nil {
		return err
	}
	for _, root := range roots {
		if mounted, err := adapter.MountBoundaryPresent(root); err != nil || !mounted {
			return failed(fmt.Sprintf("root %s left its mount after the probe: %v", root, err))
		}
	}
	return nil
}

// FirstEntry names something that exists inside dir, or nothing when it is empty. A masked directory
// lists as empty rather than failing, so only an entry known to exist can show that it is hidden.
func FirstEntry(dir string) string {
	file, err := os.Open(dir)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	names, err := file.Readdirnames(1)
	if err != nil || len(names) == 0 {
		return ""
	}
	return filepath.Join(dir, names[0])
}

// probeName is what the probe tries to create, and removes when it can.
const probeName = ".pw-confinement-probe"

// workerBox is the boundary of a worker's command, reaching loopback as broadly as a grant can so
// that a refused listener is the view's rule, not the absence of a grant.
func workerBox(roots []string, boundary confine.ViewBoundary) (*confine.Confinement, error) {
	box, ok := confine.DefaultConfinement(confine.Request{
		Roots: roots, ReadRoots: roots, Egress: confine.EgressDeny, View: boundary,
		LoopbackConnect: len(boundary.DeniedLoopbackPorts) > 0,
	})
	if !ok || box == nil {
		return nil, unavailable(errors.New("this host applies no confinement to a worker's commands"))
	}
	return box, nil
}

// Launch runs steps in one process through the launcher under box, entering dir. A boundary may end
// the process at a mount step, so results stop at the step that ended it.
func Launch(ctx context.Context, box *confine.Confinement, dir string, steps []denialprobe.Step) ([]denialprobe.Result, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	plan, err := denialprobe.Encode(steps)
	if err != nil {
		return nil, err
	}
	stdout, stderr, _, err := execpkg.RunSeparate(ctx, self, []string{denialprobe.Flag, plan}, execpkg.ExecOpts{
		Launch: execpkg.AgentLaunch(execpkg.LaunchAgentCommand, "view_confinement_probe", box).WithPathAuthority(confine.NativeLaunchPaths(dir)), Dir: dir,
		Timeout: probeBound, MaxOutputBytes: maxProbeOutput,
	})
	if err != nil {
		return nil, fmt.Errorf("launch confinement probe: %w (%s)", err, bytes.TrimSpace(stderr))
	}
	results := make([]denialprobe.Result, 0, len(steps))
	lines := bufio.NewScanner(bytes.NewReader(stdout))
	for lines.Scan() {
		var result denialprobe.Result
		if err := json.Unmarshal(lines.Bytes(), &result); err != nil {
			return nil, fmt.Errorf("read confinement probe: %w", err)
		}
		results = append(results, result)
	}
	if len(results) < len(steps) && (len(results) != len(steps)-1 || !endsProcess(steps[len(results)].Kind)) {
		return nil, fmt.Errorf("confinement probe answered %d of %d steps (%s)", len(results), len(steps), bytes.TrimSpace(stderr))
	}
	return results, nil
}

// endsProcess names the steps a boundary may answer by ending the process.
func endsProcess(kind denialprobe.Kind) bool {
	return kind == denialprobe.Mount || kind == denialprobe.Unmount
}

// judge requires the first allowed results to have worked and the rest to have been refused.
func judge(results []denialprobe.Result, allowed int) error {
	for i, result := range results {
		switch {
		case i < allowed && !result.Succeeded():
			return unavailable(fmt.Errorf("a confined process could not %s %s: errno %d", result.Kind, target(result), result.Errno))
		case i >= allowed && !result.Denied():
			return failed(fmt.Sprintf("a confined process was not refused %s %s: errno %d", result.Kind, target(result), result.Errno))
		}
	}
	return nil
}

func target(result denialprobe.Result) string {
	if result.Path != "" {
		return result.Path
	}
	return fmt.Sprintf("port %d", result.Port)
}

func unavailable(err error) error {
	return &adapter.AdapterError{Op: "verify_confinement", ReasonCode: adapter.ReasonConfinementUnavailable, Diagnostic: err.Error(), Cause: err}
}

func failed(diagnostic string) error {
	return &adapter.AdapterError{Op: "verify_confinement", ReasonCode: adapter.ReasonBackingConfinementFailed, Diagnostic: diagnostic}
}
