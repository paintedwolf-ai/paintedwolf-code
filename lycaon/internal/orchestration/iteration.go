package orchestration

// MaxIterations is the hard cap on iterations per agent/task.
const MaxIterations = 8

// MaxStuckIterations is consecutive identical errors before kill-and-reassign.
const MaxStuckIterations = 3

// NextAction is the recommended response after a termination check.
type NextAction string

const (
	NextActionReassign NextAction = "reassign"
	NextActionAbort    NextAction = "abort"
)

// IterationState tracks per-agent/task iteration metrics.
type IterationState struct {
	Count      int
	LastError  *string
	ErrorCount int // consecutive identical errors
	Stuck      bool
}

// TerminationDecision is the outcome of cap or self-termination checks.
type TerminationDecision struct {
	ShouldTerminate bool
	Reason          TerminationReason
	NextAction      NextAction
}

// TerminationContext is input for convergence and confidence-based early exit.
type TerminationContext struct {
	AgentID        string
	TaskID         string
	Confidence     float64 // 0–1; above threshold triggers convergence exit
	IterationState IterationState
	OutputStable   bool // true when last N outputs are equivalent
}

// DefaultConvergenceConfidence is the threshold for self-termination convergence exit.
const DefaultConvergenceConfidence = 0.85
