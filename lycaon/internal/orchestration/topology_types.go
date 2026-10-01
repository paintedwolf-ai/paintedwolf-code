package orchestration

// DefaultIterationCap is the default max iterations per agent/task.
const DefaultIterationCap = 8

const wholeProjectScopeGlob = "**/*"

// TeamStrategy controls how team members execute.
type TeamStrategy string

const (
	TeamStrategyParallel   TeamStrategy = "parallel"
	TeamStrategySequential TeamStrategy = "sequential"
)

// TaskResult is output from a team member.
type TaskResult struct {
	AgentID string
	Output  string
	Error   string
}

// TerminationReason explains early team or run termination.
type TerminationReason string

const (
	TerminationReasonMaxIterations TerminationReason = "max_iterations"
	TerminationReasonStuck         TerminationReason = "stuck"
	TerminationReasonConvergence   TerminationReason = "convergence"
	TerminationReasonHumanAbort    TerminationReason = "human_abort"
)

// AggregationMode controls fan-out result merging.
type AggregationMode string

const (
	AggregationUnion     AggregationMode = "union"
	AggregationIntersect AggregationMode = "intersect"
	AggregationVote      AggregationMode = "vote"
	AggregationMerge     AggregationMode = "merge"
)

// MergeStrategy controls how pack agent outputs combine.
type MergeStrategy string

const (
	MergeFirstValid MergeStrategy = "first_valid"
	MergeConsensus  MergeStrategy = "consensus"
	MergeUnion      MergeStrategy = "union"
)

// DefaultPackCount is the recommended homogeneous parallel agent count (3–5 sweet spot).
const DefaultPackCount = 3

// PipelineStage is one sequential step in a pipeline topology.
type PipelineStage struct {
	Name string
	// Label names the stage's leg where a person watches it run.
	Label        string
	AgentProfile string
	InputFrom    []string
	OutputFormat string
}

// SupervisorSpec configures a supervisor topology.
type SupervisorSpec struct {
	Strategy   TeamStrategy
	ProfileIDs []string
	MaxAgents  int
}

// PipelineSpec configures a pipeline topology.
type PipelineSpec struct {
	Stages []PipelineStage
}

// FanOutSpec configures a fan-out topology.
type FanOutSpec struct {
	ProfileID   string
	Subtasks    []string
	MaxWorkers  int
	Aggregation AggregationMode
}

// PackSpec configures a pack topology (N homogeneous parallel probes).
type PackSpec struct {
	Count         int
	ProfileID     string
	MergeStrategy MergeStrategy
}
