package projectroot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RootRef is a project folder root exposed to tools, prompts, and the path resolver.
type RootRef struct {
	ID        string
	Label     string
	Path      string
	IsPrimary bool
}

// VirtualScratchLabel names the host namespace for the invoking session's
// scratch folder. Virtual roots are addressed like attached roots, so no
// attached root may carry their labels.
const VirtualScratchLabel = "scratch"

var (
	ErrNoProjectRoots   = errors.New("project has no folder roots")
	ErrInvalidRootSet   = errors.New("project root set is invalid")
	ErrUnknownRootLabel = errors.New("unknown root label")
	ErrPathEscape       = errors.New("path escapes project root boundary")
)

// IsVirtualRootLabel reports whether label names a host namespace. Labels
// compare case-insensitively, as attached root labels do.
func IsVirtualRootLabel(label string) bool {
	return strings.EqualFold(strings.TrimSpace(label), VirtualScratchLabel)
}

// BranchDirForID derives a stable host-only directory from a root ID.
func BranchDirForID(id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", fmt.Errorf("project root id required")
	}
	sum := sha256.Sum256([]byte(id))
	return "root-" + hex.EncodeToString(sum[:]), nil
}

// PrimaryRoot returns the sole primary root.
func PrimaryRoot(roots []RootRef) (RootRef, error) {
	if len(roots) == 0 {
		return RootRef{}, ErrNoProjectRoots
	}
	var primary RootRef
	count := 0
	for _, r := range roots {
		if r.IsPrimary {
			primary = r
			count++
		}
	}
	if count != 1 {
		return RootRef{}, fmt.Errorf("%w: expected one primary root, found %d", ErrInvalidRootSet, count)
	}
	return primary, nil
}

// ActiveRoot resolves workspace_root_id against roots, defaulting to primary.
func ActiveRoot(roots []RootRef, activeRootID string) (RootRef, error) {
	if len(roots) == 0 {
		return RootRef{}, ErrNoProjectRoots
	}
	want := strings.TrimSpace(activeRootID)
	if want != "" {
		for _, r := range roots {
			if r.ID == want {
				return r, nil
			}
		}
		return RootRef{}, fmt.Errorf("%w: %s", ErrUnknownRootLabel, want)
	}
	return PrimaryRoot(roots)
}

// IsUnionDiscoveryPath reports whether find/grep/list_dir should walk every root.
func IsUnionDiscoveryPath(path string) bool {
	p := strings.TrimSpace(path)
	return p == "" || p == "." || p == "./"
}

// ResolveAbs maps a model path to an absolute filesystem path under a project root.
func ResolveAbs(roots []RootRef, activeRootID, path string) (abs string, root RootRef, err error) {
	if len(roots) == 0 {
		return "", RootRef{}, ErrNoProjectRoots
	}
	if strings.ContainsRune(path, 0) {
		return "", RootRef{}, fmt.Errorf("path contains NUL byte")
	}
	raw := strings.TrimSpace(path)
	if raw == "" {
		return "", RootRef{}, ErrNoProjectRoots
	}

	var rel string
	var chosen RootRef

	if strings.HasPrefix(raw, "@") {
		rest := strings.TrimPrefix(raw, "@")
		slash := strings.Index(rest, "/")
		label := rest
		if slash >= 0 {
			label = rest[:slash]
			rel = strings.TrimPrefix(rest[slash:], "/")
		}
		if label == "" {
			return "", RootRef{}, fmt.Errorf("%w: %q", ErrUnknownRootLabel, raw)
		}
		chosen, err = rootByLabel(roots, label)
		if err != nil {
			return "", RootRef{}, err
		}
		if rel == "" {
			rel = "."
		}
	} else if filepath.IsAbs(raw) {
		clean := filepath.Clean(raw)
		chosen, rel, err = rootForAbsolute(roots, clean)
		if err != nil {
			return "", RootRef{}, err
		}
	} else {
		chosen, err = ActiveRoot(roots, activeRootID)
		if err != nil {
			return "", RootRef{}, err
		}
		rel = strings.TrimPrefix(filepath.Clean(raw), "."+string(os.PathSeparator))
		if rel == "." {
			rel = "."
		}
	}

	abs, err = resolveUnderRoot(chosen.Path, rel)
	if err != nil {
		return "", RootRef{}, err
	}
	return abs, chosen, nil
}

func rootByLabel(roots []RootRef, label string) (RootRef, error) {
	for _, r := range roots {
		if strings.EqualFold(r.Label, label) {
			return r, nil
		}
	}
	return RootRef{}, fmt.Errorf("%w: %s", ErrUnknownRootLabel, label)
}

func rootForAbsolute(roots []RootRef, abs string) (RootRef, string, error) {
	var match RootRef
	var rel string
	found := false
	for _, r := range roots {
		rootAbs, err := filepath.Abs(r.Path)
		if err != nil {
			continue
		}
		rootAbs = filepath.Clean(rootAbs)
		relPath, err := filepath.Rel(rootAbs, abs)
		if err != nil {
			continue
		}
		if relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator)) {
			continue
		}
		if !found || len(relPath) < len(rel) {
			match = r
			rel = relPath
			found = true
		}
	}
	if !found {
		return RootRef{}, "", fmt.Errorf("%w: %q", ErrPathEscape, abs)
	}
	if rel == "" {
		rel = "."
	}
	return match, rel, nil
}

func resolveUnderRoot(rootPath, rel string) (string, error) {
	root, err := filepath.Abs(rootPath)
	if err != nil {
		return "", fmt.Errorf("invalid root path: %w", err)
	}
	root = filepath.Clean(root)
	clean := filepath.Clean(rel)
	if filepath.IsAbs(clean) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
	}
	clean = strings.TrimPrefix(clean, "."+string(os.PathSeparator))
	full := filepath.Join(root, clean)
	relCheck, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("path resolution failed: %w", err)
	}
	if relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscape, rel)
	}
	return full, nil
}

// Qualify returns the canonical display path for abs under root (primary unprefixed).
func Qualify(primary, root RootRef, abs string) string {
	rootAbs, err := filepath.Abs(root.Path)
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil || rel == "." {
		if root.IsPrimary || root.ID == primary.ID {
			return "."
		}
		if root.Label != "" {
			return "@" + root.Label
		}
		return abs
	}
	rel = filepath.ToSlash(rel)
	if root.IsPrimary || root.ID == primary.ID {
		return rel
	}
	if root.Label != "" {
		return "@" + root.Label + "/" + rel
	}
	return rel
}

// ScopeRel returns the repo-relative path used for sandbox profile globs.
func ScopeRel(root RootRef, abs string) string {
	rootAbs, err := filepath.Abs(root.Path)
	if err != nil {
		return abs
	}
	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}
