package privategit

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/gitexec"
)

func tryCaptureSourceMetadataOnce(layout sourceRepoLayout, stageGitDir string, attempt int, hook func(worktreeDir string, attempt int)) (bool, error) {
	before, hasLock, err := snapshotMetadataDigests(layout)
	if err != nil || hasLock {
		return false, err
	}
	if err := copyCapturedMetadataToStage(layout, stageGitDir); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if hook != nil {
		hook(layout.worktreeDir, attempt)
	}
	after, hasLockAfter, err := snapshotMetadataDigests(layout)
	if err != nil || hasLockAfter || len(before) != len(after) {
		return false, err
	}
	for k, v := range before {
		if after[k] != v {
			return false, nil
		}
	}
	return true, nil
}

func snapshotMetadataDigests(layout sourceRepoLayout) (map[string]string, bool, error) {
	digests := make(map[string]string)
	for _, base := range []string{layout.gitDir, layout.commonDir} {
		for _, lockName := range []string{"HEAD.lock", "index.lock", "config.lock", "packed-refs.lock"} {
			if _, err := os.Lstat(filepath.Join(base, lockName)); err == nil {
				return nil, true, nil
			}
		}
	}
	files := []struct {
		key  string
		path string
	}{
		{"HEAD", filepath.Join(layout.gitDir, "HEAD")},
		{"index", filepath.Join(layout.gitDir, "index")},
		{"config", filepath.Join(layout.commonDir, "config")},
		{"packed-refs", filepath.Join(layout.commonDir, "packed-refs")},
	}
	for _, f := range files {
		sum, _, err := hashFileBytes(f.path)
		if err != nil {
			if os.IsNotExist(err) {
				digests[f.key] = "absent"
				continue
			}
			return nil, false, err
		}
		digests[f.key] = sum
	}
	refsDir := filepath.Join(layout.commonDir, "refs")
	hasLock := false
	walkErr := filepath.WalkDir(refsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".lock") {
			hasLock = true
			return fs.SkipAll
		}
		rel, rErr := filepath.Rel(refsDir, path)
		if rErr != nil {
			return rErr
		}
		sum, _, hErr := hashFileBytes(path)
		if hErr != nil {
			if os.IsNotExist(hErr) {
				hasLock = true
				return fs.SkipAll
			}
			return hErr
		}
		digests["refs/"+filepath.ToSlash(rel)] = sum
		return nil
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return nil, false, walkErr
	}
	return digests, hasLock, nil
}

func copyOptionalStageFile(src, dst string) error {
	raw, err := os.ReadFile(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.WriteFile(dst, raw, 0o644)
}

func copyCapturedMetadataToStage(layout sourceRepoLayout, stageGitDir string) error {
	for _, sub := range []string{"refs/heads", "refs/tags", "objects/info", "objects/pack", "info", "logs"} {
		if err := os.MkdirAll(filepath.Join(stageGitDir, sub), 0o755); err != nil {
			return err
		}
	}
	headBytes, err := os.ReadFile(filepath.Join(layout.gitDir, "HEAD"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stageGitDir, "HEAD"), headBytes, 0o644); err != nil {
		return err
	}
	if err := copyOptionalStageFile(filepath.Join(layout.gitDir, "index"), filepath.Join(stageGitDir, "index")); err != nil {
		return err
	}
	cfgBytes, cErr := os.ReadFile(filepath.Join(layout.commonDir, "config"))
	if cErr != nil && !os.IsNotExist(cErr) {
		return cErr
	}
	if err := os.WriteFile(filepath.Join(stageGitDir, "config"), SanitizeGitConfig(cfgBytes), 0o644); err != nil {
		return err
	}
	if err := copyOptionalStageFile(filepath.Join(layout.commonDir, "packed-refs"), filepath.Join(stageGitDir, "packed-refs")); err != nil {
		return err
	}
	if err := copyMetadataTree(filepath.Join(layout.commonDir, "refs"), filepath.Join(stageGitDir, "refs")); err != nil {
		return err
	}
	if err := copyMetadataTree(filepath.Join(layout.commonDir, "logs"), filepath.Join(stageGitDir, "logs")); err != nil {
		return err
	}
	if layout.gitDir != layout.commonDir {
		if err := copyOptionalStageFile(filepath.Join(layout.gitDir, "logs", "HEAD"), filepath.Join(stageGitDir, "logs", "HEAD")); err != nil {
			return err
		}
	}
	return copyOptionalStageFile(filepath.Join(layout.commonDir, "info", "exclude"), filepath.Join(stageGitDir, "info", "exclude"))
}

func copyMetadataTree(srcDir, dstDir string) error {
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return ErrUnauthorizedGitTarget
		}
		rel, rErr := filepath.Rel(srcDir, path)
		if rErr != nil {
			return rErr
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if strings.HasSuffix(d.Name(), ".lock") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(target, raw, 0o644)
	})
}

func setupPrivateLinkedWorktreeMetadata(privateGitDir, worktreeDir, wtName string) error {
	wtDir := filepath.Join(privateGitDir, "worktrees", wtName)
	if err := os.MkdirAll(wtDir, 0o755); err != nil {
		return err
	}
	headBytes, err := os.ReadFile(filepath.Join(privateGitDir, "HEAD"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wtDir, "HEAD"), headBytes, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(wtDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(wtDir, "gitdir"), []byte(filepath.Join(worktreeDir, ".git")+"\n"), 0o644)
}

func collectCapturedRefAndIndexOIDs(ctx context.Context, stageGitDir string) ([]string, map[string]string, error) {
	refs := make(map[string]string)
	if packed, err := os.ReadFile(filepath.Join(stageGitDir, "packed-refs")); err == nil {
		for _, line := range strings.Split(string(packed), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && len(fields[0]) == 40 && isHexString(fields[0]) {
				refs[fields[1]] = strings.ToLower(fields[0])
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	refsRoot := filepath.Join(stageGitDir, "refs")
	walkErr := filepath.WalkDir(refsRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rErr := filepath.Rel(stageGitDir, path)
		if rErr != nil {
			return rErr
		}
		refName := filepath.ToSlash(rel)
		raw, rErr := os.ReadFile(path)
		if rErr != nil {
			return rErr
		}
		val := strings.TrimSpace(string(raw))
		if len(val) == 40 && isHexString(val) {
			refs[refName] = strings.ToLower(val)
			return nil
		}
		if strings.HasPrefix(val, "ref:") {
			return nil
		}
		return fmt.Errorf("%w: invalid ref %s", ErrCorruptObjectStore, refName)
	})
	if walkErr != nil && !os.IsNotExist(walkErr) {
		return nil, nil, walkErr
	}
	var oids []string
	for _, oid := range refs {
		oids = append(oids, oid)
	}
	headRaw, err := os.ReadFile(filepath.Join(stageGitDir, "HEAD"))
	if err != nil {
		return nil, nil, err
	}
	headStr := strings.TrimSpace(string(headRaw))
	if strings.HasPrefix(headStr, "ref:") {
		targetRef := strings.TrimSpace(strings.TrimPrefix(headStr, "ref:"))
		if oid, ok := refs[targetRef]; ok {
			oids = append(oids, oid)
		}
	} else if len(headStr) == 40 && isHexString(headStr) {
		oids = append(oids, strings.ToLower(headStr))
	} else {
		return nil, nil, fmt.Errorf("%w: invalid HEAD %q", ErrCorruptObjectStore, headStr)
	}
	idxPath := filepath.Join(stageGitDir, "index")
	if _, err := os.Stat(idxPath); err == nil {
		out, code, gErr := gitexec.Run(ctx, filepath.Dir(stageGitDir), []string{"--git-dir=" + stageGitDir, "ls-files", "--stage"}, gitexec.Opts{
			Profile:   gitexec.ProfileHermetic,
			IndexFile: idxPath,
		})
		if gErr != nil || code != 0 {
			return nil, nil, fmt.Errorf("%w: invalid git index (exit %d): %v: %s", ErrCorruptObjectStore, code, gErr, strings.TrimSpace(string(out)))
		}
		for _, line := range bytes.Split(out, []byte{'\n'}) {
			fields := bytes.Fields(line)
			if len(fields) >= 2 && string(fields[0]) != "160000" && len(fields[1]) == 40 && isHexString(string(fields[1])) {
				oids = append(oids, strings.ToLower(string(fields[1])))
			}
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	sort.Strings(oids)
	return oids, refs, nil
}
