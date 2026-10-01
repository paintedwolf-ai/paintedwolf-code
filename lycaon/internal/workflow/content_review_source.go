package workflow

import (
	"context"

	"github.com/lycaon/lycaon/internal/toolhost"
)

// PhaseContentReviewSource reads active run scaffold vars for content_review policy.
type PhaseContentReviewSource struct {
	Runs RunScaffoldReader
}

// RunScaffoldReader loads scaffold vars for the active workflow run of a session.
type RunScaffoldReader interface {
	ScaffoldVarsForSession(ctx context.Context, sessionID string) (map[string]any, error)
}

// PhaseContentReview implements toolhost.SessionContentReviewSource.
func (s *PhaseContentReviewSource) PhaseContentReview(ctx context.Context, sessionID string) (*toolhost.PhaseContentReview, error) {
	if s == nil || s.Runs == nil || sessionID == "" {
		return nil, nil
	}
	vars, err := s.Runs.ScaffoldVarsForSession(ctx, sessionID)
	if err != nil || vars == nil {
		return nil, err
	}
	raw, ok := vars["content_review"].(map[string]any)
	if !ok || raw == nil {
		return nil, nil
	}
	out := &toolhost.PhaseContentReview{}
	if tools, ok := raw["tools"].([]any); ok {
		for _, t := range tools {
			if s, ok := t.(string); ok && s != "" {
				out.Tools = append(out.Tools, s)
			}
		}
	}
	if paths, ok := raw["paths"].([]any); ok {
		for _, p := range paths {
			if s, ok := p.(string); ok && s != "" {
				out.Paths = append(out.Paths, s)
			}
		}
	}
	if len(out.Tools) == 0 && len(out.Paths) == 0 {
		return &toolhost.PhaseContentReview{}, nil
	}
	return out, nil
}
