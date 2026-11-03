package adapter

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

func resolveValidatedObjectDirs(commonDir, gitDir string, allowed []string) ([]string, error) {
	primaryObj := filepath.Join(commonDir, "objects")
	dirs := []string{primaryObj}
	if gitDir != commonDir {
		wtObj := filepath.Join(gitDir, "objects")
		if st, err := os.Stat(wtObj); err == nil && st.IsDir() {
			dirs = append(dirs, wtObj)
		}
	}
	altPath := filepath.Join(primaryObj, "info", "alternates")
	linfo, err := os.Lstat(altPath)
	if errors.Is(err, fs.ErrNotExist) {
		return dirs, nil
	}
	if err != nil || linfo.Mode()&fs.ModeSymlink != 0 {
		return nil, unauthorizedGitError("invalid alternates file")
	}
	raw, err := os.ReadFile(altPath)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		target := line
		if !filepath.IsAbs(target) {
			target = filepath.Join(primaryObj, target)
		}
		st, sErr := os.Lstat(target)
		if sErr != nil || !st.IsDir() || st.Mode()&fs.ModeSymlink != 0 {
			return nil, unauthorizedGitError("unresolved alternate object directory")
		}
		cleanTarget := canonicalGitPath(target)
		if !isPathUnderAllowed(cleanTarget, allowed) {
			return nil, unauthorizedGitError("alternate object directory outside granted roots")
		}
		dirs = append(dirs, cleanTarget)
	}
	return dirs, nil
}

func surveyGitDirsWithinLimits(ctx context.Context, gitDir, commonDir string, objDirs []string, maxEntries, maxBytes int64) (int64, int64, error) {
	var entries, totalBytes int64
	seen := make(map[string]bool)
	scanList := []string{gitDir}
	if commonDir != gitDir {
		scanList = append(scanList, commonDir)
	}
	scanList = append(scanList, objDirs...)

	for _, root := range scanList {
		if _, err := os.Stat(root); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if cErr := ctx.Err(); cErr != nil {
				return &AdapterError{AdapterID: AdapterEagerCopy, Op: "seed_git", ReasonCode: ReasonContextCanceled, Diagnostic: cErr.Error(), Cause: cErr}
			}
			cleanP := filepath.Clean(p)
			if seen[cleanP] {
				return nil
			}
			seen[cleanP] = true
			if d.Type()&fs.ModeSymlink != 0 {
				return unauthorizedGitError("symlink inside .git metadata is forbidden")
			}
			entries++
			if !d.IsDir() {
				info, iErr := d.Info()
				if iErr != nil {
					return iErr
				}
				totalBytes += info.Size()
			}
			if (maxEntries > 0 && entries > maxEntries) || (maxBytes > 0 && totalBytes > maxBytes) {
				return privategit.ErrSeedLimitExceeded
			}
			return nil
		})
		if err != nil {
			return entries, totalBytes, err
		}
	}
	return entries, totalBytes, nil
}

func populateSanitizedPrivateGitMetadata(gitDir, commonDir, stageDir string) (string, error) {
	for _, sub := range []string{"refs/heads", "refs/tags", "objects/info", "objects/pack", "info", "logs"} {
		if err := os.MkdirAll(filepath.Join(stageDir, sub), 0o700); err != nil {
			return "", err
		}
	}
	headRef := "ref: refs/heads/main\n"
	if rawHead, err := os.ReadFile(filepath.Join(gitDir, "HEAD")); err == nil {
		headRef = strings.TrimSpace(string(rawHead)) + "\n"
	}
	if err := os.WriteFile(filepath.Join(stageDir, "HEAD"), []byte(headRef), 0o600); err != nil {
		return "", err
	}
	_ = streamCopyIfPresent(filepath.Join(gitDir, "index"), filepath.Join(stageDir, "index"))
	_ = streamCopyIfPresent(filepath.Join(commonDir, "packed-refs"), filepath.Join(stageDir, "packed-refs"))
	_ = streamCopyIfPresent(filepath.Join(commonDir, "info", "exclude"), filepath.Join(stageDir, "info", "exclude"))

	cfgRaw, _ := os.ReadFile(filepath.Join(commonDir, "config"))
	if err := os.WriteFile(filepath.Join(stageDir, "config"), privategit.SanitizeGitConfig(cfgRaw), 0o600); err != nil {
		return "", err
	}
	if err := streamCopyTreeIfPresent(filepath.Join(commonDir, "refs"), filepath.Join(stageDir, "refs")); err != nil {
		return "", err
	}
	_ = streamCopyTreeIfPresent(filepath.Join(commonDir, "logs"), filepath.Join(stageDir, "logs"))
	return strings.TrimSpace(headRef), nil
}

func streamCopyIfPresent(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, cErr := io.Copy(out, in)
	closeErr := out.Close()
	if cErr != nil {
		return cErr
	}
	return closeErr
}

func streamCopyTreeIfPresent(srcDir, dstDir string) error {
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rErr := filepath.Rel(srcDir, p)
		if rErr != nil || rel == "." {
			return rErr
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dstDir, rel), 0o700)
		}
		if strings.HasSuffix(d.Name(), ".lock") {
			return nil
		}
		return streamCopyIfPresent(p, filepath.Join(dstDir, rel))
	})
}

func canonicalGitPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	if resolved, rErr := filepath.EvalSymlinks(abs); rErr == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

func isPathUnderAllowed(target string, allowed []string) bool {
	target = canonicalGitPath(target)
	if target == "" {
		return false
	}
	for _, r := range allowed {
		if target == r || strings.HasPrefix(target, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func unauthorizedGitError(diag string) error {
	return &AdapterError{
		AdapterID:  AdapterEagerCopy,
		Op:         "seed_git",
		ReasonCode: ReasonUnauthorizedRoot,
		Diagnostic: diag,
		Cause:      privategit.ErrUnauthorizedGitTarget,
	}
}
