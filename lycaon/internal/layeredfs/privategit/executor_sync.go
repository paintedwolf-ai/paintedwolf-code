package privategit

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

func prepareAndAuthorizeGitArgs(ctx context.Context, view *managedView, targetWT string, policy RemotePolicy, args []string) ([]string, error) {
	sub := args[0]
	switch sub {
	case "fetch", "pull", "ls-remote", "push":
		isPush := sub == "push"
		if (isPush && !policy.AllowPush) || (!isPush && !policy.AllowFetch) {
			return nil, fmt.Errorf("%w: %s is disabled", ErrRemoteOperationRefused, sub)
		}
		remote, remoteURL := resolveTargetRemote(ctx, targetWT, args[1:])
		if isPush && canonicalCleanPath(remoteURL) == canonicalCleanPath(view.trunkDir) && !policy.ApprovedRemotes[view.trunkDir] {
			return nil, fmt.Errorf("%w: pushing directly to trunk is forbidden", ErrRemoteOperationRefused)
		}
		if !isRemoteApproved(policy.ApprovedRemotes, remote, remoteURL) {
			return nil, fmt.Errorf("%w: remote %q (%s) is not approved for %s", ErrRemoteOperationRefused, remote, remoteURL, sub)
		}
		if policy.CredentialCheck != nil {
			if err := policy.CredentialCheck(ctx, sub, remote, remoteURL); err != nil {
				return nil, fmt.Errorf("%w: credential check failed: %v", ErrRemoteOperationRefused, err)
			}
		}
	case "repack":
		for _, a := range args[1:] {
			if a == "-l" || a == "--local" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "l")) {
				return args, nil
			}
		}
		return append([]string{"repack", "-l"}, args[1:]...), nil
	}
	return args, nil
}

func resolveTargetRemote(ctx context.Context, targetWT string, restArgs []string) (string, string) {
	remote := "origin"
	for _, a := range restArgs {
		if !strings.HasPrefix(a, "-") && a != "" {
			remote = a
			break
		}
	}
	if out, code, err := gitexec.Run(ctx, targetWT, []string{"config", "--get", "remote." + remote + ".url"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err == nil && code == 0 {
		if url := strings.TrimSpace(string(out)); url != "" {
			return remote, url
		}
	}
	return remote, remote
}

func isRemoteApproved(approved map[string]bool, remote, remoteURL string) bool {
	if approved[remote] || approved[remoteURL] {
		return true
	}
	cleanURL := canonicalCleanPath(remoteURL)
	for k, ok := range approved {
		if ok && cleanURL != "" && canonicalCleanPath(k) == cleanURL {
			return true
		}
	}
	return false
}

func syncCoreOverlayToWorktree(view *managedView) error {
	var touched []string
	if view.core != nil {
		changes, err := view.core.EnumerateChanges(false)
		if err != nil {
			return err
		}
		for _, ce := range changes {
			if ce.Path.RootID != view.rootID {
				continue
			}
			touched = append(touched, ce.Path.Rel)
			dstPath := filepath.Join(view.worktreeDir, filepath.FromSlash(ce.Path.Rel))
			if ce.Deleted || ce.Kind == layeredfs.EntryAbsent {
				_ = os.RemoveAll(dstPath)
				continue
			}
			switch ce.Kind {
			case layeredfs.EntryDirectory:
				if err := os.MkdirAll(dstPath, 0o755); err != nil {
					return err
				}
			case layeredfs.EntrySymlink:
				_ = os.RemoveAll(dstPath)
				if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
					return err
				}
				if err := os.Symlink(ce.Final.LinkTarget, dstPath); err != nil {
					return err
				}
			case layeredfs.EntryRegular:
				raw, rErr := view.core.ReadFile(ce.Path)
				if rErr != nil {
					return rErr
				}
				if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
					return err
				}
				perm := ce.Final.Mode.Perm()
				if perm == 0 {
					perm = 0o644
				}
				if err := os.WriteFile(dstPath, raw, perm|0o200); err != nil {
					return err
				}
			}
		}
	}
	if !view.materialized {
		touched = append(touched, sparseMaterializedPaths(view.worktreeDir)...)
		if len(touched) > 0 {
			_ = updateIndexSkipWorktree(context.Background(), view.worktreeDir, "--no-skip-worktree", touched)
		}
	}
	return nil
}

type worktreeEntryState struct {
	kind       layeredfs.EntryKind
	mode       fs.FileMode
	sha256     string
	linkTarget string
}

type worktreeMutationBaseline struct {
	headOID    string
	candidates map[string]worktreeEntryState
}

func captureWorktreeMutationBaseline(ctx context.Context, view *managedView, targetWT string) *worktreeMutationBaseline {
	if view == nil || view.core == nil {
		return nil
	}
	base := &worktreeMutationBaseline{candidates: make(map[string]worktreeEntryState)}
	if hOut, hCode, hErr := gitexec.Run(ctx, targetWT, []string{"rev-parse", "--verify", "HEAD"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); hErr == nil && hCode == 0 {
		base.headOID = strings.TrimSpace(string(hOut))
	}
	for rel := range collectCandidateWorktreePaths(ctx, view, targetWT, "") {
		if st, ok := statWorktreeEntry(targetWT, rel); ok {
			base.candidates[rel] = st
		}
	}
	return base
}

func collectCandidateWorktreePaths(ctx context.Context, view *managedView, targetWT, beforeHeadOID string) map[string]bool {
	paths := make(map[string]bool)
	if sOut, sCode, sErr := gitexec.Run(ctx, targetWT, []string{"--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); sErr == nil && sCode == 0 {
		for _, p := range parsePorcelainZPaths(sOut) {
			paths[cleanSlashRel(p)] = true
		}
	}
	if beforeHeadOID != "" {
		if hOut, hCode, hErr := gitexec.Run(ctx, targetWT, []string{"rev-parse", "--verify", "HEAD"}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); hErr == nil && hCode == 0 {
			if afterHead := strings.TrimSpace(string(hOut)); afterHead != "" && afterHead != beforeHeadOID {
				if dtOut, dtCode, dtErr := gitexec.Run(ctx, targetWT, []string{"diff-tree", "-r", "--name-only", "-z", beforeHeadOID, afterHead}, gitexec.Opts{Profile: gitexec.ProfileHermetic}); dtErr == nil && dtCode == 0 {
					for _, tok := range bytes.Split(dtOut, []byte{0}) {
						if len(tok) > 0 {
							paths[cleanSlashRel(string(tok))] = true
						}
					}
				}
			}
		}
	}
	if view.core != nil {
		if changes, err := view.core.EnumerateChanges(false); err == nil {
			for _, ce := range changes {
				if ce.Path.RootID == view.rootID {
					paths[cleanSlashRel(ce.Path.Rel)] = true
				}
			}
		}
	}
	if !view.materialized {
		for _, p := range sparseMaterializedPaths(targetWT) {
			paths[p] = true
		}
	}
	delete(paths, ".")
	return paths
}

func statWorktreeEntry(worktreeDir, slashRel string) (worktreeEntryState, bool) {
	abs := filepath.Join(worktreeDir, filepath.FromSlash(slashRel))
	info, err := os.Lstat(abs)
	if err != nil || info.IsDir() {
		return worktreeEntryState{}, false
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		link, _ := os.Readlink(abs)
		return worktreeEntryState{kind: layeredfs.EntrySymlink, mode: info.Mode(), linkTarget: link}, true
	}
	if sum, _, hErr := hashFileBytes(abs); hErr == nil {
		return worktreeEntryState{kind: layeredfs.EntryRegular, mode: info.Mode(), sha256: sum}, true
	}
	return worktreeEntryState{}, false
}

func syncWorktreeMutationsToCore(ctx context.Context, view *managedView, repoRel string, base *worktreeMutationBaseline) error {
	if view == nil || view.core == nil || base == nil {
		return nil
	}
	targetWT := view.worktreeDir
	if repoRel != "." {
		targetWT = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
	}
	candidates := collectCandidateWorktreePaths(ctx, view, targetWT, base.headOID)
	for rel := range base.candidates {
		candidates[rel] = true
	}
	for rel := range candidates {
		aState, exists := statWorktreeEntry(targetWT, rel)
		bState, existed := base.candidates[rel]
		fullRel := rel
		if repoRel != "." && repoRel != "" {
			fullRel = cleanSlashRel(repoRel + "/" + rel)
		}
		qp := layeredfs.QualifiedPath{RootID: view.rootID, Rel: fullRel}
		if !exists {
			_ = view.core.Unlink(qp, bState.kind == layeredfs.EntryDirectory)
			continue
		}
		if existed && bState == aState {
			continue
		}
		if err := ensureCoreParentDirs(view.core, view.rootID, fullRel); err != nil {
			return err
		}
		switch aState.kind {
		case layeredfs.EntrySymlink:
			if existed {
				_ = view.core.Unlink(qp, false)
			}
			if _, err := view.core.Symlink(aState.linkTarget, qp); err != nil {
				return err
			}
		case layeredfs.EntryRegular:
			raw, err := os.ReadFile(filepath.Join(targetWT, filepath.FromSlash(rel)))
			if err != nil {
				return err
			}
			if _, err := view.core.WriteFile(qp, raw, aState.mode.Perm()); err != nil {
				return err
			}
		}
	}
	return nil
}

func ensureCoreParentDirs(core *layeredfs.Core, rootID, rel string) error {
	parts := strings.Split(cleanSlashRel(rel), "/")
	for i, cur := 0, ""; i < len(parts)-1; i++ {
		if cur == "" {
			cur = parts[i]
		} else {
			cur = cur + "/" + parts[i]
		}
		qp := layeredfs.QualifiedPath{RootID: rootID, Rel: cur}
		if _, err := core.Stat(qp); os.IsNotExist(err) {
			if _, mErr := core.Mkdir(qp, 0o755); mErr != nil && !os.IsExist(mErr) {
				return mErr
			}
		}
	}
	return nil
}
