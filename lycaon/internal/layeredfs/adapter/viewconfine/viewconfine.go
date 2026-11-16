// Package viewconfine enforces what a worker view's processes must not reach. Everything it claims
// comes from a process started through the real launcher under the boundary a worker gets, reporting
// what the kernel answered, so a host that cannot enforce falls back with a typed reason.
package viewconfine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/denialprobe"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/protectedpath"
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

// Boundary is what the processes of a view must not reach: every other worker's branch beside it
// and, for a mounted view, its storage, its sources and its listener. Only its own roots stay open,
// with the object stores its repositories borrow from readable.
func Boundary(view *adapter.PreparedView) confine.ViewBoundary {
	if view == nil {
		return confine.ViewBoundary{}
	}
	var boundary confine.ViewBoundary
	if view.Facts.PreparationKind == adapter.PreparationMounted {
		boundary = mountedStorageBoundary(view)
	}
	base := enginepaths.WorkerBranchesRootOf(view.PrimaryWorkPath)
	if base == "" {
		return boundary
	}
	boundary.DeniedPaths = append(boundary.DeniedPaths, base)
	boundary.ReopenedReadPaths = append(boundary.ReopenedReadPaths, privategit.RetainedObjectsDir(enginepaths.PrivateGitDirUnder(base)))
	if view.Facts.PreparationKind == adapter.PreparationMaterialized && view.RuntimeViewDir != "" {
		boundary.ReopenedReadPaths = append(boundary.ReopenedReadPaths, adapter.RetainedGitObjectsDirOfView(view.RuntimeViewDir))
	}
	for _, root := range view.Roots {
		boundary.ReopenedPaths = append(boundary.ReopenedPaths, root.WorkspacePath)
		if root.PristinePath != "" {
			boundary.ReopenedReadPaths = append(boundary.ReopenedReadPaths, root.PristinePath)
		}
	}
	return boundary
}

func mountedStorageBoundary(view *adapter.PreparedView) confine.ViewBoundary {
	boundary := confine.ViewBoundary{DeniedLoopbackPorts: slices.Clone(view.ListenerPorts), DeniedPaths: slices.Clone(view.SourcePaths)}
	if view.BackingDir != "" {
		boundary.DeniedPaths = append(boundary.DeniedPaths, view.BackingDir)
	}
	policyNames := agentPolicyNames()
	for _, root := range view.Roots {
		vr := confine.ViewRootBoundary{
			RootID:        root.RootID,
			WorkspacePath: root.WorkspacePath,
			SourcePath:    root.SourcePath,
		}
		if view.Core != nil {
			inv := view.Core.PolicyInventory(root.RootID, policyNames...)
			vr.Inventory = confine.PolicyFileInventory{
				Files:      inv.Files,
				Removed:    inv.Removed,
				Complete:   inv.Complete,
				Generation: inv.Generation,
				ReadDirs:   inv.ReadDirs,
			}
		}
		boundary.Roots = append(boundary.Roots, vr)
	}
	return boundary
}

func agentPolicyNames() []string {
	var names []string
	for _, loc := range protectedpath.AgentPolicyLocations() {
		if loc.Name != "" {
			names = append(names, loc.Name)
		}
	}
	return names
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

const maxEntriesScanned = 64

// probeName is what the probe tries to create, and removes when it can.
const probeName = ".pw-confinement-probe"

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
