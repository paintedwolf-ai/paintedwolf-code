package portablepython

// OpKind enumerates the operations in the independent reference state model.
type OpKind string

const (
	OpOpen             OpKind = "open"
	OpRead             OpKind = "read"
	OpWrite            OpKind = "write"
	OpFlush            OpKind = "flush"
	OpClose            OpKind = "close"
	OpRename           OpKind = "rename"
	OpHumanEdit        OpKind = "human_edit"
	OpApprovalDecision OpKind = "approval_decision"
	OpInterrupt        OpKind = "interrupt"
	OpRestart          OpKind = "restart"
)

// ModelOp is one step in a generated or regression operation sequence.
type ModelOp struct {
	Kind     OpKind
	Path     string
	DestPath string
	HandleID int
	Mode     string // "r" or "w"
	Data     []byte
	Approved bool
	ReqID    string
}

// StepResult reports the observable outcome of executing one ModelOp.
type StepResult struct {
	Data      []byte
	HandleID  int
	Published bool
	Conflict  bool
	Rejected  bool
	Reason    string
}

// OpenHandle represents an active guest file handle.
type OpenHandle struct {
	ID       int
	Path     string
	Mode     string
	Snapshot []byte
	Buffer   []byte
	Closed   bool
}

// PublishedEvent records one host-committed revision in history.
type PublishedEvent struct {
	ReqID    string
	Path     string
	Revision int
	Data     []byte
}

// HostState stores host-owned saved bytes, editor drafts, revisions, and aliases.
type HostState struct {
	Saved     map[string][]byte
	Drafts    map[string][]byte
	Revisions map[string]int
	Aliases   map[string]string
}

// GuestState stores guest-staged bytes, captured base revisions, and open handles.
type GuestState struct {
	Stage       map[string][]byte
	BaseRevs    map[string]int
	Dirty       map[string]bool
	Handles     map[int]*OpenHandle
	NextHandle  int
	Alive       bool
	LatchedStop bool
}

// ReferenceModel is an independent state machine for guest/host document semantics.
type ReferenceModel struct {
	Host        HostState
	Guest       GuestState
	History     []PublishedEvent
	AppliedReqs map[string]bool
	PendingReq  string
}
