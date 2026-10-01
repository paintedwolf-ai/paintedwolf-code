package sandbox

import (
	"context"
	"path/filepath"
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
