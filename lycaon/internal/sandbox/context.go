package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
)

type scopeTokensKey struct{}

type sessionIDKey struct{}

type toolProfileSnapshotKey struct{}

type toolProfileSnapshot struct {
	boundary *Boundary
	profile  ToolProfile
}

func withToolProfileSnapshot(ctx context.Context, snapshot toolProfileSnapshot) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolProfileSnapshotKey{}, snapshot)
}

func toolProfileSnapshotFromContext(ctx context.Context) (toolProfileSnapshot, bool) {
	if ctx == nil {
		return toolProfileSnapshot{}, false
	}
	snapshot, ok := ctx.Value(toolProfileSnapshotKey{}).(toolProfileSnapshot)
	return snapshot, ok
}

// WithScopeTokens attaches scope template values for glob substitution at check time.
func WithScopeTokens(ctx context.Context, tokens ScopeTokens) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, scopeTokensKey{}, tokens)
}

// ScopeTokensFromContext returns scope template values when present.
func ScopeTokensFromContext(ctx context.Context) ScopeTokens {
	if ctx == nil {
		return ScopeTokens{}
	}
	if v, ok := ctx.Value(scopeTokensKey{}).(ScopeTokens); ok {
		return v
	}
	return ScopeTokens{}
}

type turnWritePinKey struct{}

// TurnWritePin restricts every write of one coordinator turn to a single root.
// Globs are root-relative slash patterns; an empty RootPath or empty Globs
// denies all writes (fail closed). Set by editor selection turns.
type TurnWritePin struct {
	RootPath string
	Globs    []string
}

// Allows reports whether a write to relPath under projectDir is inside the pin.
func (p TurnWritePin) Allows(projectDir, relPath string) bool {
	if p.RootPath == "" || len(p.Globs) == 0 {
		return false
	}
	if filepath.Clean(projectDir) != filepath.Clean(p.RootPath) {
		return false
	}
	return matchAnyGlob(p.Globs, relPath)
}

// WithTurnWritePin attaches a per-turn write pin for AssertWriteScope.
func WithTurnWritePin(ctx context.Context, pin TurnWritePin) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, turnWritePinKey{}, pin)
}

// TurnWritePinFromContext returns the per-turn write pin when present.
func TurnWritePinFromContext(ctx context.Context) (TurnWritePin, bool) {
	if ctx == nil {
		return TurnWritePin{}, false
	}
	v, ok := ctx.Value(turnWritePinKey{}).(TurnWritePin)
	return v, ok
}

// WithSessionID attaches the host session id for scoped write overrides (merge reconcile).
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionIDKey{}, sessionID)
}

// SessionIDFromContext returns the host session id when present.
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(sessionIDKey{}).(string); ok {
		return v
	}
	return ""
}

type sessionDirsKey struct{}

type sessionDirs struct {
	scratchDir string
	outputDir  string
}

// WithSessionDirs attaches the active session's scratch and output directories
// for control-plane read-floor evaluation.
func WithSessionDirs(ctx context.Context, scratchDir, outputDir string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sessionDirsKey{}, sessionDirs{
		scratchDir: strings.TrimSpace(scratchDir),
		outputDir:  strings.TrimSpace(outputDir),
	})
}

// SessionDirsFromContext returns the active session's scratch and output directories when present.
func SessionDirsFromContext(ctx context.Context) (scratchDir, outputDir string) {
	if ctx == nil {
		return "", ""
	}
	if v, ok := ctx.Value(sessionDirsKey{}).(sessionDirs); ok {
		return v.scratchDir, v.outputDir
	}
	return "", ""
}

type approvedReadPathsKey struct{}

func frozenApprovedReadPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return ""
	}
	clean := filepath.Clean(filepath.FromSlash(trimmed))
	if clean == "" || clean == "." || !filepath.IsAbs(clean) {
		return ""
	}
	return clean
}

func canonicalApprovedReadPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return ""
	}
	canon := fspath.CanonicalPath(trimmed)
	if canon == "" {
		return ""
	}
	clean := filepath.Clean(filepath.FromSlash(canon))
	if clean == "" || clean == "." || !filepath.IsAbs(clean) {
		return ""
	}
	return clean
}

// WithApprovedReadPaths attaches exact single-file read approvals for
// AssertReadScope on protected key-material targets.
func WithApprovedReadPaths(ctx context.Context, paths []string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(paths) == 0 {
		return ctx
	}
	merged := make(map[string]struct{}, len(paths))
	if existing, ok := ctx.Value(approvedReadPathsKey{}).(map[string]struct{}); ok {
		for k := range existing {
			merged[k] = struct{}{}
		}
	}
	for _, p := range paths {
		// Preserve the frozen approval subject without re-evaluating filesystem symlinks at use time.
		if norm := frozenApprovedReadPath(p); norm != "" {
			merged[norm] = struct{}{}
		}
	}
	if len(merged) == 0 {
		return ctx
	}
	return context.WithValue(ctx, approvedReadPathsKey{}, merged)
}

// IsApprovedReadPathFromContext reports whether absPath is an exact non-directory
// file read approved on ctx.
func IsApprovedReadPathFromContext(ctx context.Context, absPath string) bool {
	if ctx == nil {
		return false
	}
	set, ok := ctx.Value(approvedReadPathsKey{}).(map[string]struct{})
	if !ok || len(set) == 0 {
		return false
	}
	norm := canonicalApprovedReadPath(absPath)
	if norm == "" {
		return false
	}
	if info, err := os.Lstat(norm); err == nil && (info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return false
	}
	_, exists := set[norm]
	return exists
}
