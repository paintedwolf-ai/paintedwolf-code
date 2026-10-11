package workeroutcomes

import (
	"context"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/pkg/api"
)

type SummaryStore interface {
	Get(context.Context, string) (*api.Session, error)
	GetMessages(context.Context, string) ([]api.Message, error)
	GetWorkerJobMessages(context.Context, string, string) ([]api.Message, error)
	LoadLedger(context.Context, string) (evidence.Ledger, error)
	SeedUntrustedContent(context.Context, string) error
	UpsertEvidenceRecord(context.Context, string, evidence.Record) error
	SessionSecretExposure(context.Context, string) (bool, error)
	SeedSecretExposure(context.Context, string) error
}
type SummaryLimits interface {
	Compaction(context.Context, *api.Session) compaction.CompactionConfig
}
type SummaryBudget interface {
	BudgetForTask(context.Context, *api.WorkerTask) spawn.WorkerToolBudget
}
type SummaryDecisions interface {
	GetByJob(context.Context, string) (api.WorkerDecisionRequest, bool, error)
}
type SummaryVerification interface {
	Revision(context.Context, string) (string, string)
	SourceVerifyCommand(context.Context, string) string
	WorkerSourceRuns(context.Context, string) ([]workercompletion.WorkerInvocationReceipt, error)
}
type SummaryResources interface {
	DisposeRuntime(context.Context, string) error
}
type SummaryDelivery interface {
	Evaluate(context.Context, workercompletion.WorkerCompletionEnvelope) (DeliveryResult, error)
}

type Summaries struct {
	store          SummaryStore
	tasks          TaskReader
	limits         SummaryLimits
	budget         SummaryBudget
	decisions      SummaryDecisions
	verification   SummaryVerification
	resources      SummaryResources
	delivery       SummaryDelivery
	cards          ParentCards
	digests        *Digests
	grounding      GroundingReset
	workspaceCheck workercompletion.WorkspaceChangeChecker
	workflowHints  *guidance.HintConfig
	oarPipeline    *oar.GuardPipeline
	inspectChanges func(context.Context, *api.WorkerTask, []projectroot.RootRef) ([]string, error)
	overlayStatus  func(context.Context, api.WorkerSummaryStatus, *api.WorkerTask) api.WorkerSummaryStatus
	overlayPending func(context.Context, *api.WorkerTask) bool
}

type SummaryPorts struct {
	Sessions       SummaryStore
	Tasks          TaskReader
	Limits         SummaryLimits
	Budget         SummaryBudget
	Decisions      SummaryDecisions
	Verification   SummaryVerification
	Resources      SummaryResources
	Delivery       SummaryDelivery
	Cards          ParentCards
	Digests        *Digests
	InspectChanges func(context.Context, *api.WorkerTask, []projectroot.RootRef) ([]string, error)
	OverlayStatus  func(context.Context, api.WorkerSummaryStatus, *api.WorkerTask) api.WorkerSummaryStatus
	OverlayPending func(context.Context, *api.WorkerTask) bool
}

func NewSummaries(p SummaryPorts) *Summaries {
	return &Summaries{store: p.Sessions, tasks: p.Tasks, limits: p.Limits, budget: p.Budget, decisions: p.Decisions, verification: p.Verification, resources: p.Resources, delivery: p.Delivery, cards: p.Cards, digests: p.Digests, inspectChanges: p.InspectChanges, overlayStatus: p.OverlayStatus, overlayPending: p.OverlayPending}
}
func (m *Summaries) SetTasks(p TaskReader)           { m.tasks = p }
func (m *Summaries) SetDecisions(p SummaryDecisions) { m.decisions = p }
func (m *Summaries) SetGrounding(p GroundingReset)   { m.grounding = p }
func (m *Summaries) SetEvaluation(check workercompletion.WorkspaceChangeChecker, hints *guidance.HintConfig, pipeline *oar.GuardPipeline) {
	m.workspaceCheck = check
	m.workflowHints = hints
	m.oarPipeline = pipeline
}
