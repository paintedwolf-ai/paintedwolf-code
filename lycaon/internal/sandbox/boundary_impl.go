package sandbox

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// ErrToolNotAllowedByProfile classifies a profile allow-list rejection.
var ErrToolNotAllowedByProfile = errors.New("tool not allowed by profile")

// MergeReconcileAllowlister permits coordinator writes on active overlay-promote conflict paths.
// Implemented by session.Manager; wired from app (sandbox must not import session).
type MergeReconcileAllowlister interface {
	Allowed(sessionID, relPath string) bool
}

// ProfileSource resolves tool profiles for one session evaluation.
// Nil or a nil/empty return keeps the boot-time profile map.
type ProfileSource func(ctx context.Context, sessionID string) []ToolProfile

// Boundary implements SandboxBoundary using sandbox.yaml and tool profiles.
type Boundary struct {
	cfg            Config
	profiles       map[string]ToolProfile
	profileSource  ProfileSource
	pathScopes     PathScopeRegistry
	mergeReconcile MergeReconcileAllowlister
}

// NewBoundary constructs a sandbox boundary from config and profiles.
func NewBoundary(cfg Config, profiles []ToolProfile) *Boundary {
	m := make(map[string]ToolProfile, len(profiles))
	for _, p := range profiles {
		m[p.ID] = p
	}
	return &Boundary{cfg: cfg, profiles: m}
}

// SetMergeReconcileAllowlister sets session conflict-path write overrides.
func (b *Boundary) SetMergeReconcileAllowlister(l MergeReconcileAllowlister) {
	if b == nil {
		return
	}
	b.mergeReconcile = l
}

// SetPathScopes wires named path scopes for AssertNamedWriteScope.
func (b *Boundary) SetPathScopes(reg PathScopeRegistry) {
	if b == nil {
		return
	}
	b.pathScopes = reg
}

// SetProfileSource installs a per-session profile resolver. Nil clears it.
func (b *Boundary) SetProfileSource(src ProfileSource) {
	if b == nil {
		return
	}
	b.profileSource = src
}

func (b *Boundary) lookupProfile(ctx context.Context, profileID string) (ToolProfile, bool) {
	if b == nil {
		return ToolProfile{}, false
	}
	if snap, ok := toolProfileSnapshotFromContext(ctx); ok && snap.boundary == b && snap.profile.ID == profileID {
		return snap.profile, true
	}
	if b.profileSource != nil {
		if sid := SessionIDFromContext(ctx); sid != "" {
			for _, p := range b.profileSource(ctx, sid) {
				if p.ID == profileID {
					return p, true
				}
			}
		}
	}
	prof, ok := b.profiles[profileID]
	return prof, ok
}

// WithToolProfileSnapshot pins one effective profile for a tool invocation.
func (b *Boundary) WithToolProfileSnapshot(ctx context.Context, profileID string) (context.Context, error) {
	if b == nil {
		return ctx, nil
	}
	prof, ok := b.lookupProfile(ctx, profileID)
	if !ok {
		return ctx, fmt.Errorf("unknown tool profile %q", profileID)
	}
	prof.Tools = maps.Clone(prof.Tools)
	prof.StickyTools = maps.Clone(prof.StickyTools)
	prof.DeferredTools = maps.Clone(prof.DeferredTools)
	prof.DenyTools = append([]string(nil), prof.DenyTools...)
	prof.MCPDeny = append([]string(nil), prof.MCPDeny...)
	prof.ReadGlobs = append([]string(nil), prof.ReadGlobs...)
	prof.WriteGlobs = append([]string(nil), prof.WriteGlobs...)
	return withToolProfileSnapshot(ctx, toolProfileSnapshot{boundary: b, profile: prof}), nil
}

// AssertNamedWriteScope enforces a named path scope.
func (b *Boundary) AssertNamedWriteScope(ctx context.Context, projectDir, relPath, scopeName string) error {
	if err := b.AssertPathAllowed(ctx, projectDir, relPath, PathOpWrite); err != nil {
		return err
	}
	if b.pathScopes == nil {
		return fmt.Errorf("path scopes not configured")
	}
	scope, ok := b.pathScopes[scopeName]
	if !ok {
		return fmt.Errorf("unknown path scope %q", scopeName)
	}
	return CheckWriteInScope(scope, scopeName, relPath)
}

// AssertPathAllowed validates a relative path for read/write/edit within projectDir.
func (b *Boundary) AssertPathAllowed(_ context.Context, projectDir, relPath string, _ PathOp) error {
	if strings.TrimSpace(projectDir) == "" && b.cfg.ProjectRootRequired {
		return fmt.Errorf("project root is required")
	}
	if strings.ContainsRune(relPath, 0) {
		return fmt.Errorf("path contains NUL byte")
	}
	if _, err := b.resolveAbs(projectDir, relPath, true); err != nil {
		return err
	}
	return nil
}

// AssertReadScope enforces profile read scope when configured.
func (b *Boundary) AssertReadScope(ctx context.Context, projectDir, relPath, profileID string) error {
	if err := b.AssertPathAllowed(ctx, projectDir, relPath, PathOpRead); err != nil {
		return err
	}
	return b.AssertReadGlobs(ctx, relPath, profileID)
}

// AssertReadGlobs enforces the profile's read globs alone, for a path whose
// containment the caller enforces with a descriptor-relative open.
func (b *Boundary) AssertReadGlobs(ctx context.Context, relPath, profileID string) error {
	if strings.ContainsRune(relPath, 0) {
		return fmt.Errorf("path contains NUL byte")
	}
	prof, ok := b.lookupProfile(ctx, profileID)
	if !ok {
		return fmt.Errorf("unknown tool profile %q", profileID)
	}
	if len(prof.ReadGlobs) == 0 {
		return nil
	}
	globs := SubstituteScopeGlobs(prof.ReadGlobs, ScopeTokensFromContext(ctx))
	if !matchAnyGlob(globs, relPath) {
		return newOutsideReadScope(relPath, profileID)
	}
	return nil
}

// WriteGlobsForProfile returns resolved write globs for profileID (empty when
// unrestricted). It reads the session-effective profile so a rejection message
// cites the same globs the enforcement in AssertWriteScope applied — including
// an extension-pack or project overlay that narrowed the base profile.
func (b *Boundary) WriteGlobsForProfile(ctx context.Context, profileID string) []string {
	prof, ok := b.lookupProfile(ctx, profileID)
	if !ok || len(prof.WriteGlobs) == 0 {
		return nil
	}
	return append([]string(nil), prof.WriteGlobs...)
}

// AssertWriteScope enforces profile write scope when configured.
func (b *Boundary) AssertWriteScope(ctx context.Context, projectDir, relPath, profileID string) error {
	if err := b.AssertPathAllowed(ctx, projectDir, relPath, PathOpWrite); err != nil {
		return err
	}
	prof, ok := b.lookupProfile(ctx, profileID)
	if !ok {
		return fmt.Errorf("unknown tool profile %q", profileID)
	}
	if len(prof.WriteGlobs) > 0 {
		globs := SubstituteScopeGlobs(prof.WriteGlobs, ScopeTokensFromContext(ctx))
		if !matchAnyGlob(globs, relPath) {
			if b.mergeReconcile != nil && profileID == "coordinator" {
				if sid := SessionIDFromContext(ctx); sid != "" && b.mergeReconcile.Allowed(sid, relPath) {
					return nil
				}
			}
			return newOutsideWriteScope(relPath, profileID)
		}
	}
	if pin, ok := TurnWritePinFromContext(ctx); ok && !pin.Allows(projectDir, relPath) {
		return newOutsideWriteScope(relPath, profileID)
	}
	return nil
}

// ProfileAllowsTool reports whether profileID may invoke toolName (deny list wins).
func (b *Boundary) ProfileAllowsTool(profileID, toolName string) bool {
	if b == nil {
		return false
	}
	return b.AssertToolAllowed(context.Background(), profileID, toolName, ToolAccessProfile) == nil
}

// AssertToolAllowed checks tool profile allow/deny lists.
func (b *Boundary) AssertToolAllowed(ctx context.Context, profileID, toolName string, access ToolAccess) error {
	prof, ok := b.lookupProfile(ctx, profileID)
	if !ok {
		return fmt.Errorf("unknown tool profile %q", profileID)
	}
	if prof.ToolDenied(toolName) {
		return fmt.Errorf("tool %q denied by profile %q", toolName, profileID)
	}
	if access == ToolAccessAll {
		return nil
	}
	if prof.ToolAllowed(toolName) {
		return nil
	}
	return fmt.Errorf("%w: tool %q profile %q", ErrToolNotAllowedByProfile, toolName, profileID)
}

// ToolDeferred reports whether the profile marks toolName as deferred
// (exact name or trailing-* pattern). Open-world tools defer unless sticky.
func (b *Boundary) ToolDeferred(profileID, toolName string, access ToolAccess) bool {
	prof, ok := b.lookupProfile(context.Background(), profileID)
	if !ok {
		return false
	}
	if prof.ToolDeferred(toolName) {
		return true
	}
	if access == ToolAccessAll && !prof.ToolSticky(toolName) {
		return true
	}
	return false
}

// WaitConditions returns the profile's closed condition vocabulary.
func (b *Boundary) WaitConditions(profileID string) []string {
	prof, ok := b.lookupProfile(context.Background(), profileID)
	if !ok {
		return nil
	}
	return append([]string(nil), prof.WaitConditions...)
}

// ExecMode returns subprocess policy for a profile, read from the
// session-effective profile so an overlay that grants or removes `command`
// is honored — consistent with the tool-allow and write-scope checks.
func (b *Boundary) ExecMode(ctx context.Context, profileID string) ExecMode {
	prof, ok := b.lookupProfile(ctx, profileID)
	if !ok {
		return ExecModeNone
	}
	if prof.Tools["command"] {
		return ExecModeAllowlisted
	}
	return ExecModeNone
}

// ResolveAbs returns the absolute path for relPath under projectDir after sandbox checks.
func (b *Boundary) ResolveAbs(projectDir, relPath string) (string, error) {
	return b.resolveAbs(projectDir, relPath, b.cfg.RejectSymlinkEscape)
}

func (b *Boundary) resolveAbs(projectDir, relPath string, evalSymlinks bool) (string, error) {
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return "", fmt.Errorf("invalid project dir: %w", err)
	}
	if evalSymlinks {
		if canonical, err := filepath.EvalSymlinks(root); err == nil {
			root = canonical
		}
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("project dir not accessible: %w", err)
	}
	if !rootInfo.IsDir() {
		return "", fmt.Errorf("project dir is not a directory")
	}

	clean := filepath.Clean(relPath)
	if filepath.IsAbs(clean) {
		if evalSymlinks {
			if canDir, err := filepath.EvalSymlinks(filepath.Dir(clean)); err == nil {
				clean = filepath.Join(canDir, filepath.Base(clean))
			}
		}
		rel, err := filepath.Rel(root, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("path %q escapes project boundary", relPath)
		}
		full := clean
		if evalSymlinks {
			canonical, err := canonicalizeWithinRoot(root, full, relPath)
			if err != nil {
				return "", err
			}
			full = canonical
		}
		return full, nil
	}

	clean = strings.TrimPrefix(clean, "."+string(os.PathSeparator))

	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil {
		return "", fmt.Errorf("path resolution failed: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes project boundary", relPath)
	}

	if evalSymlinks {
		canonical, err := canonicalizeWithinRoot(root, full, relPath)
		if err != nil {
			return "", err
		}
		full = canonical
	}

	return full, nil
}

// canonicalizeWithinRoot resolves the deepest existing ancestor, appends the
// missing tail, and verifies the result remains under root. This catches symlink
// escapes when filepath.EvalSymlinks returns IsNotExist.
func canonicalizeWithinRoot(root, full, relPath string) (string, error) {
	existing := full
	tail := ""
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("symlink evaluation failed: %w", err)
		}
		parent := filepath.Dir(existing)
		tail = filepath.Join(filepath.Base(existing), tail)
		if parent == existing {
			return "", fmt.Errorf("path %q has no accessible parent", relPath)
		}
		existing = parent
	}
	evaluated, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", fmt.Errorf("symlink evaluation failed: %w", err)
	}
	resolved := evaluated
	if tail != "" {
		resolved = filepath.Join(evaluated, tail)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes project boundary via symlink", relPath)
	}
	return resolved, nil
}
