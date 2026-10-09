package projectadmin

import (
	"context"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// detectVerifyAsync deduplicates detection by primary root and outlives the request.
// Completed proposals are cached and published as settings changes.
func (s *Verification) detectVerifyAsync(parent context.Context, projectID string) {
	if !llm.ProviderUtilityCallsEnabled() {
		return
	}

	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return
	}
	p, err := s.Registry.Get(parent, projectID)
	if err != nil || p == nil {
		return
	}
	primary := project.PrimaryRootPath(p)
	if strings.TrimSpace(primary) == "" {
		return
	}
	if _, loaded := s.verifyDetectInFlight.LoadOrStore(primary, true); loaded {
		return
	}
	roots := project.RootPaths(p)
	s.background.Go(parent, func(ctx context.Context) {
		defer s.verifyDetectInFlight.Delete(primary)
		summarizer := s.LLMService.BindSummarizer(&llm.RegistrySummarizer{
			Scope:      llm.SettingsScopeProject,
			ProjectID:  projectID,
			ProjectDir: primary,
			Cost:       s.Sessions.Coordinator.Model.Cost,
			// An unavailable model leaves the proposal unset.
			Fallback: compaction.UnavailableSummarizer{},
			Purpose:  "verify_detect",
			Class:    llm.UtilityClassOverlay,
		})
		cand, outcome := settings.DetectVerifyCommandLLM(ctx, summarizer, roots)
		if !outcome.Ran() {
			// Unavailable detection stays uncached so a later request can retry.
			slog.DebugContext(ctx, "verify detect unavailable; leaving the project uncached",
				"component", "verify_detect", "project_dir", primary)
			return
		}
		// Cache completed empty results to avoid repeating detection on every settings read.
		s.Settings.Verify.SetProposal(primary, cand)
		projectview.PublishSettings(s.Events, s.Registry, ctx, wire.SettingsAreaVerify, string(llm.SettingsScopeProject), projectID, "updated")
	})
}
