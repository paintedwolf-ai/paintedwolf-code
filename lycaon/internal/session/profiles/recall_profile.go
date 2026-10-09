package profiles

import (
	"context"

	"github.com/lycaon/lycaon/internal/recall"
	"github.com/lycaon/lycaon/pkg/api"
)

// RecallAvailable reports whether the session's prompt profile carries recall.
// The continuation record names the tool only when the model can call it.
func (m *Service) RecallAvailable(ctx context.Context, sess *api.Session) bool {
	profileID, err := m.PromptToolProfile(ctx, sess)
	if err != nil {
		return false
	}
	profile, ok := m.ToolProfile(ctx, sess, profileID)
	return ok && profile.ToolAllowed(recall.ToolName)
}
