package briefing

// SectionMode states whether the model may be asked for a section.
type SectionMode int

const (
	// Generated sections are asked of the model when no result exists.
	Generated SectionMode = iota
	// Lookup sections use a stored result and are otherwise absent.
	Lookup
	// Deterministic sections carry text computed from host facts.
	Deterministic
)

// SectionPlan is one resolved section of a target, built with bounded work
// and no model call.
type SectionPlan struct {
	ID       string
	Mode     SectionMode
	Manifest Manifest
	// Prompt is required for Generated sections.
	Prompt Prompt
	// Text is the Deterministic section's value.
	Text string
	// Fallback is factual text used when generation yields nothing usable.
	Fallback string
}

// Prompt is the rendered utility request for a generated section.
type Prompt struct {
	System    string
	User      string
	MaxTokens int
}

// Request asks for one resolved target.
type Request struct {
	ProjectID  string
	ProjectDir string
	ProfileID  string
	Version    string
	TargetKey  string
	// TargetKind, TargetJSON and ScopeFingerprint describe the target for
	// storage; they never enter a manifest.
	TargetKind       string
	TargetJSON       string
	ScopeFingerprint string
	Manual           bool
	Policy           Policy
	Sections         []SectionPlan
}

// SectionState is a section of a target's current resolution.
type SectionState struct {
	ID           string
	ManifestHash string
	Result       *Result
	Reused       bool
}

// Status is a target's settlement.
type Status string

const (
	StatusPending  Status = "pending"
	StatusComplete Status = "complete"
	StatusFailed   Status = "failed"
)

// Snapshot is a target's state as one request or completion sees it.
type Snapshot struct {
	ProfileID string
	TargetKey string
	Status    Status
	AttemptID string
	Sections  []SectionState
	RecordID  string
	Epoch     int64
	Revision  int64
	ErrorCode string
	Error     string
	// Regenerate answers a manual request: whether any section needed work.
	Regenerate RegenerateOutcome
}

// RegenerateOutcome reports what a manual request did.
type RegenerateOutcome string

const (
	InputsUnchanged RegenerateOutcome = "inputs_unchanged"
	Started         RegenerateOutcome = "started"
)

// Trigger names who asked for an attempt.
func (r Request) Trigger() string {
	if r.Manual {
		return "manual"
	}
	return "automatic"
}
