package session

import (
	"context"
	"github.com/lycaon/lycaon/internal/promptresult"

	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

// PromptRunner executes worker turns.
type PromptRunner interface {
	Prompt(ctx context.Context, sessionID, text string) (*promptresult.Result, error)
	PromptWorker(ctx context.Context, sessionID, jobID, text string) (*promptresult.Result, error)
	// PromptHostTurn records host-initiated work before execution.
	PromptHostTurn(ctx context.Context, sessionID string, origin store.PromptSubmissionOrigin, text string) (*promptresult.Result, error)
	SpawnChild(ctx context.Context, parentID string, req api.SpawnChildRequest) (*api.Session, error)
}
