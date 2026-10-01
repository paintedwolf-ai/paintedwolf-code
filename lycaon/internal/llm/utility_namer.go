package llm

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/llm/failure"
)

// UtilityNamer names one project or session.
type UtilityNamer interface {
	Name(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// PlaneNamer names through the utility plane.
type PlaneNamer struct {
	Summarizer *RegistrySummarizer
}

// Name sends a gift-class utility request.
func (n PlaneNamer) Name(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if n.Summarizer == nil {
		return "", ErrLiteUnavailable
	}
	n.Summarizer.Class = UtilityClassGift
	// Completion tokens include reasoning even when visible output is a title.
	name, err := n.Summarizer.SummarizeRequired(ctx, systemPrompt, userPrompt, 1024)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	empty, _ := failure.AsProviderEmptyCompletion(err)
	if errors.Is(err, failure.ErrProviderOutputTruncated) || empty.OutputLimitReached() {
		return n.Summarizer.SummarizeRequired(ctx, systemPrompt, userPrompt, 4096)
	}
	return name, err
}

// Namer returns a gift-class namer on this service's plane.
func (s *Service) Namer(purpose, projectDir, sessionID, projectID string) UtilityNamer {
	if s == nil {
		return nil
	}
	sum := s.BindSummarizer(&RegistrySummarizer{
		Scope:      SettingsScopeProject,
		ProjectID:  projectID,
		ProjectDir: projectDir,
		Purpose:    purpose,
		Class:      UtilityClassGift,
	})
	if sessionID != "" {
		sum.Cost = nil // session cost is attached by the caller when needed
	}
	return PlaneNamer{Summarizer: sum}
}
