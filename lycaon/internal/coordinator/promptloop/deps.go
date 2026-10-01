package promptloop

import "context"

// DoomLoopGuard tracks repetition and structured survey outcomes.
type DoomLoopGuard interface {
	Check(ctx context.Context, sessionID, responseID, tool string, args map[string]any) (allowed bool, count int, repeatedCode string, err error)
	ResolveRejection(ctx context.Context, sessionID, tool string, args map[string]any, code string) error
	RecordAttempt(ctx context.Context, sessionID, responseID, tool string, args map[string]any, rejectCode string, mutated bool) error
	RecordSearchOutcome(ctx context.Context, sessionID, tool string, args map[string]any, foundMaterial bool) (int, error)
}
