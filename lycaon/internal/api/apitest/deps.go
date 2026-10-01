// Package apitest completes API server dependencies for tests outside the api
// package.
package apitest

import (
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/testutil/apitestdeps"
)

// Dependencies supplies every dependency the route families require that deps
// leaves unset.
func Dependencies(t *testing.T, deps api.Dependencies) api.Dependencies {
	t.Helper()
	fill := apitestdeps.Deps{
		ApprovalGate:      deps.ApprovalGate,
		ApprovalDecisions: deps.Authority.ApprovalDecisions,
		Database:          deps.Database, Store: deps.Store, Projects: deps.Projects, Sessions: deps.Sessions,
		Settings: deps.Settings, Invocations: deps.Invocations, MutationGate: deps.MutationGate,
		ManagedSecrets: deps.ManagedSecrets, SecretIgnores: deps.SecretIgnores, SourceLedger: deps.SourceLedger,
		SourceMutations: deps.SourceMutations, FileOperations: deps.FileOperations,
		EditorDocuments: deps.EditorDocuments, FileBriefings: deps.FileBriefings, Workflows: deps.Workflows,
		WorkflowRuns: deps.WorkflowRuns, WorkflowComposer: deps.WorkflowComposer, WorkflowPersister: deps.WorkflowPersister,
		Blueprints: deps.Blueprints, ScanCoordinator: deps.ScanCoordinator, ScanCadence: deps.ScanCadence,
		PublishDetections: deps.PublishDetections, DataDir: deps.DataDir, ModuleRoot: deps.ModuleRoot,
		MCP: deps.MCP, ExtensionViews: deps.ExtensionViews,
		ContributionReceipts: deps.Contributions.Receipts, ContributionAuthority: deps.Contributions.Authority,
		LLM: deps.LLM, CostTracker: deps.CostTracker, Events: deps.Events, EventPublisher: deps.EventPublisher,
		HostIdentity: deps.HostIdentity, Checkpoints: deps.Checkpoints, ProgressStore: deps.ProgressStore,
		VisualStore: deps.VisualStore, HistoryStorage: deps.HistoryStorage, HostResources: deps.HostResources,
		HostPower: deps.HostPower, Pricing: deps.Pricing, AgentPresence: deps.AgentPresence, Workers: deps.Workers,
		WorkerCancel: deps.WorkerCancel, Delegations: deps.Delegations, Board: deps.Board,
		HarnessWorkers: deps.HarnessWorkers, WebResearch: deps.WebResearch, WebDiscoverer: deps.WebDiscoverer,
	}
	apitestdeps.Fill(t, &fill)
	deps.ApprovalGate = fill.ApprovalGate
	deps.Authority.ApprovalDecisions = fill.ApprovalDecisions
	deps.Database, deps.Store, deps.Projects, deps.Sessions = fill.Database, fill.Store, fill.Projects, fill.Sessions
	deps.Settings, deps.Invocations, deps.MutationGate = fill.Settings, fill.Invocations, fill.MutationGate
	deps.ManagedSecrets, deps.SecretIgnores, deps.SourceLedger = fill.ManagedSecrets, fill.SecretIgnores, fill.SourceLedger
	deps.SourceMutations, deps.FileOperations = fill.SourceMutations, fill.FileOperations
	deps.EditorDocuments, deps.FileBriefings, deps.Workflows = fill.EditorDocuments, fill.FileBriefings, fill.Workflows
	deps.WorkflowRuns, deps.WorkflowComposer, deps.WorkflowPersister = fill.WorkflowRuns, fill.WorkflowComposer, fill.WorkflowPersister
	deps.Blueprints, deps.ScanCoordinator, deps.ScanCadence = fill.Blueprints, fill.ScanCoordinator, fill.ScanCadence
	deps.PublishDetections, deps.DataDir, deps.ModuleRoot = fill.PublishDetections, fill.DataDir, fill.ModuleRoot
	deps.MCP, deps.ExtensionViews = fill.MCP, fill.ExtensionViews
	deps.Contributions.Receipts, deps.Contributions.Authority = fill.ContributionReceipts, fill.ContributionAuthority
	deps.LLM, deps.CostTracker, deps.Events, deps.EventPublisher = fill.LLM, fill.CostTracker, fill.Events, fill.EventPublisher
	deps.HostIdentity, deps.Checkpoints, deps.ProgressStore = fill.HostIdentity, fill.Checkpoints, fill.ProgressStore
	deps.VisualStore, deps.HistoryStorage, deps.HostResources = fill.VisualStore, fill.HistoryStorage, fill.HostResources
	deps.HostPower, deps.Pricing, deps.AgentPresence = fill.HostPower, fill.Pricing, fill.AgentPresence
	deps.Workers, deps.WorkerCancel, deps.Delegations, deps.Board = fill.Workers, fill.WorkerCancel, fill.Delegations, fill.Board
	deps.HarnessWorkers = fill.HarnessWorkers
	deps.WebResearch, deps.WebDiscoverer = fill.WebResearch, fill.WebDiscoverer
	return deps
}
