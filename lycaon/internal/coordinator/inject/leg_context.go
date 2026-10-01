package inject

import "github.com/lycaon/lycaon/pkg/api"

// WorkerLegContext is host-built Layer 3 input for worker prompts.
type WorkerLegContext struct {
	LegID              string
	AgentType          string
	WorkflowID         string
	PhaseID            string
	TopologyPattern    string
	CompletionCriteria []string
	RequiresIsolation  bool
	FailedLeaves       []string
	Checklist          []string
	LegTools           []string
	ScanDigest         []string
	HasPeerFindings    bool
	SiblingNotesMore   bool
	SiblingNotesAfter  int64
	SiblingNotes       []SiblingNote
	ReservedPaths      []ReservedPath
	AgentsMDMessage    api.Message
	LayoutTopLevel     []string
}

// SiblingNote is one cross-cutting finding from another worker, pushed into a
// worker's per-turn context so peers never poll for shared findings.
type SiblingNote struct {
	ID      int64
	HasBody bool
	Agent   string
	Summary string
	Ref     string
}

// ReservedPath is one path held by a sibling worker via handoff_reserve.
type ReservedPath struct {
	Path     string
	JobID    string
	LegLabel string
}

// BuildWorkerPromptContext applies default L3 fields from leg input.
func BuildWorkerPromptContext(leg WorkerLegContext) WorkerLegContext {
	out := leg
	if out.TopologyPattern == "" {
		out.TopologyPattern = "pipeline"
	}
	return out
}

// SiblingNotePage is the bounded peer context prepared for one model request.
type SiblingNotePage struct {
	Notes  []SiblingNote
	Cursor int64
	More   bool
}
