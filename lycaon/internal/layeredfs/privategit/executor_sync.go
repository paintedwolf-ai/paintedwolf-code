package privategit

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/layeredfs"
)

func prepareAndAuthorizeGitArgs(ctx context.Context, view *managedView, targetWT, privGitDir string, policy RemotePolicy, args []string) ([]string, error) {
	sub := args[0]
	switch sub {
	case "fetch", "pull", "ls-remote", "push":
		isPush := sub == "push"
		if (isPush && !policy.AllowPush) || (!isPush && !policy.AllowFetch) {
			return nil, fmt.Errorf("%w: %s is disabled", ErrRemoteOperationRefused, sub)
		}
		remote, remoteURL := resolveTargetRemote(ctx, targetWT, privGitDir, args[1:])
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

func resolveTargetRemote(ctx context.Context, targetWT, privGitDir string, restArgs []string) (string, string) {
	remote := "origin"
	for _, a := range restArgs {
		if !strings.HasPrefix(a, "-") && a != "" {
			remote = a
			break
		}
	}
	cfgDir := targetWT
	cfgArgs := []string{"config", "--get", "remote." + remote + ".url"}
	if privGitDir != "" && privGitDir != filepath.Join(targetWT, ".git") {
		cfgDir = filepath.Dir(privGitDir)
		cfgArgs = []string{"--git-dir=" + privGitDir, "config", "--get", "remote." + remote + ".url"}
	}
	if out, code, err := gitexec.Run(ctx, cfgDir, cfgArgs, gitexec.Opts{Profile: gitexec.ProfileHermetic}); err == nil && code == 0 {
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
	if err := syncViewGitOverlay(view); err != nil {
		return err
	}
	if view == nil || view.core == nil || (!view.materialized && view.kernelMounted) {
		return nil
	}
	changes, err := view.core.EnumerateChanges(false)
	if err != nil {
		return err
	}
	for _, ce := range changes {
		if ce.Path.RootID != view.rootID || layeredfs.IsGitMetadataPath(ce.Path.Rel) {
			continue
		}
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
			// Unlink any existing hardlink to trunkDir before writing private upper-layer bytes.
			_ = os.RemoveAll(dstPath)
			if err := os.WriteFile(dstPath, raw, perm|0o200); err != nil {
				return err
			}
		}
	}
	return nil
}

type worktreeStatEntry struct {
	kind       layeredfs.EntryKind
	mode       fs.FileMode
	size       int64
	mtimeNano  int64
	ctimeSec   int64
	ctimeNsec  int64
	dev        uint64
	ino        uint64
	linkTarget string
}

func captureWorktreeMutationBaseline(view *managedView, targetWT string) map[string]worktreeStatEntry {
	if view == nil || view.core == nil || (!view.materialized && view.kernelMounted) {
		return nil
	}
	res := make(map[string]worktreeStatEntry)
	_ = filepath.WalkDir(targetWT, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rErr := filepath.Rel(targetWT, path)
		if rErr != nil || rel == "." {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, iErr := d.Info()
		if iErr != nil || info.IsDir() {
			return nil
		}
		le := fileInfoToLowerEntry(info)
		entry := worktreeStatEntry{
			kind:      le.Kind,
			mode:      le.Mode,
			size:      le.Size,
			mtimeNano: le.ModTime.UnixNano(),
			ctimeSec:  le.CtimeSec,
			ctimeNsec: le.CtimeNsec,
			dev:       le.Dev,
			ino:       le.Ino,
		}
		if entry.kind == layeredfs.EntrySymlink {
			entry.linkTarget, _ = os.Readlink(path)
		}
		res[filepath.ToSlash(rel)] = entry
		return nil
	})
	return res
}

func syncWorktreeMutationsToCore(view *managedView, repoRel string, before map[string]worktreeStatEntry) error {
	if view == nil || view.core == nil || before == nil {
		return nil
	}
	targetWT := view.worktreeDir
	if repoRel != "." {
		targetWT = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
	}
	after := captureWorktreeMutationBaseline(view, targetWT)
	qualify := func(rel string) string {
		if repoRel == "." || repoRel == "" {
			return rel
		}
		return cleanSlashRel(repoRel + "/" + rel)
	}

	for rel, bState := range before {
		if _, exists := after[rel]; !exists {
			qp := layeredfs.QualifiedPath{RootID: view.rootID, Rel: qualify(rel)}
			_ = view.core.Unlink(qp, bState.kind == layeredfs.EntryDirectory)
		}
	}

	for rel, aState := range after {
		bState, existed := before[rel]
		if existed && bState == aState {
			continue
		}
		fullRel := qualify(rel)
		if err := ensureCoreParentDirs(view.core, view.rootID, fullRel); err != nil {
			return err
		}
		qp := layeredfs.QualifiedPath{RootID: view.rootID, Rel: fullRel}
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
