package projectroot

import (
	"path/filepath"
	"strings"
)

// Baseline spellings are the display grammar baseline manifests and overlay
// change lists use: they qualify against the primary root, so bare rel paths
// are primary-relative and "@label/rel" addresses another attached root.

// ParseBaselinePath maps a baseline-spelled display path to its root and
// scope-relative path. Labels compare case-insensitively. Legacy
// branch-relative "root-<hash>/rel" spellings are accepted so in-flight
// callers keep resolving; they take precedence over a primary-relative file
// that happens to live under a directory named like a branch directory.
// Parent traversal and absolute paths are rejected.
func ParseBaselinePath(roots []RootRef, raw string) (RootRef, string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RootRef{}, "", false
	}
	slash := filepath.ToSlash(raw)
	if strings.HasPrefix(slash, "@") {
		rest := strings.TrimPrefix(slash, "@")
		label, rel := rest, ""
		if idx := strings.Index(rest, "/"); idx >= 0 {
			label, rel = rest[:idx], strings.TrimPrefix(rest[idx:], "/")
		}
		if label == "" {
			// Unlabeled roots are addressed bare, never as "@".
			return RootRef{}, "", false
		}
		root, err := rootByLabel(roots, label)
		if err != nil {
			return RootRef{}, "", false
		}
		if rel == "" {
			rel = "."
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if !baselineRelSafe(rel) {
			return RootRef{}, "", false
		}
		return root, rel, true
	}
	if root, ok := RootForBranchRel(roots, slash); ok {
		_, rel, _ := strings.Cut(slash, "/")
		rel = filepath.ToSlash(filepath.Clean(rel))
		if !baselineRelSafe(rel) {
			return RootRef{}, "", false
		}
		return root, rel, true
	}
	primary, err := PrimaryRoot(roots)
	if err != nil {
		return RootRef{}, "", false
	}
	rel := filepath.ToSlash(filepath.Clean(slash))
	if !baselineRelSafe(rel) {
		return RootRef{}, "", false
	}
	return primary, rel, true
}

func baselineRelSafe(rel string) bool {
	if rel == "" || rel == "." {
		return true
	}
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	return !strings.Contains(rel, "\x00")
}

// BaselineDisplayPath spells a structured identity the way baseline manifests
// and overlay change lists do: bare off the primary root, @label-qualified off
// other labeled roots, and bare off unlabeled roots.
func BaselineDisplayPath(roots []RootRef, rootID, rel string) string {
	primary, err := PrimaryRoot(roots)
	if err != nil {
		return rel
	}
	return DisplayPath(roots, primary.ID, rootID, rel)
}
