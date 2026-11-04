package adapter

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

var sharedRootGitStores sync.Map // map[string]*privategit.RetainedObjectStore

type gitStoreRegistry struct {
	mu     sync.Mutex
	byRoot map[string]*privategit.RetainedObjectStore
}

func newGitStoreRegistry() *gitStoreRegistry {
	return &gitStoreRegistry{byRoot: make(map[string]*privategit.RetainedObjectStore)}
}

func (r *gitStoreRegistry) storeForPrepare(runtimeDir string, grants []RootGrant) (*privategit.RetainedObjectStore, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := canonicalGitPath(runtimeDir)
	for _, g := range grants {
		if g.IsPrimary {
			key = canonicalGitPath(g.SourcePath)
			break
		}
	}
	if s, ok := r.byRoot[key]; ok && storeDirExists(s) {
		return s, nil
	}
	if v, ok := sharedRootGitStores.Load(key); ok {
		if s, ok := v.(*privategit.RetainedObjectStore); ok && storeDirExists(s) {
			r.byRoot[key] = s
			return s, nil
		}
	}
	storeDir := filepath.Join(runtimeDir, "retained-git-store")
	s, err := privategit.NewRetainedObjectStore(storeDir)
	if err != nil {
		return nil, err
	}
	_ = s.Close()
	r.byRoot[key] = s
	sharedRootGitStores.Store(key, s)
	return s, nil
}

func storeDirExists(s *privategit.RetainedObjectStore) bool {
	if s == nil || s.RootDir() == "" {
		return false
	}
	st, err := os.Stat(s.ObjectsDir())
	return err == nil && st.IsDir()
}

func outcomeStore(out *seedOutcome) *privategit.RetainedObjectStore {
	if out == nil {
		return nil
	}
	return out.gitStore
}

func outcomeLeases(out *seedOutcome) []string {
	if out == nil {
		return nil
	}
	return out.gitLeases
}

func releaseGitLeases(store *privategit.RetainedObjectStore, leases []string) {
	if store == nil {
		return
	}
	for _, lid := range leases {
		if lid != "" {
			_ = store.ReleaseLease(lid)
		}
	}
}

func cleanupFailedPrepare(req PrepareRequest, runtimeViewDir string, out *seedOutcome, core *layeredfs.Core) {
	releaseGitLeases(outcomeStore(out), outcomeLeases(out))
	if core != nil {
		_ = core.ReleaseView()
	}
	_ = removeAllForceWritable(runtimeViewDir)
	if req.BackingDir != "" {
		_ = removeAllForceWritable(req.BackingDir)
	}
	for _, dir := range req.TargetRootDirs {
		if strings.TrimSpace(dir) != "" {
			_ = removeAllForceWritable(dir)
		}
	}
}

type seededGitInterceptor struct {
	redirects map[layeredfs.QualifiedPath]layeredfs.QualifiedPath
}

func (g *seededGitInterceptor) InterceptGitAccess(viewID string, qpath layeredfs.QualifiedPath, writeIntent bool) (layeredfs.GitInterceptOutcome, error) {
	if qpath.RootID == "__privategit__" {
		return layeredfs.GitInterceptOutcome{Intercepted: true}, nil
	}
	rel := filepath.ToSlash(filepath.Clean(qpath.Rel))
	for gitPath, privTarget := range g.redirects {
		if qpath.RootID != gitPath.RootID {
			continue
		}
		if rel == gitPath.Rel {
			return layeredfs.GitInterceptOutcome{
				Intercepted:  true,
				RedirectRoot: privTarget.RootID,
				RedirectRel:  privTarget.Rel,
			}, nil
		}
		prefix := gitPath.Rel + "/"
		if strings.HasPrefix(rel, prefix) {
			sub := strings.TrimPrefix(rel, prefix)
			return layeredfs.GitInterceptOutcome{
				Intercepted:  true,
				RedirectRoot: privTarget.RootID,
				RedirectRel:  filepath.ToSlash(filepath.Join(privTarget.Rel, sub)),
			}, nil
		}
	}
	return layeredfs.GitInterceptOutcome{}, nil
}

func defaultSeedGitInitializer(ctx context.Context, spec SeedGitRepoSpec) (SeedGitInitResult, error) {
	if err := ctx.Err(); err != nil {
		return SeedGitInitResult{}, &AdapterError{AdapterID: AdapterEagerCopy, Op: "seed_git", ReasonCode: ReasonContextCanceled, Diagnostic: err.Error(), Cause: err}
	}
	gitDir, commonDir, objDirs, err := resolveAndValidateGitSpec(spec)
	if err != nil {
		return SeedGitInitResult{}, err
	}
	entries, bytesCount, err := surveyGitDirsWithinLimits(ctx, gitDir, commonDir, objDirs, spec.MaxEntries, spec.MaxBytes)
	if err != nil {
		return SeedGitInitResult{AddedEntries: entries, AddedBytes: bytesCount}, err
	}

	parentDir := filepath.Dir(spec.PrivateGitDir)
	if err := os.MkdirAll(parentDir, 0o700); err != nil {
		return SeedGitInitResult{}, err
	}
	stageDir, err := os.MkdirTemp(parentDir, ".git-stage-*")
	if err != nil {
		return SeedGitInitResult{}, err
	}
	cleanupStage := func() { _ = os.RemoveAll(stageDir) }

	headRef, err := populateSanitizedPrivateGitMetadata(gitDir, commonDir, stageDir)
	if err != nil {
		cleanupStage()
		return SeedGitInitResult{}, err
	}

	store := spec.RetainedStore
	if store == nil {
		store, err = privategit.NewRetainedObjectStore(filepath.Join(parentDir, ".retained-store"))
		if err != nil {
			cleanupStage()
			return SeedGitInitResult{}, err
		}
	}
	metrics, reachable, err := store.ImportRepositoryObjects(ctx, objDirs, nil)
	_ = store.Close()
	if err != nil {
		cleanupStage()
		return SeedGitInitResult{}, err
	}
	holder := spec.ViewID + ":" + spec.RootID + ":" + spec.RepoRelPath
	leaseID, err := store.RetainForView(holder, reachable)
	if err != nil {
		cleanupStage()
		return SeedGitInitResult{}, err
	}
	altPath := filepath.Join(stageDir, "objects", "info", "alternates")
	if err := os.WriteFile(altPath, []byte(store.ObjectsDir()+"\n"), 0o600); err != nil {
		_ = store.ReleaseLease(leaseID)
		cleanupStage()
		return SeedGitInitResult{}, err
	}
	_ = os.RemoveAll(spec.PrivateGitDir)
	if err := os.Rename(stageDir, spec.PrivateGitDir); err != nil {
		_ = store.ReleaseLease(leaseID)
		cleanupStage()
		return SeedGitInitResult{}, err
	}
	return SeedGitInitResult{
		PrivateMetaDir: spec.PrivateGitDir,
		HeadRef:        headRef,
		ObjectCount:    metrics.ObjectsImported + metrics.ObjectsReused,
		ObjectsReused:  metrics.ObjectsReused,
		BytesImported:  metrics.BytesImported,
		BytesReused:    metrics.BytesReused,
		AddedEntries:   entries,
		AddedBytes:     bytesCount,
		LeaseID:        leaseID,
	}, nil
}

func resolveAndValidateGitSpec(spec SeedGitRepoSpec) (string, string, []string, error) {
	worktreeDir := filepath.Dir(spec.SourceGitPath)
	allowed := make([]string, 0, len(spec.AuthorizedGitDirs)+1)
	for _, a := range spec.AuthorizedGitDirs {
		if c := canonicalGitPath(a); c != "" {
			allowed = append(allowed, c)
		}
	}
	if len(allowed) == 0 {
		if c := canonicalGitPath(worktreeDir); c != "" {
			allowed = append(allowed, c)
		}
	}

	gitDir := canonicalGitPath(spec.SourceGitPath)
	if spec.IsGitlink {
		line := strings.TrimSpace(spec.GitlinkContent)
		if !strings.HasPrefix(line, "gitdir:") {
			return "", "", nil, unauthorizedGitError("invalid gitlink syntax")
		}
		target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if target == "" || strings.ContainsRune(target, 0) {
			return "", "", nil, unauthorizedGitError("empty gitlink target")
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(worktreeDir, target)
		}
		linfo, err := os.Lstat(target)
		if err != nil || !linfo.IsDir() || linfo.Mode()&fs.ModeSymlink != 0 {
			return "", "", nil, unauthorizedGitError("unresolved or invalid gitlink target")
		}
		gitDir = canonicalGitPath(target)
		if !isPathUnderAllowed(gitDir, allowed) {
			common, proofErr := privategit.ProveLinkedWorktree(spec.SourceGitPath, gitDir)
			if proofErr != nil {
				return "", "", nil, unauthorizedGitError("external gitlink target is outside granted roots")
			}
			// The worktree's shared repository is authorized by that linkage, and its
			// alternates still answer to the same check as any other.
			allowed = append(allowed, gitDir, common)
		}
	} else {
		linfo, err := os.Lstat(spec.SourceGitPath)
		if err != nil || !linfo.IsDir() || linfo.Mode()&fs.ModeSymlink != 0 {
			return "", "", nil, unauthorizedGitError("invalid .git directory")
		}
	}

	commonDir, err := resolveValidatedCommonDir(gitDir, allowed)
	if err != nil {
		return "", "", nil, err
	}
	objDirs, err := resolveValidatedObjectDirs(commonDir, gitDir, allowed)
	if err != nil {
		return "", "", nil, err
	}
	return gitDir, commonDir, objDirs, nil
}

func resolveValidatedCommonDir(gitDir string, allowed []string) (string, error) {
	p := filepath.Join(gitDir, "commondir")
	linfo, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return gitDir, nil
	}
	if err != nil || linfo.Mode()&fs.ModeSymlink != 0 {
		return "", unauthorizedGitError("invalid commondir")
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	val := strings.TrimSpace(string(raw))
	if val == "" {
		return "", unauthorizedGitError("empty commondir")
	}
	if !filepath.IsAbs(val) {
		val = filepath.Join(gitDir, val)
	}
	st, err := os.Lstat(val)
	if err != nil || !st.IsDir() || st.Mode()&fs.ModeSymlink != 0 {
		return "", unauthorizedGitError("unresolved commondir target")
	}
	commonDir := canonicalGitPath(val)
	if !isPathUnderAllowed(commonDir, allowed) {
		return "", unauthorizedGitError("commondir escapes granted roots")
	}
	return commonDir, nil
}
