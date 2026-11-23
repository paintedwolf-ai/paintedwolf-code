package adaptertest

import (
	"errors"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// BindPrivateGit records a fixture identity for each active root.
func (a *Mounted) BindPrivateGit(viewID, workerID string, mgr *privategit.Manager, authorizedGitDirs ...string) error {
	if mgr == nil {
		return errors.New("adapter: nil privategit.Manager")
	}
	a.mu.RLock()
	st, ok := a.views[viewID]
	a.mu.RUnlock()
	if !ok {
		return &adapter.AdapterError{AdapterID: a.cfg.AdapterID, Op: "bind_privategit", ReasonCode: adapter.ReasonViewNotFound, Diagnostic: viewID}
	}
	bindingFor := func(r adapter.PreparedRoot) privategit.MountedViewBinding {
		return privategit.MountedViewBinding{
			ViewID: viewID, WorkerID: workerID, RootID: r.RootID,
			TrunkDir: r.SourcePath, MountPath: r.WorkspacePath, AdapterID: string(a.cfg.AdapterID),
			Core: st.view.Core, MountID: "fixture:" + viewID, Active: true,
			IsActive: func() bool { return a.IsViewActive(viewID) },
		}
	}
	for _, r := range st.view.Roots {
		if r.RootID != st.view.PrimaryRootID {
			continue
		}
		if err := mgr.MountedClaimWithBinding(bindingFor(r), authorizedGitDirs...); err != nil {
			return err
		}
	}
	for _, r := range st.view.Roots {
		if r.RootID == st.view.PrimaryRootID {
			continue
		}
		if err := mgr.BindSecondaryRoot(bindingFor(r), authorizedGitDirs...); err != nil {
			return err
		}
	}
	return nil
}
