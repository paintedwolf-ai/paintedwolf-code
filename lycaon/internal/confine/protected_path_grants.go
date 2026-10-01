package confine

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/fspath"
)

// MaxProtectedPathGrants caps protected-path transactions per execution.
const MaxProtectedPathGrants = 8

// ProtectedPathGrant permits one approved protected path.
type ProtectedPathGrant struct {
	// ApprovedPath is the path the human approved.
	ApprovedPath string
	// ResolvedPath has a canonical parent and may not exist yet.
	ResolvedPath string
	// Subtree covers descendants of an approved directory.
	Subtree bool
}

// ProtectedPathDropReason is a stable machine reason a grant was not applied.
type ProtectedPathDropReason string

const (
	ProtectedDropNotAbsolute  ProtectedPathDropReason = "not_absolute"
	ProtectedDropInvalidUTF8  ProtectedPathDropReason = "invalid_utf8"
	ProtectedDropNUL          ProtectedPathDropReason = "nul"
	ProtectedDropTooLong      ProtectedPathDropReason = "too_long"
	ProtectedDropNoParent     ProtectedPathDropReason = "parent_missing"
	ProtectedDropRepointed    ProtectedPathDropReason = "repointed"
	ProtectedDropNotRegular   ProtectedPathDropReason = "not_regular_file"
	ProtectedDropIsSymlink    ProtectedPathDropReason = "symlink"
	ProtectedDropControlPlane ProtectedPathDropReason = "control_plane"
)

// ProtectedPathDrop records a grant refused at the choke point.
type ProtectedPathDrop struct {
	Grant  ProtectedPathGrant
	Reason ProtectedPathDropReason
}

const maxProtectedPathBytes = 1024

// SplitChatOverlay separates protected paths from ordinary roots.
func SplitChatOverlay(paths []string) (roots []string, protected []ProtectedPathGrant) {
	for _, path := range paths {
		switch ClassifyBlockedWrite(path).Kind {
		case WriteSubjectCredentialStore, WriteSubjectKeyMaterial:
			protected = append(protected, NewProtectedPathGrant(path))
		default:
			roots = append(roots, path)
		}
	}
	return roots, protected
}

// NewProtectedPathGrant canonicalizes an approved path.
func NewProtectedPathGrant(approvedPath string) ProtectedPathGrant {
	approved := filepath.Clean(strings.TrimSpace(approvedPath))
	dir := fspath.CanonicalPath(filepath.Dir(approved))
	if dir == "" {
		return ProtectedPathGrant{ApprovedPath: approved}
	}
	resolved := filepath.Join(dir, filepath.Base(approved))
	subtree := false
	if info, statErr := os.Lstat(resolved); statErr == nil && info.IsDir() {
		subtree = true
	}
	return ProtectedPathGrant{
		ApprovedPath: approved,
		ResolvedPath: resolved,
		Subtree:      subtree,
	}
}

// validateProtectedPathGrants rejects stale or redirected authority.
func validateProtectedPathGrants(grants []ProtectedPathGrant) ([]ProtectedPathGrant, []ProtectedPathDrop, error) {
	if len(grants) == 0 {
		return nil, nil, nil
	}
	applied := make([]ProtectedPathGrant, 0, len(grants))
	var dropped []ProtectedPathDrop
	drop := func(g ProtectedPathGrant, reason ProtectedPathDropReason) {
		dropped = append(dropped, ProtectedPathDrop{Grant: g, Reason: reason})
	}
	for _, g := range grants {
		if reason := protectedPathSyntaxReason(g.ApprovedPath); reason != "" {
			drop(g, reason)
			continue
		}
		if reason := protectedPathSyntaxReason(g.ResolvedPath); reason != "" {
			drop(g, reason)
			continue
		}
		dir := fspath.CanonicalPath(filepath.Dir(g.ApprovedPath))
		if dir == "" {
			drop(g, ProtectedDropNoParent)
			continue
		}
		resolved := filepath.Join(dir, filepath.Base(g.ApprovedPath))
		if resolved != g.ResolvedPath {
			drop(g, ProtectedDropRepointed)
			continue
		}
		if ControlPlaneWriteDenied(resolved) {
			drop(g, ProtectedDropControlPlane)
			continue
		}
		info, err := os.Lstat(resolved)
		switch {
		case err != nil && !os.IsNotExist(err):
			drop(g, ProtectedDropNoParent)
			continue
		case err == nil && info.Mode()&os.ModeSymlink != 0:
			drop(g, ProtectedDropIsSymlink)
			continue
		case err == nil && info.IsDir():
			// File grants cannot become directory grants.
			if !g.Subtree {
				drop(g, ProtectedDropRepointed)
				continue
			}
		case err == nil && !info.Mode().IsRegular():
			drop(g, ProtectedDropNotRegular)
			continue
		case g.Subtree:
			// Missing directories invalidate subtree grants.
			drop(g, ProtectedDropRepointed)
			continue
		}
		applied = append(applied, ProtectedPathGrant{
			ApprovedPath: g.ApprovedPath, ResolvedPath: resolved, Subtree: g.Subtree,
		})
	}
	if len(applied) > MaxProtectedPathGrants {
		return nil, nil, fmt.Errorf(
			"protected write grants exceed cap: %d > %d", len(applied), MaxProtectedPathGrants,
		)
	}
	return applied, dropped, nil
}

func protectedPathSyntaxReason(p string) ProtectedPathDropReason {
	if p == "" || !filepath.IsAbs(p) {
		return ProtectedDropNotAbsolute
	}
	if strings.ContainsRune(p, 0) {
		return ProtectedDropNUL
	}
	if !utf8.ValidString(p) {
		return ProtectedDropInvalidUTF8
	}
	if len(p) > maxProtectedPathBytes {
		return ProtectedDropTooLong
	}
	return ""
}

func normalizeProtectedPathGrants(in []ProtectedPathGrant) []ProtectedPathGrant {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]ProtectedPathGrant, 0, len(in))
	for _, g := range in {
		g.ApprovedPath = filepath.Clean(strings.TrimSpace(g.ApprovedPath))
		g.ResolvedPath = filepath.Clean(strings.TrimSpace(g.ResolvedPath))
		if g.ApprovedPath == "" || g.ApprovedPath == "." {
			continue
		}
		if _, duplicate := seen[g.ApprovedPath]; duplicate {
			continue
		}
		seen[g.ApprovedPath] = struct{}{}
		out = append(out, g)
	}
	return out
}
