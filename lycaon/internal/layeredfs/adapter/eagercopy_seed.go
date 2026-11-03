package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

type seedOutcome struct {
	rootSpecs       []layeredfs.RootSpec
	preparedRoots   []PreparedRoot
	denyPaths       []string
	primaryID       string
	primaryPath     string
	bases           []layeredfs.BaseCapture
	filePaths       map[layeredfs.QualifiedPath]string
	seededRepos     map[string]SeedGitInitResult
	gitInterceptMap map[layeredfs.QualifiedPath]layeredfs.QualifiedPath
	gitStore        *privategit.RetainedObjectStore
	gitLeases       []string
	observedEntries int64
	observedBytes   int64
}

type seededDirMode struct {
	dstPath string
	perm    fs.FileMode
	modTime time.Time
}

type seedAccumulator struct {
	ctx            context.Context
	req            PrepareRequest
	runtimeViewDir string
	selectedIDs    []string
	narrowed       []RootGrant
	maxEntries     int64
	maxBytes       int64
	gitStores      *gitStoreRegistry
	gitReposBuilt  int
	seededDirs     []seededDirMode
	out            *seedOutcome
}

func seedSelectedRoots(
	ctx context.Context,
	req PrepareRequest,
	runtimeViewDir string,
	narrowed []RootGrant,
	selectedIDs []string,
	maxEntries, maxBytes int64,
	gitStores *gitStoreRegistry,
) (*seedOutcome, error) {
	out := &seedOutcome{
		filePaths:       make(map[layeredfs.QualifiedPath]string),
		seededRepos:     make(map[string]SeedGitInitResult),
		gitInterceptMap: make(map[layeredfs.QualifiedPath]layeredfs.QualifiedPath),
	}
	acc := &seedAccumulator{
		ctx: ctx, req: req, runtimeViewDir: runtimeViewDir, selectedIDs: selectedIDs,
		narrowed: narrowed, maxEntries: maxEntries, maxBytes: maxBytes, gitStores: gitStores, out: out,
	}

	for _, g := range narrowed {
		seedDir := filepath.Join(runtimeViewDir, "seeds", g.RootID)
		if custom := strings.TrimSpace(req.TargetRootDirs[g.RootID]); custom != "" {
			seedDir = filepath.Clean(custom)
		}
		if err := os.MkdirAll(seedDir, 0o750); err != nil {
			return out, err
		}
		if err := acc.walkAndSeedRoot(g, seedDir); err != nil {
			return out, err
		}
		out.rootSpecs = append(out.rootSpecs, layeredfs.RootSpec{
			ID: g.RootID, Label: g.Label, TrunkPath: seedDir, Subroot: g.Subroot, IsPrimary: g.IsPrimary,
		})
		out.preparedRoots = append(out.preparedRoots, PreparedRoot{
			RootID: g.RootID, Label: g.Label, SourcePath: g.SourcePath, Subroot: g.Subroot, WorkspacePath: seedDir, IsPrimary: g.IsPrimary,
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

func (a *seedAccumulator) walkAndSeedRoot(g RootGrant, seedDir string) error {
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
		return a.seedSingleEntry(g, rel, srcPath, filepath.Join(seedDir, relOS))
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
	}
	return nil
}

func (a *seedAccumulator) handleDiscoveredGit(g RootGrant, gitRel, srcGitPath string, d fs.DirEntry) error {
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
		AuthorizedGitDirs: authDirs, MaxEntries: max(a.maxEntries-a.out.observedEntries, 1),
		MaxBytes: max(a.maxBytes-a.out.observedBytes, 1), RetainedStore: a.out.gitStore,
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
			return a.chargeUsage(g.RootID, max(res.AddedEntries, a.maxEntries-a.out.observedEntries+1), max(res.AddedBytes, 0))
		}
		return err
	}
	if res.LeaseID != "" {
		a.out.gitLeases = append(a.out.gitLeases, res.LeaseID)
	}
	a.gitReposBuilt++
	if err := a.chargeUsage(g.RootID, res.AddedEntries, res.AddedBytes); err != nil {
		return err
	}
	a.out.seededRepos[g.RootID+":"+repoRel] = res
	a.out.gitInterceptMap[layeredfs.QualifiedPath{RootID: g.RootID, Rel: gitRel}] = layeredfs.QualifiedPath{RootID: "__privategit__", Rel: privGitRel}
	return nil
}

func (a *seedAccumulator) seedSingleEntry(g RootGrant, rel, srcPath, dstPath string) error {
	info, err := os.Lstat(srcPath)
	if err != nil {
		return err
	}
	qp := layeredfs.QualifiedPath{RootID: g.RootID, Rel: rel}
	mode := info.Mode()
	xattrs, err := readPathXattrs(srcPath)
	if err != nil {
		return err
	}

	switch {
	case mode.IsDir():
		if err := a.chargeUsage(g.RootID, 1, 0); err != nil {
			return err
		}
		tempPerm := mode.Perm() | 0o700
		if err := os.MkdirAll(dstPath, tempPerm); err != nil {
			return err
		}
		if err := os.Chmod(dstPath, tempPerm); err != nil {
			return err
		}
		if err := writePathXattrs(dstPath, xattrs); err != nil {
			return err
		}
		a.seededDirs = append(a.seededDirs, seededDirMode{
			dstPath: dstPath, perm: mode.Perm(), modTime: info.ModTime(),
		})
		a.out.bases = append(a.out.bases, layeredfs.BaseCapture{
			Path: qp, Layer: layeredfs.LayerTrunk, Kind: layeredfs.EntryDirectory,
			Mode: mode, ModTime: info.ModTime(), Xattrs: xattrs, CapturedAt: time.Now(),
		})
		return nil

	case mode&os.ModeSymlink != 0:
		target, err := os.Readlink(srcPath)
		if err != nil {
			return err
		}
		if err := a.chargeUsage(g.RootID, 1, int64(len(target))); err != nil {
			return err
		}
		if err := os.Symlink(target, dstPath); err != nil {
			return err
		}
		if err := writePathXattrs(dstPath, xattrs); err != nil {
			return err
		}
		a.out.bases = append(a.out.bases, layeredfs.BaseCapture{
			Path: qp, Layer: layeredfs.LayerTrunk, Kind: layeredfs.EntrySymlink,
			Mode: mode, Size: int64(len(target)), ModTime: info.ModTime(), LinkTarget: target, Xattrs: xattrs, CapturedAt: time.Now(),
		})
		return nil

	case mode.IsRegular():
		return a.seedRegularFile(g.RootID, qp, srcPath, dstPath, info, xattrs)

	default:
		return nil
	}
}

func (a *seedAccumulator) seedRegularFile(rootID string, qp layeredfs.QualifiedPath, srcPath, dstPath string, info fs.FileInfo, xattrs map[string][]byte) error {
	if err := a.chargeUsage(rootID, 1, 0); err != nil {
		return err
	}
	if a.req.FileSeeder != nil {
		sz := max(info.Size(), 0)
		if err := a.chargeUsage(rootID, 0, sz); err != nil {
			return err
		}
		_, n, shaHex, err := a.req.FileSeeder(a.ctx, rootID, qp.Rel, srcPath, dstPath, info)
		if err != nil {
			return err
		}
		if n > sz {
			if err := a.chargeUsage(rootID, 0, n-sz); err != nil {
				return err
			}
		}
		if err := chmodPath(dstPath, info.Mode().Perm()); err != nil {
			return err
		}
		if err := writePathXattrs(dstPath, xattrs); err != nil {
			return err
		}
		a.out.filePaths[qp] = dstPath
		a.out.bases = append(a.out.bases, layeredfs.BaseCapture{
			Path: qp, Layer: layeredfs.LayerTrunk, Kind: layeredfs.EntryRegular,
			Mode: info.Mode(), Size: n, ModTime: info.ModTime(), SHA256: shaHex,
			Xattrs: xattrs, CapturedAt: time.Now(),
		})
		return nil
	}
	remainingBytes := a.maxBytes - a.out.observedBytes
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	outFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(outFile, h), io.LimitReader(in, remainingBytes+1))
	closeErr := outFile.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return err
	}
	if err := a.chargeUsage(rootID, 0, n); err != nil {
		return err
	}
	if err := chmodPath(dstPath, info.Mode().Perm()); err != nil {
		return err
	}
	if err := writePathXattrs(dstPath, xattrs); err != nil {
		return err
	}
	if err := os.Chtimes(dstPath, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	a.out.filePaths[qp] = dstPath
	a.out.bases = append(a.out.bases, layeredfs.BaseCapture{
		Path: qp, Layer: layeredfs.LayerTrunk, Kind: layeredfs.EntryRegular,
		Mode: info.Mode(), Size: n, ModTime: info.ModTime(), SHA256: hex.EncodeToString(h.Sum(nil)),
		Xattrs: xattrs, CapturedAt: time.Now(),
	})
	return nil
}

func (a *seedAccumulator) chargeUsage(rootID string, entries, bytesDelta int64) error {
	a.out.observedEntries += entries
	a.out.observedBytes += bytesDelta
	if a.req.ProgressFn != nil {
		a.req.ProgressFn(FallbackProgress{
			ViewID: a.req.ViewID, RootID: rootID, ObservedEntries: a.out.observedEntries,
			ObservedBytes: a.out.observedBytes, MaxEntries: a.maxEntries, MaxBytes: a.maxBytes, GitReposBuilt: a.gitReposBuilt,
		})
	}
	if a.out.observedEntries > a.maxEntries || a.out.observedBytes > a.maxBytes {
		return &PreparationLimitError{
			ReasonCode: ReasonWorkspacePreparationLimit, ViewID: a.req.ViewID, RootID: rootID,
			SelectedRoots: a.selectedIDs, ObservedEntries: a.out.observedEntries, ObservedBytes: a.out.observedBytes,
			MaxEntries: a.maxEntries, MaxBytes: a.maxBytes, EstimatedRejection: false,
			CleanedIncompleteSeed: true, RetryWithSmallerSelection: true, SuggestedSubroots: suggestSubroots(a.narrowed),
			Diagnostic: "actual seed traversal exceeded configured workspace preparation limit",
		}
	}
	return nil
}
