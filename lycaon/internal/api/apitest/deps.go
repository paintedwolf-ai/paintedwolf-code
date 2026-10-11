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
		ApprovalGate:      deps.Approvals.ApprovalGate,
		ApprovalDecisions: deps.Approvals.Authority.ApprovalDecisions,
		Database:          deps.Core.Database, Store: deps.Core.Store, Projects: deps.Core.Projects, Sessions: deps.Core.Sessions,
		Settings: deps.Core.Settings, Invocations: deps.Core.Invocations, MutationGate: deps.Core.MutationGate,
		ManagedSecrets: deps.Approvals.ManagedSecrets, SecretIgnores: deps.Approvals.SecretIgnores, SourceLedger: deps.Source.SourceLedger,
		SourceMutations: deps.Source.SourceMutations, FileOperations: deps.Source.FileOperations,
		EditorDocuments: deps.Source.EditorDocuments, FileBriefings: deps.Source.FileBriefings, Workflows: deps.Workflow.Workflows,
		WorkflowRuns: deps.Workflow.WorkflowRuns, WorkflowComposer: deps.Workflow.WorkflowComposer, WorkflowPersister: deps.Workflow.WorkflowPersister,
		Blueprints: deps.Workflow.Blueprints, ScanCoordinator: deps.Scans.ScanCoordinator, ScanCadence: deps.Scans.ScanCadence,
		PublishDetections: deps.Scans.PublishDetections, DataDir: deps.Storage.DataDir, ModuleRoot: deps.Storage.ModuleRoot,
		MCP: deps.External.MCP, ExtensionViews: deps.Extensions.ExtensionViews,
		ContributionReceipts: deps.Extensions.Contributions.Receipts, ContributionAuthority: deps.Extensions.Contributions.Authority,
		LLM: deps.Providers.LLM, CostTracker: deps.Providers.CostTracker, Events: deps.Host.Events, EventPublisher: deps.Host.EventPublisher,
		HostIdentity: deps.Host.HostIdentity, Checkpoints: deps.Approvals.Checkpoints, ProgressStore: deps.Source.ProgressStore,
		VisualStore: deps.Source.VisualStore, HistoryStorage: deps.External.HistoryStorage, HostResources: deps.Host.HostResources,
		HostPower: deps.Host.HostPower, Pricing: deps.Host.Pricing, AgentPresence: deps.Source.AgentPresence, Workers: deps.Workflow.Workers,
		WorkerCancel: deps.Workflow.WorkerCancel, Delegations: deps.Workflow.Delegations, Board: deps.Workflow.Board,
		HarnessWorkers: deps.Harness.HarnessWorkers, WebResearch: deps.External.WebResearch, WebDiscoverer: deps.External.WebDiscoverer,
	}
	apitestdeps.Fill(t, &fill)
	deps.Approvals.ApprovalGate = fill.ApprovalGate
	deps.Approvals.Authority.ApprovalDecisions = fill.ApprovalDecisions
	deps.Core.Database, deps.Core.Store, deps.Core.Projects, deps.Core.Sessions = fill.Database, fill.Store, fill.Projects, fill.Sessions
	deps.Core.Settings, deps.Core.Invocations, deps.Core.MutationGate = fill.Settings, fill.Invocations, fill.MutationGate
	deps.Approvals.ManagedSecrets, deps.Approvals.SecretIgnores, deps.Source.SourceLedger = fill.ManagedSecrets, fill.SecretIgnores, fill.SourceLedger
	deps.Source.SourceMutations, deps.Source.FileOperations = fill.SourceMutations, fill.FileOperations
	if deps.Source.SourceInventory == nil {
		deps.Source.SourceInventory = fill.SourceLedger.Inventory
	}
	deps.Source.EditorDocuments, deps.Source.FileBriefings, deps.Workflow.Workflows = fill.EditorDocuments, fill.FileBriefings, fill.Workflows
	deps.Workflow.WorkflowRuns, deps.Workflow.WorkflowComposer, deps.Workflow.WorkflowPersister = fill.WorkflowRuns, fill.WorkflowComposer, fill.WorkflowPersister
	deps.Workflow.Blueprints, deps.Scans.ScanCoordinator, deps.Scans.ScanCadence = fill.Blueprints, fill.ScanCoordinator, fill.ScanCadence
	deps.Scans.PublishDetections, deps.Storage.DataDir, deps.Storage.ModuleRoot = fill.PublishDetections, fill.DataDir, fill.ModuleRoot
	deps.External.MCP, deps.Extensions.ExtensionViews = fill.MCP, fill.ExtensionViews
	deps.Extensions.Contributions.Receipts, deps.Extensions.Contributions.Authority = fill.ContributionReceipts, fill.ContributionAuthority
	deps.Providers.LLM, deps.Providers.CostTracker, deps.Host.Events, deps.Host.EventPublisher = fill.LLM, fill.CostTracker, fill.Events, fill.EventPublisher
	deps.Host.HostIdentity, deps.Approvals.Checkpoints, deps.Source.ProgressStore = fill.HostIdentity, fill.Checkpoints, fill.ProgressStore
	deps.Source.VisualStore, deps.External.HistoryStorage, deps.Host.HostResources = fill.VisualStore, fill.HistoryStorage, fill.HostResources
	deps.Host.HostPower, deps.Host.Pricing, deps.Source.AgentPresence = fill.HostPower, fill.Pricing, fill.AgentPresence
	deps.Workflow.Workers, deps.Workflow.WorkerCancel, deps.Workflow.Delegations, deps.Workflow.Board = fill.Workers, fill.WorkerCancel, fill.Delegations, fill.Board
	deps.Harness.HarnessWorkers = fill.HarnessWorkers
	deps.External.WebResearch, deps.External.WebDiscoverer = fill.WebResearch, fill.WebDiscoverer
	return deps
}
