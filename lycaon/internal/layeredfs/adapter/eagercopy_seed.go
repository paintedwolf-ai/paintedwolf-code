package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

type seedOutcome struct {
	rootSpecs       []layeredfs.RootSpec
	preparedRoots   []PreparedRoot
	denyPaths       []string
	primaryID       string
	primaryPath     string
	bases           []layeredfs.BaseCapture
	seededRepos     map[string]SeedGitInitResult
	gitInterceptMap map[layeredfs.QualifiedPath]layeredfs.QualifiedPath
	observedEntries int64
	observedBytes   int64
}

type seedAccumulator struct {
	ctx             context.Context
	req             PrepareRequest
	runtimeViewDir  string
	selectedIDs     []string
	narrowed        []RootGrant
	maxEntries      int64
	maxBytes        int64
	observedEntries int64
	observedBytes   int64
	gitReposBuilt   int
	bases           []layeredfs.BaseCapture
	seededRepos     map[string]SeedGitInitResult
	gitInterceptMap map[layeredfs.QualifiedPath]layeredfs.QualifiedPath
}

func resolvePrepareLimits(maxEntries, maxBytes int64) (int64, int64) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return maxEntries, maxBytes
}

func collectGrantIDs(grants []RootGrant) []string {
	ids := make([]string, 0, len(grants))
	for _, g := range grants {
		ids = append(ids, g.RootID)
	}
	return ids
}

func checkSourceIndexEstimates(viewID string, narrowed []RootGrant, selectedIDs []string, maxEntries, maxBytes int64) error {
	var totalEntries, totalBytes int64
	for _, g := range narrowed {
		if g.Estimate == nil {
			continue
		}
		totalEntries += g.Estimate.EstimatedEntries
		totalBytes += g.Estimate.EstimatedBytes
		if g.Estimate.EstimatedEntries > maxEntries || g.Estimate.EstimatedBytes > maxBytes ||
			totalEntries > maxEntries || totalBytes > maxBytes {
			return &PreparationLimitError{
				ReasonCode:                ReasonWorkspacePreparationLimit,
				ViewID:                    viewID,
				RootID:                    g.RootID,
				SelectedRoots:             selectedIDs,
				ObservedEntries:           totalEntries,
				ObservedBytes:             totalBytes,
				MaxEntries:                maxEntries,
				MaxBytes:                  maxBytes,
				EstimatedRejection:        true,
				CleanedIncompleteSeed:     true,
				RetryWithSmallerSelection: true,
				SuggestedSubroots:         suggestSubroots(narrowed),
				Diagnostic:                "source-index estimate exceeds workspace preparation limit",
			}
		}
	}
	return nil
}

func suggestSubroots(grants []RootGrant) []string {
	var suggestions []string
	for _, g := range grants {
		entries, err := os.ReadDir(g.SourcePath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() && e.Name() != ".git" && !strings.HasPrefix(e.Name(), ".") {
				suggestions = append(suggestions, g.RootID+":"+e.Name())
			}
		}
	}
	sort.Strings(suggestions)
	return suggestions
}

func seedSelectedRoots(
	ctx context.Context,
	req PrepareRequest,
	runtimeViewDir string,
	narrowed []RootGrant,
	selectedIDs []string,
	maxEntries, maxBytes int64,
) (*seedOutcome, error) {
	acc := &seedAccumulator{
		ctx:             ctx,
		req:             req,
		runtimeViewDir:  runtimeViewDir,
		selectedIDs:     selectedIDs,
		narrowed:        narrowed,
		maxEntries:      maxEntries,
		maxBytes:        maxBytes,
		seededRepos:     make(map[string]SeedGitInitResult),
		gitInterceptMap: make(map[layeredfs.QualifiedPath]layeredfs.QualifiedPath),
	}
	out := &seedOutcome{seededRepos: acc.seededRepos, gitInterceptMap: acc.gitInterceptMap}

	for _, g := range narrowed {
		seedDir := filepath.Join(runtimeViewDir, "seeds", g.RootID)
		if err := os.MkdirAll(seedDir, 0o700); err != nil {
			return nil, err
		}
		if err := acc.walkAndSeedRoot(g, seedDir); err != nil {
			return nil, err
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
	if len(acc.gitInterceptMap) > 0 {
		out.rootSpecs = append(out.rootSpecs, layeredfs.RootSpec{
			ID: "__privategit__", Label: "privategit", TrunkPath: filepath.Join(runtimeViewDir, "privategit"),
		})
	}
	out.bases = acc.bases
	out.observedEntries = acc.observedEntries
	out.observedBytes = acc.observedBytes
	return out, nil
}

func (a *seedAccumulator) walkAndSeedRoot(g RootGrant, seedDir string) error {
	srcRoot := filepath.Clean(g.SourcePath)
	return filepath.WalkDir(srcRoot, func(srcPath string, d fs.DirEntry, walkErr error) error {
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
}

func (a *seedAccumulator) handleDiscoveredGit(g RootGrant, gitRel, srcGitPath string, d fs.DirEntry) error {
	repoRel := filepath.ToSlash(filepath.Dir(gitRel))
	if repoRel == "" {
		repoRel = "."
	}
	privGitRel := filepath.ToSlash(filepath.Join(g.RootID, gitRel))
	privGitDir := filepath.Join(a.runtimeViewDir, "privategit", filepath.FromSlash(privGitRel))
	spec := SeedGitRepoSpec{
		ViewID: a.req.ViewID, RootID: g.RootID, RepoRelPath: repoRel, GitEntryRel: gitRel,
		SourceGitPath: srcGitPath, PrivateGitDir: privGitDir, IsGitlink: !d.IsDir(),
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
		return err
	}
	a.gitReposBuilt++
	if err := a.chargeUsage(g.RootID, res.AddedEntries, res.AddedBytes); err != nil {
		return err
	}
	a.seededRepos[g.RootID+":"+repoRel] = res
	a.gitInterceptMap[layeredfs.QualifiedPath{RootID: g.RootID, Rel: gitRel}] = layeredfs.QualifiedPath{
		RootID: "__privategit__", Rel: privGitRel,
	}
	return nil
}

func (a *seedAccumulator) seedSingleEntry(g RootGrant, rel, srcPath, dstPath string) error {
	info, err := os.Lstat(srcPath)
	if err != nil {
		return err
	}
	qp := layeredfs.QualifiedPath{RootID: g.RootID, Rel: rel}
	mode := info.Mode()

	switch {
	case mode.IsDir():
		if err := a.chargeUsage(g.RootID, 1, 0); err != nil {
			return err
		}
		if err := os.MkdirAll(dstPath, mode.Perm()|0o700); err != nil {
			return err
		}
		a.bases = append(a.bases, layeredfs.BaseCapture{
			Path: qp, Layer: layeredfs.LayerTrunk, Kind: layeredfs.EntryDirectory,
			Mode: mode, ModTime: info.ModTime(), CapturedAt: time.Now(),
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
		a.bases = append(a.bases, layeredfs.BaseCapture{
			Path: qp, Layer: layeredfs.LayerTrunk, Kind: layeredfs.EntrySymlink,
			Mode: mode, Size: int64(len(target)), ModTime: info.ModTime(), LinkTarget: target, CapturedAt: time.Now(),
		})
		return nil

	case mode.IsRegular():
		return a.seedRegularFile(g.RootID, qp, srcPath, dstPath, info)

	default:
		return nil
	}
}

func (a *seedAccumulator) seedRegularFile(rootID string, qp layeredfs.QualifiedPath, srcPath, dstPath string, info fs.FileInfo) error {
	if err := a.chargeUsage(rootID, 1, 0); err != nil {
		return err
	}
	remainingBytes := a.maxBytes - a.observedBytes
	in, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()|0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(in, remainingBytes+1))
	_ = out.Close()
	if copyErr != nil {
		return copyErr
	}
	if err := a.chargeUsage(rootID, 0, n); err != nil {
		return err
	}
	_ = os.Chtimes(dstPath, info.ModTime(), info.ModTime())
	a.bases = append(a.bases, layeredfs.BaseCapture{
		Path: qp, Layer: layeredfs.LayerTrunk, Kind: layeredfs.EntryRegular,
		Mode: info.Mode(), Size: n, ModTime: info.ModTime(), SHA256: hex.EncodeToString(h.Sum(nil)),
		BackingFile: dstPath, CapturedAt: time.Now(),
	})
	return nil
}

func (a *seedAccumulator) chargeUsage(rootID string, entries, bytesDelta int64) error {
	a.observedEntries += entries
	a.observedBytes += bytesDelta
	if a.req.ProgressFn != nil {
		a.req.ProgressFn(FallbackProgress{
			ViewID: a.req.ViewID, RootID: rootID, ObservedEntries: a.observedEntries,
			ObservedBytes: a.observedBytes, MaxEntries: a.maxEntries, MaxBytes: a.maxBytes, GitReposBuilt: a.gitReposBuilt,
		})
	}
	if a.observedEntries > a.maxEntries || a.observedBytes > a.maxBytes {
		return &PreparationLimitError{
			ReasonCode: ReasonWorkspacePreparationLimit, ViewID: a.req.ViewID, RootID: rootID,
			SelectedRoots: a.selectedIDs, ObservedEntries: a.observedEntries, ObservedBytes: a.observedBytes,
			MaxEntries: a.maxEntries, MaxBytes: a.maxBytes, EstimatedRejection: false,
			CleanedIncompleteSeed: true, RetryWithSmallerSelection: true, SuggestedSubroots: suggestSubroots(a.narrowed),
			Diagnostic: "actual seed traversal exceeded configured workspace preparation limit",
		}
	}
	return nil
}
