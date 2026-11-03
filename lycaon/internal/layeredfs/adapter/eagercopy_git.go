package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

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
	if err := os.MkdirAll(spec.PrivateGitDir, 0o700); err != nil {
		return SeedGitInitResult{}, err
	}

	res := SeedGitInitResult{
		PrivateMetaDir: spec.PrivateGitDir,
		HeadRef:        "ref: refs/heads/main\n",
		AddedEntries:   1,
	}

	srcGitDir := spec.SourceGitPath
	if spec.IsGitlink {
		target := strings.TrimSpace(strings.TrimPrefix(spec.GitlinkContent, "gitdir:"))
		if target != "" {
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(spec.SourceGitPath), target)
			}
			if st, err := os.Stat(target); err == nil && st.IsDir() {
				srcGitDir = target
			}
		}
	}

	if st, err := os.Stat(srcGitDir); err == nil && st.IsDir() {
		entries, bytesCopied, headRef, objCount, cErr := copyPrivateGitRetention(ctx, srcGitDir, spec.PrivateGitDir)
		if cErr != nil {
			return SeedGitInitResult{}, cErr
		}
		res.AddedEntries += entries
		res.AddedBytes += bytesCopied
		res.ObjectCount = objCount
		if headRef != "" {
			res.HeadRef = headRef
		}
		return res, nil
	}

	headPath := filepath.Join(spec.PrivateGitDir, "HEAD")
	if err := os.WriteFile(headPath, []byte(res.HeadRef), 0o600); err != nil {
		return SeedGitInitResult{}, err
	}
	res.AddedEntries++
	res.AddedBytes += int64(len(res.HeadRef))
	return res, nil
}

func copyPrivateGitRetention(ctx context.Context, srcGitDir, dstGitDir string) (int64, int64, string, int64, error) {
	var entries, totalBytes, objCount int64
	var headRef string
	err := filepath.WalkDir(srcGitDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return &AdapterError{AdapterID: AdapterEagerCopy, Op: "seed_git", ReasonCode: ReasonContextCanceled, Diagnostic: err.Error(), Cause: err}
		}
		rel, err := filepath.Rel(srcGitDir, p)
		if err != nil || rel == "." {
			return err
		}
		dst := filepath.Join(dstGitDir, rel)
		if d.IsDir() {
			entries++
			return os.MkdirAll(dst, 0o700)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, raw, 0o600); err != nil {
			return err
		}
		entries++
		totalBytes += int64(len(raw))
		slashRel := filepath.ToSlash(rel)
		if slashRel == "HEAD" {
			headRef = strings.TrimSpace(string(raw))
		}
		if strings.HasPrefix(slashRel, "objects/") && !strings.HasPrefix(slashRel, "objects/info") {
			objCount++
		}
		return nil
	})
	return entries, totalBytes, headRef, objCount, err
}

func syncMaterializedTreeToCore(view *PreparedView, seedRecords map[layeredfs.QualifiedPath]layeredfs.BaseCapture) error {
	seenOnDisk := make(map[layeredfs.QualifiedPath]bool, len(seedRecords))
	for _, r := range view.Roots {
		if err := syncSingleMaterializedRoot(view.Core, r, seedRecords, seenOnDisk); err != nil {
			return err
		}
	}
	for qp, base := range seedRecords {
		if !seenOnDisk[qp] && base.Kind != layeredfs.EntryDirectory {
			_ = view.Core.Unlink(qp, false)
		}
	}
	return nil
}

func syncSingleMaterializedRoot(
	core *layeredfs.Core,
	r PreparedRoot,
	seedRecords map[layeredfs.QualifiedPath]layeredfs.BaseCapture,
	seenOnDisk map[layeredfs.QualifiedPath]bool,
) error {
	rootDir := filepath.Clean(r.WorkspacePath)
	return filepath.WalkDir(rootDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relOS, err := filepath.Rel(rootDir, p)
		if err != nil || relOS == "." {
			return err
		}
		qp := layeredfs.QualifiedPath{RootID: r.RootID, Rel: filepath.ToSlash(relOS)}
		seenOnDisk[qp] = true
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		base, hadSeed := seedRecords[qp]
		if info.IsDir() {
			if !hadSeed {
				_, _ = core.Mkdir(qp, info.Mode().Perm())
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if !hadSeed || base.Kind != layeredfs.EntrySymlink || base.LinkTarget != target {
				_, _ = core.Symlink(target, qp)
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		sha := hex.EncodeToString(sum[:])
		if hadSeed && base.Kind == layeredfs.EntryRegular && base.SHA256 == sha && base.Size == int64(len(raw)) {
			return nil
		}
		if existing, rErr := core.ReadFile(qp); rErr == nil && bytes.Equal(existing, raw) && hadSeed {
			// Check if Core already has this mutation recorded.
			if attr, sErr := core.Lstat(qp); sErr == nil && attr.Layer == layeredfs.LayerPrivate {
				return nil
			}
		}
		_, err = core.WriteFile(qp, raw, info.Mode().Perm())
		return err
	})
}
