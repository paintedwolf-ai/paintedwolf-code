package turnnudges

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

type Surface interface{ PromptTurnSurfaceID(string) string }
type Spend interface {
	ClaimWarning(context.Context, *api.Session, float64) bool
}
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
func (m *Service) renderKick(ctx context.Context, id string, data map[string]any) (string, error) {
	if m == nil || m.prompts == nil {
		return "", nil
	}
	return m.prompts.RenderKick(ctx, strings.TrimSpace(id), data)
}
