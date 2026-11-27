package turnnudges

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/pkg/api"
)

type Surface interface{ PromptTurnSurfaceID(string) string }

type Spend interface {
	ClaimWarning(context.Context, *api.Session, float64) bool
}

// Service renders the loop's host nudges against the Reader of the request
// that precedes them.
type Service struct {
	ledger  *turnload.Ledger
	Spend   Spend
	surface Surface
	prompts prompts.PromptTemplateEngine
}

func New(ledger *turnload.Ledger, spend Spend) *Service {
	return &Service{ledger: ledger, Spend: spend}
}
func (m *Service) SetLedger(ledger *turnload.Ledger)               { m.ledger = ledger }
func (m *Service) SetSurface(surface Surface)                      { m.surface = surface }
func (m *Service) SetPrompts(prompts prompts.PromptTemplateEngine) { m.prompts = prompts }

func (m *Service) renderKick(ctx context.Context, reader prompts.Reader, id string, data map[string]any) (string, error) {
	if m == nil || m.prompts == nil {
		return "", nil
	}
	return m.prompts.RenderKick(ctx, reader, strings.TrimSpace(id), data)
}

// commandMutationCapable reports whether reader's request can emit mutating
// command redirects outside Ask mode.
func commandMutationCapable(sess *api.Session, reader prompts.Reader) bool {
	if sess == nil || sess.Mode == api.SessionModeAsk {
		return false
	}
	return reader.Valid() && reader.Held()[toolcontract.ReachCommandMutation]
}
