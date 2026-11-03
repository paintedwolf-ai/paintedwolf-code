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

func prepareAndAuthorizeGitArgs(
	ctx context.Context,
	view *managedView,
	targetWT string,
	policy RemotePolicy,
	args []string,
) ([]string, error) {
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
		hasLocal := false
		for _, a := range args[1:] {
			if a == "-l" || a == "--local" || strings.Contains(a, "l") && strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") {
				hasLocal = true
				break
			}
		}
		if !hasLocal {
			out := make([]string, 0, len(args)+1)
			out = append(out, "repack", "-l")
			out = append(out, args[1:]...)
			return out, nil
		}
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
	out, code, err := gitexec.Run(ctx, targetWT, []string{"config", "--get", "remote." + remote + ".url"}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
	})
	if err == nil && code == 0 {
		url := strings.TrimSpace(string(out))
		if url != "" {
			return remote, url
		}
	}
	return remote, remote
}

func isRemoteApproved(approved map[string]bool, remote, remoteURL string) bool {
	if len(approved) == 0 {
		return false
	}
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
	if view.core == nil {
		return nil
	}
	changes, err := view.core.EnumerateChanges(false)
	if err != nil {
		return err
	}
	for _, ce := range changes {
		if ce.Path.RootID != view.rootID {
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
			if err := os.WriteFile(dstPath, raw, perm|0o200); err != nil {
				return err
			}
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

func snapshotWorktreeDigests(worktreeDir string) map[string]worktreeEntryState {
	res := make(map[string]worktreeEntryState)
	_ = filepath.WalkDir(worktreeDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, rErr := filepath.Rel(worktreeDir, path)
		if rErr != nil || rel == "." {
			return nil
		}
		if d.Name() == ".git" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		slashRel := filepath.ToSlash(rel)
		info, iErr := d.Info()
		if iErr != nil {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			link, _ := os.Readlink(path)
			res[slashRel] = worktreeEntryState{
				kind:       layeredfs.EntrySymlink,
				mode:       info.Mode(),
				linkTarget: link,
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		sum, _, hErr := hashFileBytes(path)
		if hErr == nil {
			res[slashRel] = worktreeEntryState{
				kind:   layeredfs.EntryRegular,
				mode:   info.Mode(),
				sha256: sum,
			}
		}
		return nil
	})
	return res
}

func syncWorktreeDeltaToCore(
	view *managedView,
	repoRel string,
	before, after map[string]worktreeEntryState,
) error {
	if view.core == nil {
		return nil
	}
	targetWT := view.worktreeDir
	if repoRel != "." {
		targetWT = filepath.Join(view.worktreeDir, filepath.FromSlash(repoRel))
	}
	qualifyRel := func(rel string) string {
		if repoRel == "." || repoRel == "" {
			return rel
		}
		return cleanSlashRel(repoRel + "/" + rel)
	}

	for rel, bState := range before {
		if _, exists := after[rel]; !exists {
			qp := layeredfs.QualifiedPath{RootID: view.rootID, Rel: qualifyRel(rel)}
			_ = view.core.Unlink(qp, bState.kind == layeredfs.EntryDirectory)
		}
	}

	for rel, aState := range after {
		bState, existed := before[rel]
		if existed && bState == aState {
			continue
		}
		fullRel := qualifyRel(rel)
		if err := ensureCoreParentDirs(view.core, view.rootID, fullRel); err != nil {
			return err
		}
		qp := layeredfs.QualifiedPath{RootID: view.rootID, Rel: fullRel}
		absFile := filepath.Join(targetWT, filepath.FromSlash(rel))
		switch aState.kind {
		case layeredfs.EntrySymlink:
			if existed {
				_ = view.core.Unlink(qp, false)
			}
			if _, err := view.core.Symlink(aState.linkTarget, qp); err != nil {
				return err
			}
		case layeredfs.EntryRegular:
			raw, err := os.ReadFile(absFile)
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
	if len(parts) <= 1 {
		return nil
	}
	cur := ""
	for i := 0; i < len(parts)-1; i++ {
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

func measureDirBytes(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, iErr := d.Info(); iErr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
