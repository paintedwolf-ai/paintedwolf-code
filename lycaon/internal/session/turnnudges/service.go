package turnnudges

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

type Surface interface{ PromptTurnSurfaceID(string) string }
type Tasks interface {
	Get(string) (*api.WorkerTask, bool)
}
type Budget interface {
	BudgetForTask(context.Context, *api.WorkerTask) spawn.WorkerToolBudget
}
type Spend interface {
	ClaimWarning(context.Context, *api.Session, float64) bool
}
type Service struct {
	ledger  *turnload.Ledger
	tasks   Tasks
	budget  Budget
	Spend   Spend
	surface Surface
	prompts prompts.PromptTemplateEngine
}

func New(ledger *turnload.Ledger, budget Budget, spend Spend) *Service {
	return &Service{ledger: ledger, budget: budget, Spend: spend}
}
func (m *Service) SetLedger(ledger *turnload.Ledger)               { m.ledger = ledger }
func (m *Service) SetTasks(tasks Tasks)                            { m.tasks = tasks }
func (m *Service) SetSurface(surface Surface)                      { m.surface = surface }
func (m *Service) SetPrompts(prompts prompts.PromptTemplateEngine) { m.prompts = prompts }
func (m *Service) renderKick(ctx context.Context, id string, data map[string]any) (string, error) {
	if m == nil || m.prompts == nil {
		return "", nil
	}
	return m.prompts.RenderKick(ctx, strings.TrimSpace(id), data)
}
