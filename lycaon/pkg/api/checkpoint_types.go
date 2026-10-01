package api

// CheckpointKind discriminates human checkpoint payloads.
type CheckpointKind string

const (
	CheckpointKindToolApproval CheckpointKind = "tool_approval"
	CheckpointKindContentApply CheckpointKind = "content_apply"
)

// ContentApplyDecision is the human resolution for content_apply checkpoints.
type ContentApplyDecision string

const (
	ContentApplyApprove        ContentApplyDecision = "approve"
	ContentApplyReject         ContentApplyDecision = "reject"
	ContentApplyApprovePartial ContentApplyDecision = "approve_partial"
)

// CheckpointStatus is the lifecycle state of a checkpoint.
type CheckpointStatus string

const (
	CheckpointStatusPending  CheckpointStatus = "pending"
	CheckpointStatusApproved CheckpointStatus = "approved"
	CheckpointStatusRejected CheckpointStatus = "rejected"
	CheckpointStatusExpired  CheckpointStatus = "expired"
	CheckpointStatusCanceled CheckpointStatus = "canceled"
)

// SocketCapabilityAuthority is the effective authority for an AF_UNIX grant.
type SocketCapabilityAuthority string

const (
	SocketCapabilityAuthorityOutsideSandboxDaemon SocketCapabilityAuthority = "outside_sandbox_daemon"
)

type ApprovalPlanStage string

const (
	ApprovalPlanStagePreSpawn ApprovalPlanStage = "pre_spawn"
	ApprovalPlanStagePreDial  ApprovalPlanStage = "pre_dial"
	ApprovalPlanStagePreSend  ApprovalPlanStage = "pre_send"
)

type ApprovalSubjectKind string

const (
	ApprovalSubjectKindAction          ApprovalSubjectKind = "action"
	ApprovalSubjectKindActionSet       ApprovalSubjectKind = "action_set"
	ApprovalSubjectKindSocketSet       ApprovalSubjectKind = "socket_set"
	ApprovalSubjectKindDirectIP        ApprovalSubjectKind = "direct_ip"
	ApprovalSubjectKindProcessControl  ApprovalSubjectKind = "process_control"
	ApprovalSubjectKindHostExecution   ApprovalSubjectKind = "host_execution"
	ApprovalSubjectKindLocalListen     ApprovalSubjectKind = "local_listen"
	ApprovalSubjectKindLoopbackConnect ApprovalSubjectKind = "loopback_connect"
	ApprovalSubjectKindDestinationSet  ApprovalSubjectKind = "destination_set"
	ApprovalSubjectKindWriteRootSet    ApprovalSubjectKind = "write_root_set"
	ApprovalSubjectKindReadPathSet     ApprovalSubjectKind = "read_path_set"
	ApprovalSubjectKindSecret          ApprovalSubjectKind = "secret"
	ApprovalSubjectKindPackageSet      ApprovalSubjectKind = "package_set"
)

type ApprovalSecretOriginKind string

const (
	ApprovalSecretOriginFile  ApprovalSecretOriginKind = "file"
	ApprovalSecretOriginField ApprovalSecretOriginKind = "field"
)

// ApprovalSecretDestinationKind names who receives a screened send.
type ApprovalSecretDestinationKind string

const (
	// ApprovalSecretDestinationModelProvider is the configured AI provider; the
	// model reads the send.
	ApprovalSecretDestinationModelProvider ApprovalSecretDestinationKind = "model_provider"
	// ApprovalSecretDestinationService is an external destination addressed by URL.
	ApprovalSecretDestinationService ApprovalSecretDestinationKind = "service"
	// ApprovalSecretDestinationProcess is a local process whose onward
	// destinations are not observed here.
	ApprovalSecretDestinationProcess ApprovalSecretDestinationKind = "process"
	// ApprovalSecretDestinationFile is a local file on disk.
	ApprovalSecretDestinationFile ApprovalSecretDestinationKind = "file"
)

// ApprovalSecretLocation is the host-authored secret identity line.
type ApprovalSecretLocation struct {
	SecretNames      []string                      `json:"secret_names,omitempty"`
	Recipients       []ApprovalSecretRecipient     `json:"recipients,omitempty"`
	Origin           string                        `json:"origin"`
	Destination      string                        `json:"destination"`
	OriginKind       ApprovalSecretOriginKind      `json:"origin_kind"`
	DestinationKind  ApprovalSecretDestinationKind `json:"destination_kind"`
	Path             string                        `json:"path,omitempty"`
	Line             int                           `json:"line,omitempty"`
	RevealToolCallID string                        `json:"reveal_tool_call_id,omitempty"`
}

type ApprovalOptionKind string

const (
	ApprovalOptionKindCurrentAction ApprovalOptionKind = "current_action"
	ApprovalOptionKindLease         ApprovalOptionKind = "lease"
	ApprovalOptionKindRedacted      ApprovalOptionKind = "redacted"
	ApprovalOptionKindQuiet         ApprovalOptionKind = "quiet"
	ApprovalOptionKindTracked       ApprovalOptionKind = "tracked"
)

type ApprovalOptionRung string

const (
	ApprovalOptionRungRedacted  ApprovalOptionRung = "redacted"
	ApprovalOptionRungTracked   ApprovalOptionRung = "tracked"
	ApprovalOptionRungUnchanged ApprovalOptionRung = "unchanged"
	ApprovalOptionRungOnce      ApprovalOptionRung = "once"
	ApprovalOptionRungDay       ApprovalOptionRung = "day"
	ApprovalOptionRungChat      ApprovalOptionRung = "chat"
	ApprovalOptionRungProject   ApprovalOptionRung = "project"
	ApprovalOptionRungDevice    ApprovalOptionRung = "device"
)

type ApprovalOptionDecision string

const (
	ApprovalOptionDecisionApprove ApprovalOptionDecision = "approve"
	ApprovalOptionDecisionRedact  ApprovalOptionDecision = "redact"
	ApprovalOptionDecisionTrack   ApprovalOptionDecision = "track"
)

// ResolveCheckpointRequest is discriminated by kind.
type ResolveCheckpointRequest struct {
	Kind CheckpointKind `json:"kind"`

	// tool_approval
	Action ApprovalDecisionAction `json:"action,omitempty"`
	// OptionID selects one exact host-authored option from tool_approval.plan.
	OptionID string `json:"option_id,omitempty"`
	// Guidance adds non-authorizing direction to a denying resolution.
	Guidance string                      `json:"guidance,omitempty"`
	Secrets  []PromptSecretReferencePart `json:"secrets,omitempty"`

	// Decision uses the vocabulary defined by the checkpoint kind.
	Decision string `json:"decision,omitempty"`
	// ApprovedHunks selects host-authored hunk IDs.
	ApprovedHunks []string `json:"approved_hunks,omitempty"`
}

// ApprovalDecisionAction is the tool-approval resolution action.
type ApprovalDecisionAction string

const (
	ApprovalActionApprove ApprovalDecisionAction = "approve"
	ApprovalActionReject  ApprovalDecisionAction = "reject"
)

// PresentedFact is one cited input behind an approval card.
type PresentedFact struct {
	Gate   ApprovalGate `json:"gate,omitempty"`
	Key    string       `json:"key"`
	Value  string       `json:"value"`
	Source string       `json:"source"`
}
