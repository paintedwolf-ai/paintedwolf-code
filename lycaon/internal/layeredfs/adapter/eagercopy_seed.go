package adapter

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

type seedOutcome struct {
	rootSpecs       []layeredfs.RootSpec
	preparedRoots   []PreparedRoot
	denyPaths       []string
	primaryID       string
	primaryPath     string
	records         []statRecord
	seededRepos     map[string]SeedGitInitResult
	gitInterceptMap map[layeredfs.QualifiedPath]layeredfs.QualifiedPath
	gitStore        *privategit.RetainedObjectStore
	gitLeases       []string
	stats           MaterializeStats
	logicalBytes    int64
}

type seededDirMode struct {
	dstPath string
	rootID  string
	rel     string
	perm    fs.FileMode
	modTime time.Time
}

type seedAccumulator struct {
	ctx            context.Context
	req            PrepareRequest
	runtimeViewDir string
	narrowed       []RootGrant
	gitStores      *gitStoreRegistry
	gitReposBuilt  int
	seededDirs     []seededDirMode
	clone          FileCloner
	meter          contentMeter
	out            *seedOutcome
}

func seedSelectedRoots(
	ctx context.Context,
	req PrepareRequest,
	runtimeViewDir, pristineDir string,
	narrowed []RootGrant,
	gitStores *gitStoreRegistry,
) (*seedOutcome, error) {
	out := &seedOutcome{
		seededRepos:     make(map[string]SeedGitInitResult),
		gitInterceptMap: make(map[layeredfs.QualifiedPath]layeredfs.QualifiedPath),
	}
	clone := req.CloneFile
	if clone == nil {
		clone = fileclone.Clone
	}
	acc := &seedAccumulator{
		ctx: ctx, req: req, runtimeViewDir: runtimeViewDir,
		narrowed: narrowed, gitStores: gitStores, clone: clone, out: out,
	}
	defer func() { out.stats.ContentBytesRead = acc.meter.read.Load() }()

	for _, g := range narrowed {
		seedDir := filepath.Join(runtimeViewDir, "seeds", g.RootID)
		if custom := strings.TrimSpace(req.TargetRootDirs[g.RootID]); custom != "" {
			seedDir = filepath.Clean(custom)
		}
		pristine := filepath.Join(pristineDir, g.RootID)
		for _, dir := range []string{seedDir, pristine} {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return out, err
			}
		}
		if err := acc.walkAndSeedRoot(g, seedDir, pristine); err != nil {
			return out, err
		}
		out.rootSpecs = append(out.rootSpecs, layeredfs.RootSpec{
			ID: g.RootID, Label: g.Label, TrunkPath: seedDir, Subroot: g.Subroot, IsPrimary: g.IsPrimary,
		})
		out.preparedRoots = append(out.preparedRoots, PreparedRoot{
			RootID: g.RootID, Label: g.Label, SourcePath: g.SourcePath, Subroot: g.Subroot,
			WorkspacePath: seedDir, PristinePath: pristine, IsPrimary: g.IsPrimary,
		})
		out.denyPaths = append(out.denyPaths, g.SourcePath)
		if g.IsPrimary && out.primaryID == "" {
			out.primaryID, out.primaryPath = g.RootID, seedDir
		}
	}
	if len(out.gitInterceptMap) > 0 {
		out.rootSpecs = append(out.rootSpecs, layeredfs.RootSpec{
			ID: "__privategit__", Label: "privategit", TrunkPath: filepath.Join(runtimeViewDir, "privategit"),
		})
	}
	return out, nil
}

func (a *seedAccumulator) walkAndSeedRoot(g RootGrant, seedDir, pristineDir string) error {
	srcRoot := filepath.Clean(g.SourcePath)
	a.seededDirs = a.seededDirs[:0]
	walkErr := filepath.WalkDir(srcRoot, func(srcPath string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := a.ctx.Err(); err != nil {
			return &AdapterError{AdapterID: AdapterEagerCopy, Op: "prepare", ReasonCode: ReasonContextCanceled, Diagnostic: err.Error(), Cause: err}
		}
		relOS, err := filepath.Rel(srcRoot, srcPath)
		if err != nil || relOS == "." {
			return err
		}
		rel := filepath.ToSlash(relOS)
		if d.Name() == ".lycaon" && d.IsDir() {
			return filepath.SkipDir
		}
		if d.Name() == ".git" {
			if err := a.handleDiscoveredGit(g, rel, srcPath, d); err != nil {
				return err
			}
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		return a.seedSingleEntry(g, rel, srcPath, filepath.Join(seedDir, relOS), filepath.Join(pristineDir, relOS))
	})
	if walkErr != nil {
		return walkErr
	}
	for i := len(a.seededDirs) - 1; i >= 0; i-- {
		dm := a.seededDirs[i]
		if err := os.Chtimes(dm.dstPath, dm.modTime, dm.modTime); err != nil {
			return err
		}
		if err := chmodPath(dm.dstPath, dm.perm); err != nil {
			return err
		}
		info, err := os.Lstat(dm.dstPath)
		if err != nil {
			return err
		}
		a.out.records = append(a.out.records, recordStat(dm.rootID, dm.rel, layeredfs.EntryDirectory, info))
	}
	return nil
}

func (a *seedAccumulator) handleDiscoveredGit(g RootGrant, gitRel, srcGitPath string, d fs.DirEntry) error {
	if a.out.gitStore == nil && a.req.GitObjectStore != nil {
		a.out.gitStore = a.req.GitObjectStore
	}
	if a.out.gitStore == nil && a.gitStores != nil {
		store, err := a.gitStores.storeForPrepare(a.req.RuntimeDir, a.narrowed)
		if err != nil {
			return err
		}
		a.out.gitStore = store
	}
	repoRel := filepath.ToSlash(filepath.Dir(gitRel))
	if repoRel == "" {
		repoRel = "."
	}
	privGitRel := filepath.ToSlash(filepath.Join(g.RootID, gitRel))
	privGitDir := filepath.Join(a.runtimeViewDir, "privategit", filepath.FromSlash(privGitRel))
	authDirs := make([]string, 0, len(a.req.Grants)+len(a.narrowed))
	for _, rg := range append(append([]RootGrant(nil), a.req.Grants...), a.narrowed...) {
		authDirs = append(authDirs, rg.SourcePath)
	}
	spec := SeedGitRepoSpec{
		ViewID: a.req.ViewID, RootID: g.RootID, RepoRelPath: repoRel, GitEntryRel: gitRel,
		SourceGitPath: srcGitPath, PrivateGitDir: privGitDir, IsGitlink: !d.IsDir(),
		AuthorizedGitDirs: authDirs, RetainedStore: a.out.gitStore,
	}
	if budget := a.req.CopyBudget; budget != nil {
		remaining := max(budget.Limit-a.spentCost(), 1)
		spec.MaxBytes = remaining
		spec.MaxEntries = max(remaining/max(budget.EntryCost, 1), 1)
	}
	if d.Type()&fs.ModeSymlink != 0 {
		return &AdapterError{AdapterID: AdapterEagerCopy, Op: "seed_git", ReasonCode: ReasonUnauthorizedRoot, Diagnostic: ".git symlink is forbidden", Cause: privategit.ErrUnauthorizedGitTarget}
	}
	if spec.IsGitlink {
		raw, err := os.ReadFile(srcGitPath)
		if err != nil {
			return err
		}
		spec.GitlinkContent = strings.TrimSpace(string(raw))
	}
	initFn := a.req.SeedGitInit
	if initFn == nil {
		initFn = defaultSeedGitInitializer
	}
	res, err := initFn(a.ctx, spec)
	if err != nil {
		if errors.Is(err, privategit.ErrSeedLimitExceeded) {
			return a.overrun(g.RootID)
		}
		return err
	}
	if res.LeaseID != "" {
		a.out.gitLeases = append(a.out.gitLeases, res.LeaseID)
	}
	a.gitReposBuilt++
	if err := a.chargeEntries(g.RootID, res.AddedEntries); err != nil {
		return err
	}
	if err := a.chargeCopied(g.RootID, res.AddedBytes); err != nil {
		return err
	}
	a.out.seededRepos[g.RootID+":"+repoRel] = res
	a.out.gitInterceptMap[layeredfs.QualifiedPath{RootID: g.RootID, Rel: gitRel}] = layeredfs.QualifiedPath{RootID: "__privategit__", Rel: privGitRel}
	return nil
}

func (a *seedAccumulator) seedSingleEntry(g RootGrant, rel, srcPath, dstPath, pristinePath string) error {
	info, err := os.Lstat(srcPath)
	if err != nil {
		return err
	}
	mode := info.Mode()
	xattrs, err := readPathXattrs(srcPath)
	if err != nil {
		return err
	}
	switch {
	case mode.IsDir():
		return a.seedDirectory(g.RootID, rel, dstPath, pristinePath, info, xattrs)
	case mode&os.ModeSymlink != 0:
		return a.seedSymlink(g.RootID, rel, srcPath, dstPath, pristinePath, xattrs)
	case mode.IsRegular():
		return a.seedRegularFile(g.RootID, rel, srcPath, dstPath, pristinePath, info, xattrs)
	default:
		return nil
	}
}

func (a *seedAccumulator) seedDirectory(rootID, rel, dstPath, pristinePath string, info fs.FileInfo, xattrs map[string][]byte) error {
	if err := a.chargeEntries(rootID, 1); err != nil {
		return err
	}
	tempPerm := info.Mode().Perm() | 0o700
	if err := os.MkdirAll(dstPath, tempPerm); err != nil {
		return err
	}
	if err := os.Chmod(dstPath, tempPerm); err != nil {
		return err
	}
	if err := writePathXattrs(dstPath, xattrs); err != nil {
		return err
	}
	// Pristine directories stay traversable and removable. A directory's mode is in the manifest.
	if err := os.MkdirAll(pristinePath, 0o700); err != nil {
		return err
	}
	if err := writePathXattrs(pristinePath, xattrs); err != nil {
		return err
	}
	a.seededDirs = append(a.seededDirs, seededDirMode{
		dstPath: dstPath, rootID: rootID, rel: rel, perm: info.Mode().Perm(), modTime: info.ModTime(),
	})
	return nil
}

func (a *seedAccumulator) seedSymlink(rootID, rel, srcPath, dstPath, pristinePath string, xattrs map[string][]byte) error {
	target, err := os.Readlink(srcPath)
	if err != nil {
		return err
	}
	if err := a.chargeEntries(rootID, 1); err != nil {
		return err
	}
	for _, path := range []string{dstPath, pristinePath} {
		if err := os.Symlink(target, path); err != nil {
			return err
		}
		if err := writePathXattrs(path, xattrs); err != nil {
			return err
		}
	}
	info, err := os.Lstat(dstPath)
	if err != nil {
		return err
	}
	rec := recordStat(rootID, rel, layeredfs.EntrySymlink, info)
	rec.LinkTarget = target
	a.out.records = append(a.out.records, rec)
	return nil
}

// seedRegularFile builds both trees for one file: clone where the volume allows it, and copy where it
// does not. The working file comes from the source and the pristine one from the working file, so the
// two cannot differ whatever the source does meanwhile. Nothing here hashes.
func (a *seedAccumulator) seedRegularFile(rootID, rel, srcPath, dstPath, pristinePath string, info fs.FileInfo, xattrs map[string][]byte) error {
	if err := a.chargeEntries(rootID, 1); err != nil {
		return err
	}
	size := max(info.Size(), 0)
	a.out.logicalBytes += size
	if err := a.cloneOrCopy(rootID, srcPath, dstPath, info.Mode().Perm()|0o600, size); err != nil {
		return err
	}
	if err := writePathXattrs(dstPath, xattrs); err != nil {
		return err
	}
	if err := chmodPath(dstPath, info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chtimes(dstPath, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	if err := a.seedPristineFile(rootID, dstPath, pristinePath, info, xattrs); err != nil {
		return err
	}
	final, err := os.Lstat(dstPath)
	if err != nil {
		return err
	}
	a.out.records = append(a.out.records, recordStat(rootID, rel, layeredfs.EntryRegular, final))
	return nil
}

func (a *seedAccumulator) seedPristineFile(rootID, dstPath, pristinePath string, info fs.FileInfo, xattrs map[string][]byte) error {
	current, err := os.Lstat(dstPath)
	if err != nil {
		return err
	}
	if err := a.cloneOrCopy(rootID, dstPath, pristinePath, 0o600, max(current.Size(), 0)); err != nil {
		return err
	}
	if err := os.Chmod(pristinePath, 0o600); err != nil {
		return err
	}
	if err := writePathXattrs(pristinePath, xattrs); err != nil {
		return err
	}
	if err := os.Chtimes(pristinePath, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	return os.Chmod(pristinePath, 0o400)
}

// cloneOrCopy charges a copy before its first byte moves, so a copy past the budget never starts.
func (a *seedAccumulator) cloneOrCopy(rootID, src, dst string, mode fs.FileMode, size int64) error {
	cloned, err := a.clone(src, dst)
	if err != nil {
		return err
	}
	if cloned {
		a.out.stats.ClonedFiles++
		a.out.stats.ClonedBytes += size
		return nil
	}
	if err := a.chargeCopied(rootID, size); err != nil {
		return err
	}
	var limit int64
	if budget := a.req.CopyBudget; budget != nil {
		limit = max(budget.Limit-a.spentCost(), 0) + size + 1
	}
	n, err := a.meter.copyFile(a.ctx, src, dst, mode, limit)
	if err != nil {
		_ = os.Remove(dst)
		return err
	}
	a.out.stats.CopiedFiles++
	return a.chargeCopied(rootID, n-size)
}

func (a *seedAccumulator) spentCost() int64 {
	budget := a.req.CopyBudget
	if budget == nil {
		return 0
	}
	return 2*a.out.stats.Entries*budget.EntryCost + a.out.stats.CopiedBytes
}

func (a *seedAccumulator) chargeEntries(rootID string, entries int64) error {
	a.out.stats.Entries += entries
	return a.afterCharge(rootID)
}

func (a *seedAccumulator) chargeCopied(rootID string, copied int64) error {
	a.out.stats.CopiedBytes += copied
	return a.afterCharge(rootID)
}

func (a *seedAccumulator) afterCharge(rootID string) error {
	if a.req.ProgressFn != nil {
		a.req.ProgressFn(MaterializeProgress{
			ViewID: a.req.ViewID, RootID: rootID, Entries: a.out.stats.Entries, LogicalBytes: a.out.logicalBytes,
			CopiedBytes: a.out.stats.CopiedBytes, GitReposBuilt: a.gitReposBuilt,
		})
	}
	if a.req.CopyBudget.passedBy(a.out.stats.Entries, a.out.stats.CopiedBytes) {
		return a.overrun(rootID)
	}
	return nil
}

func (a *seedAccumulator) overrun(rootID string) error {
	limit := int64(0)
	if a.req.CopyBudget != nil {
		limit = a.req.CopyBudget.Limit
	}
	return &BudgetOverrunError{
		ViewID: a.req.ViewID, RootID: rootID, Entries: a.out.stats.Entries,
		CopiedBytes: a.out.stats.CopiedBytes, Limit: limit,
	}
}
