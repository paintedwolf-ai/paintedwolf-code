package recovery

import (
	"context"
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
	if c.cfg.GitManager == nil {
		return nil
	}
	pg := m.PrivateGit
	if pg.HeadRef == "" && len(pg.Refs) == 0 {
		return nil
	}
	return applyPrivateGitSnapshotToView(ctx, c, view, m.WorkerID, pg)
}

func applyPrivateGitSnapshotToView(
	ctx context.Context,
	c *Coordinator,
	view *adapter.PreparedView,
	workerID string,
	pg checkpoint.PrivateGitSnapshot,
) error {
	if c.cfg.GitManager == nil || view == nil {
		return nil
	}
	if len(pg.Refs) == 0 && pg.HeadRef == "" {
		return nil
	}
	var primaryTrunk string
	for _, r := range view.Roots {
		if r.RootID == view.PrimaryRootID || primaryTrunk == "" {
			primaryTrunk = r.SourcePath
		}
	}
	if primaryTrunk != "" && workerID != "" {
		if _, finish, managed, err := privategit.PrepareViewGitOperation(ctx, primaryTrunk, workerID); err == nil && managed && finish != nil {
			_ = finish()
		}
	}

	gitExec := privategit.NewGitExecutor(c.cfg.GitManager, view.ViewID, privategit.RemotePolicy{})
	refNames := make([]string, 0, len(pg.Refs))
	for ref := range pg.Refs {
		refNames = append(refNames, ref)
	}
	sort.Strings(refNames)
	for _, ref := range refNames {
		oid := pg.Refs[ref]
		if oid == "" {
			continue
		}
		if _, err := gitExec.Run(ctx, ".", "update-ref", ref, oid); err != nil {
			return err
		}
	}
	headOID := pg.Refs[pg.HeadRef]
	if headOID == "" {
		headOID = pg.Refs["HEAD"]
	}
	if pg.HeadRef != "" {
		if headOID != "" {
			_, _ = gitExec.Run(ctx, ".", "update-ref", pg.HeadRef, headOID)
		}
		_, _ = gitExec.Run(ctx, ".", "symbolic-ref", "HEAD", pg.HeadRef)
	} else if headOID != "" {
		_, _ = gitExec.Run(ctx, ".", "update-ref", "--no-deref", "HEAD", headOID)
	}
	if headOID != "" {
		_, _ = gitExec.Run(ctx, ".", "read-tree", headOID)
	}
	return nil
}
