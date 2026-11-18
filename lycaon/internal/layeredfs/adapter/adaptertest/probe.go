package adaptertest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

func (a *Mounted) Probe(ctx context.Context, cfg adapter.ProbeConfig) (adapter.ProbeResult, error) {
	result := adapter.ProbeResult{AdapterID: a.ID(), Attempts: 1}
	fail := func(reason adapter.ReasonCode, err error) (adapter.ProbeResult, error) {
		result.ReasonCode, result.Diagnostic = reason, err.Error()
		return result, a.failure("probe", reason, err)
	}
	if err := ctx.Err(); err != nil {
		return fail(adapter.ReasonContextCanceled, err)
	}
	if a.cfg.MountProbeFn == nil && a.cfg.TargetOS != "" && a.cfg.TargetOS != runtime.GOOS {
		return fail(adapter.ReasonUnsupportedPlatform, fmt.Errorf("fixture requires %s", a.cfg.TargetOS))
	}
	if cfg.RuntimeDir != "" {
		if err := os.MkdirAll(cfg.RuntimeDir, 0o700); err != nil {
			return fail(adapter.ReasonMountDenied, err)
		}
	}
	root, err := os.MkdirTemp(cfg.RuntimeDir, "mounted-fixture-probe-")
	if err != nil {
		return fail(adapter.ReasonMountDenied, err)
	}
	defer os.RemoveAll(root)
	source, backing, mount := filepath.Join(root, "source"), filepath.Join(root, "backing"), filepath.Join(root, "mount")
	for _, dir := range []string{source, backing, mount} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail(adapter.ReasonMountDenied, err)
		}
	}
	if a.cfg.MountProbeFn != nil {
		reason, diagnostic, err := a.cfg.MountProbeFn(ctx, mount, backing)
		if err != nil || reason != adapter.ReasonNone {
			if reason == adapter.ReasonNone {
				reason = adapter.ReasonMountDenied
			}
			if err == nil {
				err = fmt.Errorf("%s", diagnostic)
			}
			return fail(reason, err)
		}
	} else if a.cfg.MountViewFn == nil {
		return fail(adapter.ReasonMountHelperUnavailable, fmt.Errorf("fixture mount outcome is not configured"))
	}
	result.MountExercised = true
	core, err := layeredfs.New(layeredfs.Config{ViewID: "probe", BackingDir: backing, Roots: []layeredfs.RootSpec{{ID: "root", TrunkPath: source, IsPrimary: true}}})
	if err != nil {
		return fail(adapter.ReasonProbeReadWriteFailed, err)
	}
	defer core.ReleaseView()
	path := layeredfs.QualifiedPath{RootID: "root", Rel: "probe.txt"}
	if _, err := core.WriteFile(path, []byte("probe"), 0o600); err != nil {
		return fail(adapter.ReasonProbeReadWriteFailed, err)
	}
	result.WriteExercised = true
	data, err := core.ReadFile(path)
	if err != nil {
		return fail(adapter.ReasonProbeReadWriteFailed, err)
	}
	if string(data) != "probe" {
		return fail(adapter.ReasonProbeReadWriteFailed, fmt.Errorf("fixture probe read mismatch"))
	}
	result.ReadExercised = true
	if a.cfg.VerifyBackingDenialFn != nil {
		denied, err := a.cfg.VerifyBackingDenialFn(ctx, backing)
		if err != nil {
			return fail(adapter.ReasonBackingConfinementFailed, err)
		}
		if !denied {
			return fail(adapter.ReasonBackingConfinementFailed, fmt.Errorf("fixture backing access was not denied"))
		}
	}
	result.BackingDenialVerified = true
	if err := core.ReleaseView(); err != nil {
		return fail(adapter.ReasonCleanupFailed, err)
	}
	if err := os.RemoveAll(root); err != nil {
		return fail(adapter.ReasonCleanupFailed, err)
	}
	result.CleanedUp, result.Supported = true, true
	return result, nil
}

func (a *Mounted) failure(op string, reason adapter.ReasonCode, err error) error {
	return &adapter.AdapterError{AdapterID: a.ID(), Op: op, ReasonCode: reason, Diagnostic: err.Error(), Cause: err}
}
