package promptsource

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/session/history"
	sessionlimits "github.com/lycaon/lycaon/internal/session/limits"
	sessionscope "github.com/lycaon/lycaon/internal/session/scope"
	"github.com/lycaon/lycaon/pkg/api"
)

type Model struct {
	Workspace  *sessionscope.Service
	Cost       cost.CostTracker
	History    *history.Service
	LLM        modelcall.LLMClient
	LLMService *llm.Service
	Limits     *sessionlimits.Service
}

func (m *Model) Build() promptloop.ModelDeps {
	deps := promptloop.ModelDeps{
		LLM:        m.LLM,
		LLMService: m.LLMService,
		Cost:       m.Cost,
	}
	deps.CompactionConfig = m.Limits.Compaction
	deps.RecordCompactionTokenObservation = func(sessionID string, reportedPromptTokens, transcriptEstimate int) {
		if m != nil {
			m.History.ObserveTokens(sessionID, reportedPromptTokens, transcriptEstimate)
		}
	}
	deps.CompactionTokenCalibration = func(sessionID string) compaction.PromptTokenCalibration {
		if m == nil {
			return compaction.PromptTokenCalibration{}
		}
		return m.History.Calibration(sessionID)
	}

	return deps
}

func (m *Model) Vision(ctx context.Context, sess *api.Session) bool {
	if m == nil || m.LLMService == nil || m.LLMService.Registry == nil || m.LLMService.Router == nil || sess == nil {
		return false
	}
	router := m.LLMService.Router.WithOverlayRoots(m.Workspace.SettingsRoots(ctx, sess))
	sel, err := router.ResolveSession(ctx, sess)
	if err != nil || sel == nil {
		return false
	}
	return m.LLMService.Registry.ModelHasVision(ctx, sel.ProviderID, sel.Model)
}
