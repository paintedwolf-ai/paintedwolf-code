package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ReadFilter applies read-profile rules to inventory paths.
type ReadFilter func(relSlash string, isDir bool) bool

// CompiledReadScope is one immutable authorization projection. Key identifies
// its effective policy for caches that must not mix differently scoped views.
type CompiledReadScope struct {
	Filter ReadFilter
	Key    string
}

// CompileReadFilter builds one root-scoped inventory predicate.
func (b *Boundary) CompileReadFilter(ctx context.Context, projectDir, profileID string) (ReadFilter, error) {
	scope, err := b.CompileReadScope(ctx, projectDir, profileID)
	return scope.Filter, err
}

// CompileReadScope builds one root-scoped inventory predicate and stable cache
// identity from the pinned effective profile and boundary configuration.
func (b *Boundary) CompileReadScope(ctx context.Context, projectDir, profileID string) (CompiledReadScope, error) {
	if b == nil {
		return CompiledReadScope{Key: "unrestricted"}, nil
	}
	if strings.TrimSpace(projectDir) == "" && b.cfg.ProjectRootRequired {
		return CompiledReadScope{}, fmt.Errorf("project root is required")
	}
	root, err := b.resolveAbs(projectDir, ".", b.cfg.RejectSymlinkEscape)
	if err != nil {
		return CompiledReadScope{}, err
	}
	prof, ok := b.lookupProfile(ctx, profileID)
	if !ok {
		return CompiledReadScope{}, fmt.Errorf("unknown tool profile %q", profileID)
	}
	globs := SubstituteScopeGlobs(prof.ReadGlobs, ScopeTokensFromContext(ctx))
	filter := func(relSlash string, isDir bool) bool {
		if strings.ContainsRune(relSlash, 0) {
			return false
		}
		if !inventoryPathWithinRoot(root, relSlash) {
			return false
		}
		if len(globs) == 0 || matchAnyGlob(globs, relSlash) {
			return true
		}
		return isDir && directoryMayContainReadMatch(globs, relSlash)
	}
	return CompiledReadScope{Filter: filter, Key: readScopeKey(profileID, globs, b.cfg)}, nil
}

func readScopeKey(profileID string, globs []string, cfg Config) string {
	parts := append([]string(nil), globs...)
	sort.Strings(parts)
	raw := strings.Join([]string{
		strings.TrimSpace(profileID),
		strings.Join(parts, "\x00"),
		fmt.Sprintf("%t:%t", cfg.ProjectRootRequired, cfg.RejectSymlinkEscape),
	}, "\x01")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func inventoryPathWithinRoot(root, relSlash string) bool {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(relSlash)))
	if filepath.IsAbs(clean) {
		return false
	}
	clean = strings.TrimPrefix(clean, "."+string(filepath.Separator))
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func directoryMayContainReadMatch(patterns []string, relSlash string) bool {
	dir := strings.Trim(strings.TrimSpace(strings.ReplaceAll(relSlash, "\\", "/")), "/")
	if dir == "." {
		dir = ""
	}
	for _, pattern := range patterns {
		pattern = strings.Trim(strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/")), "/")
		wildcard := strings.IndexAny(pattern, "*?[")
		if wildcard < 0 {
			if dir == "" || strings.HasPrefix(pattern, dir+"/") {
				return true
			}
			continue
		}
		prefix := pattern[:wildcard]
		if prefix == "" || dir == "" || strings.HasPrefix(prefix, dir+"/") || strings.HasPrefix(dir+"/", prefix) {
			return true
		}
	}
	return false
}
