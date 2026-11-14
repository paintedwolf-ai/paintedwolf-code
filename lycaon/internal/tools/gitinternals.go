package tools

import (
	"path"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fsname"
	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
)

// CheckNativeGitMetadataPath enforces native file-tool restrictions on direct .git
// mutations at any root or submodule depth while allowing read-only inspection (#1671).
func CheckNativeGitMetadataPath(relPath string, writeIntent bool) (class string, denied bool, err error) {
	if !writeIntent {
		return "", false, nil
	}
	if c, d := IsGitInternalsWritePath(relPath); d {
		return c, true, privategit.ErrNativeToolGitMutationForbidden
	}
	clean := normalizeGitRelPath(relPath)
	if clean == "" {
		return "", false, nil
	}
	for _, part := range strings.Split(clean, "/") {
		base := part
		if idx := strings.Index(base, ":"); idx >= 0 {
			base = base[:idx]
		}
		if strings.EqualFold(base, ".git") {
			if pErr := privategit.CheckNativeToolPath(strings.ToLower(clean), true); pErr != nil {
				return "metadata", true, pErr
			}
			return "metadata", true, privategit.ErrNativeToolGitMutationForbidden
		}
	}
	return "", false, nil
}

func normalizeGitRelPath(relPath string) string {
	rel := filepath.ToSlash(strings.TrimSpace(relPath))
	if rel == "" || rel == "." {
		return ""
	}
	if strings.HasPrefix(rel, "@") {
		if idx := strings.Index(rel, "/"); idx > 0 {
			rel = rel[idx+1:]
		} else {
			return ""
		}
	}
	rel = filepath.ToSlash(path.Clean("/" + rel))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." {
		return ""
	}
	return rel
}

// IsGitInternalsWritePath identifies executable repository metadata.
func IsGitInternalsWritePath(relPath string) (class string, denied bool) {
	rel := filepath.ToSlash(strings.TrimSpace(relPath))
	if rel == "" || rel == "." {
		return "", false
	}
	if strings.HasPrefix(rel, "@") {
		if idx := strings.Index(rel, "/"); idx > 0 {
			rel = rel[idx+1:]
		} else {
			return "", false
		}
	}
	rel = filepath.ToSlash(path.Clean("/" + rel))
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || rel == "." {
		return "", false
	}

	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if !strings.EqualFold(part, ".git") {
			continue
		}
		return classifyGitInternalsRest(parts[i+1:])
	}
	return "", false
}

// classifyGitInternalsRest handles nested submodule and worktree metadata.
func classifyGitInternalsRest(rest []string) (class string, denied bool) {
	if len(rest) == 0 {
		return "", false
	}
	// Hook directories remain executable at any metadata depth.
	for _, part := range rest {
		if strings.EqualFold(part, "hooks") {
			return "hooks", true
		}
	}
	if !isGitConfigBasename(rest[len(rest)-1]) {
		return "", false
	}
	// Submodules and worktrees carry independent configuration.
	if len(rest) == 1 {
		return "config", true
	}
	if strings.EqualFold(rest[0], "modules") || strings.EqualFold(rest[0], "worktrees") {
		return "config", true
	}
	return "", false
}

func isGitConfigBasename(name string) bool {
	return fsname.EqualBasename(name, "config") || fsname.EqualBasename(name, "config.worktree")
}
