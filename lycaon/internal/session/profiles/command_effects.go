package profiles

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

// ForSession returns the effective tool profiles constrained by the session posture.
func (m *Service) ForSession(ctx context.Context, sess *api.Session) []sandbox.ToolProfile {
	var profiles []sandbox.ToolProfile
	if m != nil && m.catalog != nil {
		if view := m.catalog.ViewForSession(ctx, sess); view != nil {
			profiles = view.ToolProfiles
		}
	}
	if len(profiles) == 0 {
		profiles, _ = sandbox.LoadToolProfiles()
	}
	if sess == nil {
		return profiles
	}
	return m.ConstrainCommandProfiles(ctx, sess.ID, profiles)
}

// ConstrainCommandProfiles freezes the actual session posture restriction into
// the same effective profiles that the sandbox snapshots for native admission.
// Lookup failure closes only the new command surface; retained interfaces still
// follow their existing owner and workflow policies.
func (m *Service) ConstrainCommandProfiles(ctx context.Context, sessionID string, input []sandbox.ToolProfile) []sandbox.ToolProfile {
	var allowed toolcontract.CommandEffects
	if m != nil && m.store != nil {
		if sess, err := m.store.Get(ctx, sessionID); err == nil && sess != nil {
			if registry, err := m.EffectivePostures(ctx, sess); err == nil {
				if posture, err := registry.Get(sess.Posture); err == nil {
					allowed = posture.CommandEffects
				}
			}
		}
	}
	output := slices.Clone(input)
	for index := range output {
		output[index].CommandEffects &= allowed
	}
	return output
}
