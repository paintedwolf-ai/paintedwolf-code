// Package orchestration defines multi-agent topology coordination. Run resolves
// each stage's profile through AgentRegistry, dispatches legs through the
// delegation manager, and checks the iteration cap and self-termination each
// iteration.
package orchestration

import (
	"context"

	"github.com/lycaon/lycaon/internal/extpacks"
)

// TopologyPattern selects orchestration topology from config.
type TopologyPattern string

const (
	TopologySupervisor TopologyPattern = "supervisor"
	TopologyPipeline   TopologyPattern = "pipeline"
	TopologyFanOut     TopologyPattern = "fan_out"
	TopologyPack       TopologyPattern = "pack"
)

// allTopologyPatterns is the set of patterns OrchestratorImpl.Run dispatches. The
// Run default error and the coverage tests derive from it.
var allTopologyPatterns = []TopologyPattern{
	TopologySupervisor,
	TopologyPipeline,
	TopologyFanOut,
	TopologyPack,
}

// AllTopologyPatterns returns a copy of the patterns OrchestratorImpl.Run dispatches.
func AllTopologyPatterns() []TopologyPattern {
	return append([]TopologyPattern(nil), allTopologyPatterns...)
}

// TopologySpec is the config-driven run definition (loaded from YAML or built in code).
type TopologySpec struct {
	ID            string
	Pattern       TopologyPattern
	Task          string
	Criterion     string
	IterationCap  int
	BudgetUSD     *float64
	Supervisor    *SupervisorSpec
	Pipeline      *PipelineSpec
	FanOut        *FanOutSpec
	Pack          *PackSpec
	WorkspaceMode WorkspaceMode
}

// RunRequest starts an orchestrated multi-agent run.
type RunRequest struct {
	SessionID       string
	Topology        TopologySpec
	WorkflowID      string
	WorkflowVersion string
	Input           map[string]any
}

// RunResult is the outcome of Orchestrator.Run.
type RunResult struct {
	RunID        string
	FinalOutput  string
	StageOutputs map[string]string
	TaskResults  []TaskResult
}

// RunStatus reports in-progress orchestration state.
type RunStatus struct {
	RunID  string
	Phase  string
	Active bool
}

// Orchestrator coordinates config-driven multi-agent topologies. Leg dispatch and
// evidence gates run in the delegation manager.
type Orchestrator interface {
	LoadTopology(ctx context.Context, path extpacks.Source) (*TopologySpec, error)
	Run(ctx context.Context, req RunRequest) (*RunResult, error)
	Status(ctx context.Context, runID string) (*RunStatus, error)
	Cancel(ctx context.Context, runID string, reason TerminationReason) error
}
