package promptloop

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

// DoomLoopGuard tracks repetition and structured survey outcomes.
type DoomLoopGuard interface {
	Check(ctx context.Context, sessionID, responseID, tool string, args map[string]any) (allowed bool, count int, repeatedCode string, err error)
	ResolveRejection(ctx context.Context, sessionID, tool string, args map[string]any, code string) error
	RecordAttempt(ctx context.Context, sessionID, responseID, tool string, args map[string]any, rejectCode string, mutated bool) error
	RecordSearchOutcome(ctx context.Context, sessionID, tool string, args map[string]any, foundMaterial bool) (int, error)
}

func rootModelRequestSessionID(sess *api.Session, sessionID string) string {
	if sess != nil && strings.TrimSpace(sess.ParentSessionID) != "" {
		return strings.TrimSpace(sess.ParentSessionID)
	}
	return strings.TrimSpace(sessionID)
}

func (l modelTurn) sessionRoutingClient(sess *api.Session) modelcall.LLMClient {
	client := llm.WithClientDispatch(l.Deps.LLM)
	if l.Deps.LLMService != nil && l.Deps.LLMService.Registry != nil && l.Deps.LLMService.Router != nil {
		client = llm.NewRoutingClient(l.Deps.LLMService.Registry, l.Deps.LLMService.Router, l.Deps.LLM, l.Deps.LLMService.Utility, l.Deps.LLMService.Capacity, l.Deps.LLMService.Refusals, func(ctx context.Context) (*llm.ModelSelection, error) {
			router := l.Deps.LLMService.Router.WithOverlayRoots(l.overlayRootPaths(ctx, sess))
			return router.ResolveSession(ctx, sess)
		})
	}
	if l.Deps.LLMService != nil && l.Deps.LLMService.Preparation != nil {
		client = l.Deps.LLMService.Preparation.Wrap(client)
	}
	return client
}

// providerProfile uses the default driver profile for unknown provider IDs.
func (l modelTurn) providerProfile(providerID string) providerprofile.Profile {
	if l.Deps.LLMService != nil && l.Deps.LLMService.Registry != nil && strings.TrimSpace(providerID) != "" {
		if p, err := l.Deps.LLMService.Registry.Get(providerID); err == nil && p != nil {
			return p.Profile()
		}
	}
	return providerprofile.Default()
}

func sessionProjectKey(sess *api.Session) string {
	if sess == nil {
		return ""
	}
	return strings.TrimSpace(sess.ProjectID)
}
