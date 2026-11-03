package privategit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type sourceRepoLayout struct {
	worktreeDir   string
	gitDir        string
	commonDir     string
	objectDirs    []string
	isLinkedWT    bool
	worktreeName  string
	submoduleRels []string
}

func resolveSourceRepoLayout(
	rootTrunkPath string,
	repoRel string,
	authorizedDirs []string,
	forbiddenDirs []string,
) (sourceRepoLayout, error) {
	canonicalRoot := canonicalCleanPath(rootTrunkPath)
	if canonicalRoot == "" {
		return sourceRepoLayout{}, ErrUnauthorizedGitTarget
	}
	worktreeDir := canonicalRoot
	if repoRel != "." && repoRel != "" {
		worktreeDir = filepath.Join(canonicalRoot, filepath.FromSlash(repoRel))
	}
	dotGit := filepath.Join(worktreeDir, ".git")
	linfo, err := os.Lstat(dotGit)
	if err != nil {
		return sourceRepoLayout{}, err
	}
	if linfo.Mode()&fs.ModeSymlink != 0 {
		return sourceRepoLayout{}, ErrUnauthorizedGitTarget
	}

	allowed := make([]string, 0, len(authorizedDirs)+1)
	allowed = append(allowed, canonicalRoot)
	for _, a := range authorizedDirs {
		if c := canonicalCleanPath(a); c != "" {
			allowed = append(allowed, c)
		}
	}

	var gitDir, commonDir string
	var isLinkedWT bool
	var wtName string

	if linfo.IsDir() {
		gitDir = canonicalCleanPath(dotGit)
		commonDir = gitDir
	} else if linfo.Mode().IsRegular() {
		raw, rErr := os.ReadFile(dotGit)
		if rErr != nil {
			return sourceRepoLayout{}, rErr
		}
		target, ok := parseGitdirPointer(worktreeDir, string(raw))
		if !ok {
			return sourceRepoLayout{}, ErrUnauthorizedGitTarget
		}
		gitDir = canonicalCleanPath(target)
		if gitDir == "" || isUnderAny(gitDir, forbiddenDirs) {
			return sourceRepoLayout{}, ErrUnauthorizedGitTarget
		}
		resolvedCommon, hasCommon, cErr := resolveCommonDirFile(gitDir)
		if cErr != nil {
			return sourceRepoLayout{}, cErr
		}
		commonDir = resolvedCommon
		if isUnderAny(commonDir, forbiddenDirs) {
			return sourceRepoLayout{}, ErrUnauthorizedGitTarget
		}
		if hasCommon {
			isLinkedWT = true
			wtName = filepath.Base(gitDir)
		}
		if !isAuthorizedGitDir(worktreeDir, dotGit, gitDir, commonDir, allowed) {
			return sourceRepoLayout{}, ErrUnauthorizedGitTarget
		}
	} else {
		return sourceRepoLayout{}, ErrUnauthorizedGitTarget
	}

	objDirs, err := collectAuthorizedObjectDirs(commonDir, gitDir, allowed, forbiddenDirs)
	if err != nil {
		return sourceRepoLayout{}, err
	}
	subRels := parseSubmodulePaths(filepath.Join(worktreeDir, ".gitmodules"))
	return sourceRepoLayout{
		worktreeDir:   worktreeDir,
		gitDir:        gitDir,
		commonDir:     commonDir,
		objectDirs:    objDirs,
		isLinkedWT:    isLinkedWT,
		worktreeName:  wtName,
		submoduleRels: subRels,
	}, nil
}

func parseGitdirPointer(baseDir, content string) (string, bool) {
	line := strings.TrimSpace(content)
	if !strings.HasPrefix(line, "gitdir:") {
		return "", false
	}
	val := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if val == "" || strings.ContainsRune(val, 0) {
		return "", false
	}
	if !filepath.IsAbs(val) {
		val = filepath.Join(baseDir, val)
	}
	return filepath.Clean(val), true
}

func resolveCommonDirFile(gitDir string) (string, bool, error) {
	p := filepath.Join(gitDir, "commondir")
	linfo, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return gitDir, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if linfo.Mode()&fs.ModeSymlink != 0 {
		return "", false, ErrUnauthorizedGitTarget
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", false, err
	}
	val := strings.TrimSpace(string(raw))
	if val == "" {
		return "", false, ErrUnauthorizedGitTarget
	}
	if !filepath.IsAbs(val) {
		val = filepath.Join(gitDir, val)
	}
	return canonicalCleanPath(val), true, nil
}

func isAuthorizedGitDir(worktreeDir, dotGit, gitDir, commonDir string, allowed []string) bool {
	if isUnderAny(gitDir, allowed) && isUnderAny(commonDir, allowed) {
		return true
	}
	// Verify bidirectional worktree back-pointer `<gitDir>/gitdir` -> `<worktreeDir>/.git`.
	backRaw, err := os.ReadFile(filepath.Join(gitDir, "gitdir"))
	if err != nil {
		return false
	}
	backTarget := strings.TrimSpace(string(backRaw))
	if !filepath.IsAbs(backTarget) {
		backTarget = filepath.Join(gitDir, backTarget)
	}
	if canonicalCleanPath(backTarget) != canonicalCleanPath(dotGit) {
		return false
	}
	// CommonDir must be a valid Git directory containing HEAD and config.
	if _, err := os.Stat(filepath.Join(commonDir, "HEAD")); err != nil {
		return false
	}
	if _, err := os.Stat(filepath.Join(commonDir, "config")); err != nil {
		return false
	}
	return filepath.Dir(filepath.Dir(commonDir)) == filepath.Dir(worktreeDir)
}

func collectAuthorizedObjectDirs(commonDir, gitDir string, allowed, forbidden []string) ([]string, error) {
	primaryObj := filepath.Join(commonDir, "objects")
	dirs := []string{primaryObj}
	if gitDir != commonDir {
		wtObj := filepath.Join(gitDir, "objects")
		if info, err := os.Stat(wtObj); err == nil && info.IsDir() {
			dirs = append(dirs, wtObj)
		}
	}
	altPath := filepath.Join(primaryObj, "info", "alternates")
	raw, err := os.ReadFile(altPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return dirs, nil
		}
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
		cleanTarget := canonicalCleanPath(target)
		if cleanTarget == "" || isUnderAny(cleanTarget, forbidden) || !isUnderAny(cleanTarget, allowed) {
			return nil, ErrUnauthorizedGitTarget
		}
		dirs = append(dirs, cleanTarget)
	}
	return dirs, nil
}

func parseSubmodulePaths(gitmodulesPath string) []string {
	raw, err := os.ReadFile(gitmodulesPath)
	if err != nil {
		return nil
	}
	var paths []string
	for _, line := range strings.Split(string(raw), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "path") {
			continue
		}
		rel := filepath.ToSlash(filepath.Clean(strings.TrimSpace(v)))
		if rel != "" && rel != "." && !strings.HasPrefix(rel, "..") && !strings.HasPrefix(rel, "/") {
			paths = append(paths, rel)
		}
	}
	sort.Strings(paths)
	return paths
}

func canonicalCleanPath(p string) string {
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

func isUnderAny(target string, roots []string) bool {
	target = canonicalCleanPath(target)
	if target == "" {
		return false
	}
	for _, r := range roots {
		rc := canonicalCleanPath(r)
		if rc == "" {
			continue
		}
		if target == rc || strings.HasPrefix(target, rc+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func hashFileBytes(path string) (string, int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), int64(len(raw)), nil
}
