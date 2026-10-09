package workflow

import (
	"context"
	workflowblueprints "github.com/lycaon/lycaon/internal/workflow/blueprints"
	workflowgates "github.com/lycaon/lycaon/internal/workflow/gates"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	workflowlifecycle "github.com/lycaon/lycaon/internal/workflow/lifecycle"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	workflowpublication "github.com/lycaon/lycaon/internal/workflow/publication"
	workflowreview "github.com/lycaon/lycaon/internal/workflow/review"
	runstate "github.com/lycaon/lycaon/internal/workflow/runstate"
	workflowruntime "github.com/lycaon/lycaon/internal/workflow/runtime"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/visual"
	workflowcatalog "github.com/lycaon/lycaon/internal/workflow/catalog"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// RunManager composes workflow services against the persistence domains.
type RunManager struct {
	Policy       *workflowruntime.SessionPolicy
	Snapshots    *workflowruntime.Snapshots
	Presentation *workflowpresentation.Runs
	Fanout       *Fanout
	Ambient      *Ambient
	Coverage     *workflowreview.Coverage
	Verdicts     *workflowreview.Verdicts
	Repairs      *ReviewRepairs
	Children     *Children
	Reports      *Reports
	Blueprints   *workflowblueprints.Service
	Approvals    *Approvals
	Requests     *workflowinputs.Requests
	Feedback     *workflowinputs.Feedback
	Asks         *workflowinputs.Asks
	Slash        *workflowinputs.SlashCommands
	Phases       *workflowphases.Service
	Publication  *workflowpublication.Runs
	Transcript   *workflowpublication.Messages
	Batch        *runstate.Batches
	Starts       *workflowlifecycle.Admission
	Controls     *workflowlifecycle.Commands
	Recovery     *workflowlifecycle.Recovery
	Store        *runstate.Repository
	Vars         *runstate.Variables
	Journal      *runstate.Journal
	Sessions     session.Store
	Resolver     workflowcatalog.Resolver
	Obligations  *Obligations
}

// NewManager creates a workflow run manager.
func NewManager(store *runstate.Repository, sessions session.Store, manifests *workflowdef.Registry, pub *events.Publisher) *RunManager {
	if manifests == nil {
		manifests = workflowdef.NewRegistry(nil)
	}
	vars := runstate.NewVariables(store.Runs, store.State, sessions)
	gates := workflowgates.FailClosedGateEvaluator{}
	manager := &RunManager{
		Store:    store,
		Vars:     vars,
		Sessions: sessions,
		Resolver: workflowcatalog.Resolver{Overlay: manifests, Sessions: sessions, Runs: store.Runs},
	}
	manager.Publication = &workflowpublication.Runs{Events: pub, Sessions: sessions, Transactions: store.Transactions}
	manager.Transcript = &workflowpublication.Messages{Sessions: sessions, Events: pub, Runs: store.Runs}
	manager.Batch = &runstate.Batches{Runs: store.Runs, Vars: vars}

	journal := &runstate.Journal{Commands: store.Commands, Vars: store.Runs, Directories: &manager.Resolver}
	manager.Journal = journal
	cleanup := &workflowlifecycle.Cleanup{Intents: store.Teardowns, Runs: store.Runs, Resolver: &manager.Resolver}
	manager.Controls = &workflowlifecycle.Commands{Runs: store.Runs, Trees: store.Commands, Vars: vars, Journal: journal, Resolver: &manager.Resolver, Cleanup: cleanup}
	manager.Starts = &workflowlifecycle.Admission{Runs: store.Runs, Starts: store.Starts, Sessions: sessions, Resolver: &manager.Resolver, Events: pub, Controls: manager.Controls, Cleanup: cleanup}
	manager.Controls.Admission = manager.Starts
	manager.Controls.Publication = manager.Publication
	manager.Starts.Publication = manager.Publication
	manager.Recovery = &workflowlifecycle.Recovery{Runs: store.Runs, Sessions: sessions, Resolver: &manager.Resolver, Journal: journal, Cleanup: cleanup}

	manager.Obligations = &Obligations{Runs: store.Runs, Vars: vars, Resolver: &manager.Resolver}
	manager.Coverage = &workflowreview.Coverage{Runs: store.Runs, Resolver: &manager.Resolver}
	questions := &workflowreview.Questions{Runs: store.Runs, Resolver: &manager.Resolver, Coverage: manager.Coverage}
	manager.Verdicts = &workflowreview.Verdicts{Runs: store.Runs, Records: store.Verdicts, Vars: vars, Resolver: &manager.Resolver, Sessions: sessions, Coverage: manager.Coverage, Questions: questions}
	manager.Repairs = &ReviewRepairs{RunManager: manager}
	questions.Verdicts = manager.Verdicts
	manager.Coverage.Reviews = manager.Verdicts
	entries := &workflowphases.Entries{Obligations: manager.Obligations}
	manager.Phases = &workflowphases.Service{Runs: store.Runs, Vars: vars, Journal: journal, Resolver: &manager.Resolver, Sessions: sessions, Gates: gates, Registry: nil, Publication: manager.Publication, Entries: entries}
	manager.Obligations.Phases = manager.Phases
	manager.Verdicts.Phases = manager.Phases
	manager.Starts.Phases = manager.Phases
	manager.Controls.Phases = manager.Phases

	scaffold := &workflowinputs.Scaffold{}
	manager.Blueprints = &workflowblueprints.Service{Runs: store.Runs, Bindings: store.Blueprints, Vars: vars, Sessions: sessions, Scaffold: scaffold, Starts: manager.Starts, Controls: manager.Controls, Phases: manager.Phases, Publication: manager.Publication, Transcript: manager.Transcript}
	manager.Approvals = &Approvals{Runs: store.Runs, State: store.State, Records: store.Blueprints, Vars: vars, Resolver: &manager.Resolver, Sessions: sessions, Phases: manager.Phases}
	cards := &workflowinputs.Cards{Sessions: sessions, Transcript: manager.Transcript}
	manager.Requests = &workflowinputs.Requests{Runs: store.Runs, Sessions: sessions, Resolver: &manager.Resolver, Vars: vars, Phases: manager.Phases, Publication: manager.Publication, Cards: cards, Approvals: manager.Approvals}
	manager.Feedback = &workflowinputs.Feedback{Runs: store.Runs, Resolver: &manager.Resolver, Vars: vars, Phases: manager.Phases, Cards: cards, Requests: manager.Requests, Controls: manager.Controls, Publication: manager.Publication}
	manager.Requests.Feedback = manager.Feedback
	manager.Asks = &workflowinputs.Asks{Runs: store.Runs, Sessions: sessions, Vars: vars, Publication: manager.Publication, Cards: cards, Approvals: manager.Approvals, Feedback: manager.Feedback, Phases: manager.Phases}
	manager.Slash = &workflowinputs.SlashCommands{Runs: store.Runs, Resolver: &manager.Resolver, Starts: manager.Starts, Controls: manager.Controls, Transcript: manager.Transcript}
	manager.Starts.Plans = manager.Blueprints
	manager.Starts.Approvals = manager.Approvals
	manager.Starts.Requests = manager.Requests
	manager.Starts.Feedback = manager.Feedback
	manager.Controls.Plans = manager.Blueprints
	manager.Controls.Scaffold = scaffold
	manager.Controls.Asks = manager.Asks
	manager.Recovery.Requests = manager.Requests
	manager.Recovery.Approvals = manager.Approvals
	manager.Children = &Children{Runs: store.Runs, Starts: store.Starts, Sessions: sessions, Resolver: &manager.Resolver, Vars: vars, Journal: journal, Blueprints: manager.Blueprints, Approvals: manager.Approvals, Phases: manager.Phases, Entries: entries, Publication: manager.Publication}
	manager.Reports = &Reports{Runs: store.Runs, Sessions: sessions, Resolver: &manager.Resolver, Vars: vars, Controls: manager.Controls, Phases: manager.Phases}
	manager.Ambient = &Ambient{Runs: store.Runs, Sessions: sessions, Resolver: &manager.Resolver, Starts: manager.Starts}
	explanations := &Explanations{Sessions: sessions, Transcript: manager.Transcript, Obligations: manager.Obligations}
	reviewEvidence := &workflowreview.ReviewEvidence{Vars: vars, Sessions: sessions}
	entries.Explanations = explanations
	entries.Reviews = reviewEvidence
	manager.Verdicts.Evidence = reviewEvidence
	manager.Requests.Ambient = manager.Ambient
	manager.Controls.Ambient = manager.Ambient
	manager.Fanout = &Fanout{Runs: store.Runs, Vars: vars, Resolver: &manager.Resolver, Journal: journal, Sessions: sessions, Phases: manager.Phases, Questions: questions}
	manager.Phases.Settlement = manager.Children
	manager.Controls.Settlement = manager.Children
	manager.Recovery.Settlement = manager.Children
	manager.Recovery.Reports = manager.Reports
	manager.Phases.Plans = manager.Blueprints
	manager.Phases.Approvals = manager.Approvals
	manager.Phases.Feedback = manager.Feedback
	entries.Blueprints = manager.Blueprints

	manager.Policy = &workflowruntime.SessionPolicy{Runs: store.Runs, Resolver: &manager.Resolver, Sessions: sessions, Registry: nil, Gates: gates, Approvals: manager.Approvals}
	manager.Snapshots = &workflowruntime.Snapshots{Sessions: sessions, Registry: nil, Approvals: manager.Approvals, Coverage: manager.Coverage, Verdicts: manager.Verdicts}
	manager.Presentation = &workflowpresentation.Runs{Reader: store.Runs, Resolver: &manager.Resolver, Asks: manager.Asks, Scaffold: scaffold, Obligations: manager.Obligations, Choices: manager.Phases}
	manager.Fanout.Policy = manager.Policy
	manager.Publication.Projector = manager.Presentation
	manager.Reports.Coverage = manager.Coverage
	manager.Reports.Verdicts = manager.Verdicts
	manager.Obligations.Gates = gates

	if mutations, ok := sessions.(runstate.SessionMutations); ok {
		store.Transactions.SetSessionMutations(mutations)
	}

	manager.Policy.Blueprints = manager.Blueprints
	return manager
}

// SetVisualArtifacts sets the session-tree artifact store.
func (m *RunManager) SetVisualArtifacts(store visual.Store, rootSessionID func(ctx context.Context, sessionID string) string) {
	if m == nil {
		return
	}
	m.Asks.Artifacts = store
	m.Asks.RootSessionID = rootSessionID
}

func (m *RunManager) SetEvidenceProjectDir(resolve func(context.Context, string) (string, error)) {
	m.Verdicts.EvidenceProjectDir = resolve
	m.Resolver.ProjectDirFallback = resolve
}

// SetConditionRegistry sets the manifest gate evaluator.
func (m *RunManager) SetConditionRegistry(reg *conditions.ConditionRegistry) {
	if m == nil {
		return
	}
	m.Policy.Registry = reg
	m.Snapshots.Registry = reg
	m.Phases.Registry = reg
	m.Approvals.Registry = reg
	m.Children.Registry = reg
	if reg != nil {
		gates := RegistryGateEvaluator{Registry: reg, Sessions: m.Policy.Sessions}
		m.Phases.Gates = gates
		m.Policy.Gates = gates
		m.Obligations.Gates = m.Phases.Gates
	}
}

// ForgetSession releases guards after session deletion stops further runs and questions.
func (m *RunManager) ForgetSession(sessionID string) {
	if m == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	if m.Starts != nil {
		m.Starts.ForgetSession(sessionID)
	}
	m.Asks.ForgetSession(sessionID)
}
