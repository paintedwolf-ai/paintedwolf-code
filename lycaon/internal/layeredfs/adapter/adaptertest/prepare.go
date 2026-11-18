package adaptertest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

func (a *Mounted) Prepare(ctx context.Context, req adapter.PrepareRequest) (*adapter.PreparedView, error) {
	narrowed, err := adapter.ValidateWorkspaceSelection(req.Grants, req.Selection)
	if err != nil {
		return nil, err
	}
	runtimeViewDir, backingDir, err := viewDirectories(req.ViewID, req.RuntimeDir, req.BackingDir)
	if err != nil {
		return nil, &adapter.AdapterError{AdapterID: a.cfg.AdapterID, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	rootSpecs, preparedRoots, denyPaths, primaryID, primaryPath, err := prepareRoots(runtimeViewDir, req.TargetRootDirs, narrowed, req.HiddenPaths, req.HiddenNames)
	if err != nil {
		_ = os.RemoveAll(runtimeViewDir)
		return nil, err
	}

	core, resumed, err := adapter.OpenViewCore(req, layeredfs.Config{
		ViewID:          req.ViewID,
		BackingDir:      backingDir,
		Roots:           rootSpecs,
		ActiveRootID:    primaryID,
		ParentLayers:    req.ParentLayers,
		GitInterceptor:  req.GitInterceptor,
		ObservationSink: req.ObservationSink,
	})
	if err != nil {
		_ = os.RemoveAll(runtimeViewDir)
		return nil, &adapter.AdapterError{AdapterID: a.cfg.AdapterID, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: err.Error(), Cause: err}
	}

	if a.cfg.MountViewFn != nil {
		if mErr := a.cfg.MountViewFn(ctx, req.ViewID, preparedRoots); mErr != nil {
			_ = core.ReleaseView()
			_ = os.RemoveAll(runtimeViewDir)
			return nil, &adapter.AdapterError{AdapterID: a.cfg.AdapterID, Op: "prepare", ReasonCode: adapter.ReasonMountDenied, Diagnostic: mErr.Error(), Cause: mErr}
		}
	}

	view := &adapter.PreparedView{
		Resumed:         resumed,
		ViewID:          req.ViewID,
		AdapterID:       a.cfg.AdapterID,
		Facts:           a.facts,
		Roots:           preparedRoots,
		PrimaryRootID:   primaryID,
		PrimaryWorkPath: primaryPath,
		BackingDir:      backingDir,
		RuntimeViewDir:  runtimeViewDir,
		SourcePaths:     append([]string(nil), denyPaths...),
		TaskPathHints:   append([]string(nil), req.TaskPathHints...),
		Core:            core,
	}

	a.mu.Lock()
	a.views[req.ViewID] = &mountedView{
		view:    view,
		deny:    denyPaths,
		quiesce: req.QuiesceHook,
		flush:   req.FlushClientHook,
	}
	a.mu.Unlock()
	return view, nil
}

func prepareRoots(runtimeViewDir string, targetDirs map[string]string, narrowed []adapter.RootGrant, hidden, hiddenNames []string) ([]layeredfs.RootSpec, []adapter.PreparedRoot, []string, string, string, error) {
	var specs []layeredfs.RootSpec
	var prepared []adapter.PreparedRoot
	var denyPaths []string
	var primaryID, primaryPath string

	for _, g := range narrowed {
		mntPath := filepath.Join(runtimeViewDir, "mounts", g.RootID)
		if custom := strings.TrimSpace(targetDirs[g.RootID]); custom != "" {
			mntPath = filepath.Clean(custom)
		}
		if err := os.MkdirAll(mntPath, 0o750); err != nil {
			return nil, nil, nil, "", "", err
		}
		specs = append(specs, layeredfs.RootSpec{
			ID:          g.RootID,
			Label:       g.Label,
			TrunkPath:   g.SourcePath,
			Subroot:     g.Subroot,
			IsPrimary:   g.IsPrimary,
			Hidden:      adapter.HiddenRelsUnder(g.SourcePath, hidden),
			HiddenNames: hiddenNames,
		})
		prepared = append(prepared, adapter.PreparedRoot{
			RootID:        g.RootID,
			Label:         g.Label,
			SourcePath:    g.SourcePath,
			Subroot:       g.Subroot,
			WorkspacePath: mntPath,
			IsPrimary:     g.IsPrimary,
		})
		denyPaths = append(denyPaths, g.SourcePath)
		if g.IsPrimary && primaryID == "" {
			primaryID = g.RootID
			primaryPath = mntPath
		}
	}
	return specs, prepared, denyPaths, primaryID, primaryPath, nil
}

func viewDirectories(viewID, runtimeDir, backingDir string) (string, string, error) {
	if viewID == "" || runtimeDir == "" {
		return "", "", fmt.Errorf("fixture view and runtime directory are required")
	}
	viewDir := filepath.Join(runtimeDir, "views", viewID)
	if backingDir == "" {
		backingDir = filepath.Join(viewDir, "backing")
	}
	for _, dir := range []string{viewDir, backingDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", "", err
		}
	}
	return viewDir, backingDir, nil
}
