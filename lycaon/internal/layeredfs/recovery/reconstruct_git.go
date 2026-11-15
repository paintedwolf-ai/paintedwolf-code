package recovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// PrivateGitBinder is implemented by mounted adapters (such as nfsdarwin.Adapter)
// that bind a PreparedView's mount to a privategit.Manager.
type PrivateGitBinder interface {
	BindPrivateGit(viewID, workerID string, mgr *privategit.Manager) error
}

func materializedPathForRoot(view *adapter.PreparedView, qp layeredfs.QualifiedPath) string {
	if view == nil || view.Facts.PreparationKind != adapter.PreparationMaterialized {
		return ""
	}
	for _, r := range view.Roots {
		if r.RootID == qp.RootID && r.WorkspacePath != "" {
			return filepath.Join(r.WorkspacePath, filepath.FromSlash(qp.Rel))
		}
	}
	return ""
}

func ensureParentDirsInCore(core *layeredfs.Core, qp layeredfs.QualifiedPath, matPath string) {
	dir := filepath.ToSlash(filepath.Dir(qp.Rel))
	if dir == "" || dir == "." {
		return
	}
	parts := strings.Split(dir, "/")
	cur := ""
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if cur == "" {
			cur = p
		} else {
			cur = cur + "/" + p
		}
		_, _ = core.Mkdir(layeredfs.QualifiedPath{RootID: qp.RootID, Rel: cur}, 0o755)
	}
	if matPath != "" {
		_ = os.MkdirAll(filepath.Dir(matPath), 0o755)
	}
}

func mustRel(base, target string) string {
	r, err := filepath.Rel(filepath.FromSlash(base), filepath.FromSlash(target))
	if err != nil {
		return filepath.FromSlash(target)
	}
	return r
}

func restorePrivateGitToView(
	ctx context.Context,
	c *Coordinator,
	view *adapter.PreparedView,
	m checkpoint.Manifest,
) error {
	return applyPrivateGitSnapshotToView(ctx, c, view, m.WorkerID, m.PrivateGit)
}

// applyPrivateGitSnapshotToView restores each root's refs into that root's own private repository.
func applyPrivateGitSnapshotToView(
	ctx context.Context,
	c *Coordinator,
	view *adapter.PreparedView,
	_ string,
	pg checkpoint.PrivateGitSnapshot,
) error {
	if c.cfg.GitManager == nil || view == nil {
		return nil
	}
	secondaries := map[string]bool{}
	for _, r := range view.Roots {
		if r.RootID != view.PrimaryRootID {
			secondaries[r.RootID] = true
		}
	}
	if err := restoreRootGitRefs(ctx, c, view.ViewID, pg.HeadRef, pg.Refs); err != nil {
		return err
	}
	for rootID, root := range pg.Roots {
		if !secondaries[rootID] {
			continue
		}
		viewID := privategit.SecondaryViewID(view.ViewID, rootID)
		if err := restoreRootGitRefs(ctx, c, viewID, root.HeadRef, root.Refs); err != nil {
			return err
		}
	}
	return nil
}

func restoreRootGitRefs(ctx context.Context, c *Coordinator, viewID, headRef string, refs map[string]string) error {
	if len(refs) == 0 && headRef == "" {
		return nil
	}
	if _, err := c.cfg.GitManager.InitRepo(ctx, viewID, "."); err != nil {
		return fmt.Errorf("recovery: prepare private git on %s: %w", viewID, err)
	}

	gitExec := privategit.NewGitExecutor(c.cfg.GitManager, viewID, privategit.RemotePolicy{})
	refNames := make([]string, 0, len(refs))
	for ref := range refs {
		refNames = append(refNames, ref)
	}
	sort.Strings(refNames)
	for _, ref := range refNames {
		oid := refs[ref]
		if oid == "" {
			continue
		}
		if _, err := gitExec.Run(ctx, ".", "update-ref", ref, oid); err != nil {
			return err
		}
	}
	headOID := refs[headRef]
	if headOID == "" {
		headOID = refs["HEAD"]
	}
	if headRef != "" {
		if headOID != "" {
			_, _ = gitExec.Run(ctx, ".", "update-ref", headRef, headOID)
		}
		_, _ = gitExec.Run(ctx, ".", "symbolic-ref", "HEAD", headRef)
	} else if headOID != "" {
		_, _ = gitExec.Run(ctx, ".", "update-ref", "--no-deref", "HEAD", headOID)
	}
	if headOID != "" {
		_, _ = gitExec.Run(ctx, ".", "read-tree", headOID)
	}
	return nil
}
