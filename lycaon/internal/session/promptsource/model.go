package promptsource

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/promptloop"
	"github.com/lycaon/lycaon/internal/cost"
	"github.com/lycaon/lycaon/internal/llm"
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
	if m.History != nil {
		deps.FitRequest = func(ctx context.Context, sess *api.Session, rows []api.Message, req modelcall.CompletionRequest) (modelcall.CompletionRequest, error) {
			var budgets history.RequestBudgets
			if m.LLMService != nil && m.LLMService.Registry != nil {
				budgets = m.LLMService.Registry
			}
			return m.History.FitRequest(ctx, sess, rows, req, budgets)
		}
	}
	deps.RecordCompactionTokenObservation = func(sessionID, providerID, model string, reportedPromptTokens, transcriptEstimate int) {
		if m != nil && m.History != nil {
			m.History.ObserveTokens(sessionID, providerID, model, reportedPromptTokens, transcriptEstimate)
		}
	}
	deps.LowerSessionLiveWindow = func(sessionID string, window int) {
		if m != nil && m.Limits != nil {
			m.Limits.LowerSessionLiveWindow(sessionID, window)
		}
	}
	if m.History != nil {
		deps.CompactAfterContextOverflow = func(ctx context.Context, sess *api.Session, rows []api.Message, reportedWindow int) ([]api.Message, error) {
			if sess == nil {
				return rows, nil
			}
			if m.Limits != nil && reportedWindow > 0 {
				m.Limits.LowerSessionLiveWindow(sess.ID, reportedWindow)
			}
			_, err := m.History.ForceCompact(ctx, sess.ID)
			reloaded, loadErr := m.History.Load(ctx, sess)
			if loadErr == nil && len(reloaded) > 0 {
				rows = reloaded
			}
			return rows, err
		}
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
