// Code generated from docs/openapi.yaml. DO NOT EDIT.

package api

import "time"

// AbortDelegationRequest
type AbortDelegationRequest struct {
	Reason string `json:"reason,omitempty"`
}

// AbortSessionRequest
type AbortSessionRequest struct {
	// Optional note for worker cancellation envelopes
	Reason string `json:"reason,omitempty"`
}

// ActiveWorkflowRunResponse
type ActiveWorkflowRunResponse struct {
	Run *WorkflowRun `json:"run"`
}

// ActivityEvent One session-scoped host activity lease. The active edge opens the lease and the done edge with the same activity_id closes it. Further active edges replace measured progress on that lease; arguments remain on the canonical transcript tool call. A lease outlives the turn that opened it: an awaiting_wake lease spans an idle session, so a client closes leases on their done edge or an authoritative session activity snapshot, never on a session status change.
type ActivityEvent struct {
	ActivityID string         `json:"activity_id"`
	SessionID  string         `json:"session_id"`
	Kind       ActivityKind   `json:"kind"`
	Status     ActivityStatus `json:"status"`
	StartedAt  time.Time      `json:"started_at"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Progress   *ToolProgress  `json:"progress,omitempty"`
	// Subscriptions that will end an awaiting_wake lease, in the host's own vocabulary (timer, next_worker_done, process_done, …). Present only on awaiting_wake, and what the client names rather than inferring a subject from the sleep reason.
	WaitTriggers []string `json:"wait_triggers,omitempty"`
	// What asked the local decision engine. Present only on deciding, which the host opens only while an available engine is answering; the answer arrives as a turn_load event.
	DecisionTrigger TurnLoadTrigger `json:"decision_trigger,omitempty"`
}

// AddSecretIgnoreRequest
type AddSecretIgnoreRequest struct {
	RootID string            `json:"root_id"`
	Entry  SecretIgnoreEntry `json:"entry"`
}

// AdvanceWorkflowRunRequest
type AdvanceWorkflowRunRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

// AdvisoryCVSS
type AdvisoryCVSS struct {
	Type   string   `json:"type"`
	Vector string   `json:"vector"`
	Score  *float64 `json:"score,omitempty"`
}

// AdvisoryPackageRef
type AdvisoryPackageRef struct {
	Name      string `json:"name,omitempty"`
	Version   string `json:"version,omitempty"`
	Ecosystem string `json:"ecosystem,omitempty"`
}

// AdvisoryRef One vulnerability under every id it is published as. osv_id is the canonical id chosen from the whole alias set (a CVE when one is known, else a GHSA, else the first ecosystem database id); the family lists carry every id, canonical included.
type AdvisoryRef struct {
	OSVID   string       `json:"osv_id,omitempty"`
	Kind    AdvisoryKind `json:"kind,omitempty"`
	CVEIDs  []string     `json:"cve_ids,omitempty"`
	GHSAIDs []string     `json:"ghsa_ids,omitempty"`
	// Ecosystem advisory database ids for the same vulnerability (GO-, PYSEC-, RUSTSEC-, MAL-, ...).
	Aliases        []string            `json:"aliases,omitempty"`
	Package        *AdvisoryPackageRef `json:"package,omitempty"`
	SeveritySource string              `json:"severity_source,omitempty"`
	CVSS           []AdvisoryCVSS      `json:"cvss,omitempty"`
	FixedVersions  []string            `json:"fixed_versions,omitempty"`
	URLs           []string            `json:"urls,omitempty"`
}

// AgentActivity A running tool call with one file target, from call start until its result commits.
type AgentActivity struct {
	ToolCallID string `json:"tool_call_id"`
	// Worker job that made the call; omitted for the chat itself.
	WorkerID string `json:"worker_id,omitempty"`
	Tool     string `json:"tool"`
	RootID   string `json:"root_id"`
	// Root-relative path.
	Path string            `json:"path"`
	Kind AgentActivityKind `json:"kind"`
}

// AgentContextInstruction
type AgentContextInstruction struct {
	// Root-relative AGENTS.md path in runtime precedence order.
	Path string `json:"path"`
}

// AgentContextSkill
type AgentContextSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Root-relative SKILL.md path.
	Path string `json:"path"`
}

// AgentIntent A write-family tool call whose mutation the host has resolved but not yet landed. Ranges are the resolved targets in the current document.
type AgentIntent struct {
	ID string `json:"id"`
	// Worker job that made the call; omitted for the chat itself.
	WorkerID   string               `json:"worker_id,omitempty"`
	ToolCallID string               `json:"tool_call_id"`
	Tool       string               `json:"tool"`
	Operation  AgentIntentOperation `json:"operation"`
	State      AgentIntentState     `json:"state"`
	// Approval checkpoint holding the mutation; present only while awaiting approval.
	CheckpointID string `json:"checkpoint_id,omitempty"`
	RootID       string `json:"root_id"`
	// Root-relative target path.
	Path string `json:"path"`
	// Root-relative destination for move and copy.
	ToPath     string              `json:"to_path,omitempty"`
	DocumentID string              `json:"document_id,omitempty"`
	Epoch      int64               `json:"epoch,omitempty"`
	Extent     AgentPresenceExtent `json:"extent"`
	Ranges     []AgentTextRange    `json:"ranges"`
}

// AgentPresenceEvent One session's complete presence, replacing any earlier value for that session.
type AgentPresenceEvent struct {
	ProjectID string               `json:"project_id"`
	SessionID string               `json:"session_id"`
	Revision  int64                `json:"revision"`
	Presence  AgentSessionPresence `json:"presence"`
}

// AgentPresenceSnapshot The host's ephemeral view of agent sessions in a project's files. It is held in memory, bounded per session, and empty after the host restarts.
type AgentPresenceSnapshot struct {
	ProjectID string `json:"project_id"`
	// Project presence revision; events with a revision at or below it are already reflected.
	Revision int64                  `json:"revision"`
	Sessions []AgentSessionPresence `json:"sessions"`
}

// AgentRead Text a read-family tool call returned to the model in this turn, after host output limits. Output that spilled to a preview records no ranges.
type AgentRead struct {
	ID string `json:"id"`
	// Worker job that made the read; omitted for the chat itself.
	WorkerID string `json:"worker_id,omitempty"`
	// Order within the session's turn; higher is newer.
	Sequence   int64  `json:"sequence"`
	ToolCallID string `json:"tool_call_id"`
	Tool       string `json:"tool"`
	RootID     string `json:"root_id"`
	// Root-relative path.
	Path string `json:"path"`
	// Editor document the text came from; omitted when no document served the read.
	DocumentID string `json:"document_id,omitempty"`
	// Document epoch the anchors belong to.
	Epoch int64 `json:"epoch,omitempty"`
	// Document revision the model read.
	Revision int64               `json:"revision,omitempty"`
	Extent   AgentPresenceExtent `json:"extent"`
	Ranges   []AgentTextRange    `json:"ranges"`
	// True when text inside a range changed after the read by someone other than this session.
	Stale bool `json:"stale"`
}

// AgentSessionPresence Everything one chat is doing in the project's files right now, including the workers it dispatched. A chat is its root session; worker child sessions fold into it and are identified per item by worker_id. Every list empty means the chat has no presence.
type AgentSessionPresence struct {
	// Root chat session.
	SessionID string `json:"session_id"`
	// Chat title; omitted until set.
	Title string `json:"title,omitempty"`
	// User-turn ordinal of the chat the activities, reads, and intents belong to.
	Turn         int                `json:"turn"`
	Activities   []AgentActivity    `json:"activities"`
	Reads        []AgentRead        `json:"reads"`
	Intents      []AgentIntent      `json:"intents"`
	WorkerDrafts []AgentWorkerDraft `json:"worker_drafts"`
}

// AgentTextRange One span of a document. Lines are 1-based and inclusive and describe the document at the revision the item was recorded against. Characters are UTF-16 offsets within their line and appear only for search matches and for empty ranges where lines go in. Anchor and head are CRDT relative positions in the item's editor document epoch; clients resolve them in their replica so a span follows later edits, and fall back to lines only when the item has no document.
type AgentTextRange struct {
	StartLine      int    `json:"start_line"`
	EndLine        int    `json:"end_line"`
	StartCharacter *int   `json:"start_character,omitempty"`
	EndCharacter   *int   `json:"end_character,omitempty"`
	Anchor         []byte `json:"anchor,omitempty"`
	Head           []byte `json:"head,omitempty"`
}

// AgentWorkerDraft A worker job's pending change to one primary-tree file. Ranges are the changed lines of the current document and appear once the draft is ready.
type AgentWorkerDraft struct {
	WorkerID string                `json:"worker_id"`
	State    AgentWorkerDraftState `json:"state"`
	RootID   string                `json:"root_id"`
	// Root-relative path.
	Path       string              `json:"path"`
	DocumentID string              `json:"document_id,omitempty"`
	Epoch      int64               `json:"epoch,omitempty"`
	Insertions *int                `json:"insertions,omitempty"`
	Deletions  *int                `json:"deletions,omitempty"`
	Extent     AgentPresenceExtent `json:"extent"`
	Ranges     []AgentTextRange    `json:"ranges"`
}

// ApprovalConfigResponse
type ApprovalConfigResponse struct {
	Scope      SettingsScope  `json:"scope"`
	Rules      []ApprovalRule `json:"rules"`
	MergedFrom []string       `json:"merged_from"`
	// Read-only effective approval rules contributed by extension packs. Project entries are additive and can never replace device entries.
	ManagedRules []ManagedApprovalRule `json:"managed_rules"`
	// Effective ask-line (light|balanced|strict), after project overlay merge.
	ApprovalPosture string `json:"approval_posture,omitempty"`
	// Effective AI rationale toggle after merge. Default true.
	AIRationaleEnabled bool `json:"ai_rationale_enabled"`
	// Effective never-ask override after merge. True means the approval layer is disabled outright — no discretionary, substrate, secret-screen, or detection asks. Global-only: a project overlay may restore asking but can never set this true. Default false.
	NeverAsk bool `json:"never_ask"`
	// Present on project scope. default = inherit Settings; override = project set.
	FieldSources *ApprovalFieldSources `json:"field_sources,omitempty"`
	// Present on project scope. Device-global effective values for the following-Settings summary when the project override is disabled.
	Defaults *ApprovalDefaults `json:"defaults,omitempty"`
}

// ApprovalDefaults
type ApprovalDefaults struct {
	ApprovalPosture    string `json:"approval_posture"`
	AIRationaleEnabled bool   `json:"ai_rationale_enabled"`
	// Device-global disable state before a project restoration override.
	NeverAsk bool `json:"never_ask"`
}

// ApprovalFieldSources
type ApprovalFieldSources struct {
	ApprovalPosture    string `json:"approval_posture"`
	AIRationaleEnabled string `json:"ai_rationale_enabled"`
	NeverAsk           string `json:"never_ask"`
}

// ApprovalFileChange
type ApprovalFileChange struct {
	// File surface being changed; index identifies a Git staging change.
	Target    string `json:"target,omitempty"`
	Path      string `json:"path"`
	RootID    string `json:"root_id,omitempty"`
	FromPath  string `json:"from_path,omitempty"`
	Operation string `json:"operation"`
	// Screened presentation text of the file before the change. Paths and text pass the host's capture screen; the hashes and sizes identify the exact bytes the reviewed action binds.
	Before string `json:"before"`
	// Screened presentation text of the file after the change.
	After        string `json:"after"`
	BeforeSHA256 string `json:"before_sha256,omitempty"`
	AfterSHA256  string `json:"after_sha256,omitempty"`
	BeforeBytes  int64  `json:"before_bytes"`
	AfterBytes   int64  `json:"after_bytes"`
	// Explains binary, oversized, or metadata-only previews.
	PreviewNote string `json:"preview_note,omitempty"`
}

// ApprovalGrant One revocable host-minted approval lease. Listed by GET /v1/approval-grants and removed by POST /v1/approval-grants/revoke. Unix socket leases use category socket_path with approved_path and resolved_path. Managed-secret leases name their secrets and recipients; local connection leases remain separately revocable.
type ApprovalGrant struct {
	// Host-classified elevated authority; empty for ordinary approvals.
	ElevatedEffects []ElevatedAccessEffect `json:"elevated_effects,omitempty"`
	// Display names of the protected values covered by this lease.
	SecretNames []string `json:"secret_names,omitempty"`
	// Reviewed receivers, without secret values or fingerprints.
	SecretRecipients []ApprovalSecretRecipient `json:"secret_recipients,omitempty"`
	// Opaque id bound to the complete authority and security witness.
	ID       string                `json:"id"`
	Scope    ApprovalGrantScope    `json:"scope"`
	Category ApprovalGrantCategory `json:"category"`
	// Searchable host label. For socket_path this mirrors approved_path; authority matching uses approved_path and resolved_path. Secret releases expose only a count label; their fingerprints stay host-only.
	Pattern string `json:"pattern"`
	// Host-rendered bounded-lease label.
	Title string `json:"title"`
	// When the host minted the lease.
	GrantedAt time.Time  `json:"granted_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Reviewed host copy describing exactly what the lease covers.
	Coverage    string `json:"coverage"`
	ExpiresWhen string `json:"expires_when"`
	ReaskWhen   string `json:"reask_when"`
	// Canonical identity of the project a project-scoped lease binds to. Grants group by this, not by folder: attaching, detaching or moving a project's folders never moves or invalidates its saved approvals.
	ProjectID string `json:"project_id,omitempty"`
	// A folder the lease was granted from, for display only. Absent for a project with no folders attached.
	ProjectDir string `json:"project_dir,omitempty"`
	// True when the granted target no longer exists on this host — a write_root whose path fails Lstat, or a socket_path whose approved path no longer resolves to a usable socket. Display and bulk-clear only; never auto-revoked.
	Unavailable bool `json:"unavailable,omitempty"`
	// Number of exact actions an action_set lease covers. Exact action identities are host-only opaque digests and never cross the wire.
	ActionCount int `json:"action_count,omitempty"`
	// Host resource ids for host_resource leases. Coverage composes per id.
	ResourceIDs []string `json:"resource_ids,omitempty"`
	// Exact AF_UNIX path the human approved (socket_path grants).
	ApprovedPath string `json:"approved_path,omitempty"`
	// Current host-resolved target for the approved path.
	ResolvedPath string `json:"resolved_path,omitempty"`
	// True when re-resolution succeeds but the target differs from the stored resolved_path (grant is not applied until renewed).
	Repointed bool `json:"repointed,omitempty"`
	// True when expires_at is in the past (display; list may still return it briefly).
	Expired            bool                      `json:"expired,omitempty"`
	EffectiveAuthority SocketCapabilityAuthority `json:"effective_authority,omitempty"`
	// Host copy stating the service runs outside the sandbox and may act on files, processes, devices, and network.
	AuthorityWarning string `json:"authority_warning,omitempty"`
	// Origin of the lease (settings, checkpoint, recovery).
	Source string `json:"source,omitempty"`
	// The chat's root session id when scope is chat.
	ChatSessionID string `json:"chat_session_id,omitempty"`
	// Chat title on chat-scoped rows, for grouping in Settings.
	SessionTitle string `json:"session_title,omitempty"`
	// Host copy for revoke consequences. Socket grants use "Applies to the next command or terminal. Already-running processes retain access."
	RevokeAppliesTo string `json:"revoke_applies_to,omitempty"`
}

// ApprovalGrantRevokeResult
type ApprovalGrantRevokeResult struct {
	ID string `json:"id"`
	// True only when a grant with this id existed and was removed.
	Revoked bool         `json:"revoked"`
	Code    ApiErrorCode `json:"code,omitempty"`
	// Host copy when not revoked; absent on success. Branch on code.
	Message string `json:"message,omitempty"`
}

// ApprovalGrantsResponse
type ApprovalGrantsResponse struct {
	Grants []ApprovalGrant `json:"grants"`
	// Live ask quiets for the listed chats (suppression, not authority).
	Quiets map[string]AskQuiet `json:"quiets,omitempty"`
}

// ApprovalHeldRelease Values a person stored that an approving option would hand to the listed recipients. They leave only while the person's chat is unlocked: while it is locked, every option whose decision_action is approve needs the person's verified presence on this device, which also unlocks the chat, and the API bearer alone cannot answer it. A redacted send hands over nothing and needs none.
type ApprovalHeldRelease struct {
	// The chat whose unlock these values leave under.
	ChatSessionID string `json:"chat_session_id"`
	// The recipients are already approved; the card exists to unlock the chat, and its one option needs presence.
	UnlockOnly bool                      `json:"unlock_only,omitempty"`
	Secrets    []ApprovalHeldSecret      `json:"secrets"`
	Recipients []ApprovalSecretRecipient `json:"recipients"`
}

// ApprovalHeldSecret One value a person stored that this approval would send.
type ApprovalHeldSecret struct {
	Reference string `json:"reference"`
	Name      string `json:"name"`
	Version   int64  `json:"version"`
}

// ApprovalOption
type ApprovalOption struct {
	ID    string             `json:"id"`
	Kind  ApprovalOptionKind `json:"kind"`
	Rung  ApprovalOptionRung `json:"rung"`
	Scope ApprovalGrantScope `json:"scope,omitempty"`
	// Optional ladder heading when a card carries a second subject (absorbed predicate or host resources). Absent means the primary ladder.
	Group          string                 `json:"group,omitempty"`
	Title          string                 `json:"title"`
	Coverage       string                 `json:"coverage"`
	ExpiresWhen    string                 `json:"expires_when"`
	ReaskWhen      string                 `json:"reask_when"`
	DecisionAction ApprovalOptionDecision `json:"decision_action"`
	// When true, the option stays in its ladder slot but is not selectable. A slot that vanished would move every rung below it, so an unreachable rung is disabled in place and carries a note.
	Disabled bool `json:"disabled,omitempty"`
	// Host copy saying why a disabled option cannot be picked here. Present exactly when disabled is true.
	Note string `json:"note,omitempty"`
}

// ApprovalPlan
type ApprovalPlan struct {
	ID           string                   `json:"id"`
	ActionDigest string                   `json:"action_digest"`
	Stage        ApprovalPlanStage        `json:"stage"`
	Subject      ApprovalSubject          `json:"subject"`
	Presentation ApprovalPlanPresentation `json:"presentation"`
	// Every gate that fired, in citation-priority order. The first is the primary reason and is repeated in presentation.gate.
	Reasons []ApprovalGate   `json:"reasons"`
	Options []ApprovalOption `json:"options"`
	// Host-classified elevated access that a selectable saved option on this card can install. Empty when no choice would make the composer unlock indicator appear. Allow once is excluded.
	ElevatedEffects []ElevatedAccessEffect `json:"elevated_effects,omitempty"`
	// Id of the face option. Exactly one; computed by the host from the subject kind and rung set. Clients must not invent a fallback. A disabled face stays in that slot; Enter does not select another send.
	RecommendedOptionID string               `json:"recommended_option_id"`
	HeldRelease         *ApprovalHeldRelease `json:"held_release,omitempty"`
}

// ApprovalPlanPresentation
type ApprovalPlanPresentation struct {
	// Ephemeral review handle for one unprotected value. Does not authorize release or contain secret bytes.
	IgnoreCandidateID string               `json:"ignore_candidate_id,omitempty"`
	FileChanges       []ApprovalFileChange `json:"file_changes,omitempty"`
	Action            string               `json:"action"`
	Tool              string               `json:"tool,omitempty"`
	// Argv attributed to this card. When the subject is that same command (ordinary action approvals), it is the reviewed subject. When the subject is a write root, destination, socket, or secret, it is the causing command Den shows under the grant subject. Empty when no argv is known. Secret command cards carry a host-redacted argv here so From can name the send without leaking the match.
	Command   string                  `json:"command,omitempty"`
	Location  *ApprovalSecretLocation `json:"location,omitempty"`
	Impact    string                  `json:"impact"`
	Who       string                  `json:"who,omitempty"`
	IfWrong   string                  `json:"if_wrong,omitempty"`
	AllowLine string                  `json:"allow_line,omitempty"`
	// The one distinguishing fact the card leads with — first contact with this host, what the chat has already read — lifted by the host from the primary gate's citations. Absent when the gate cited no such fact.
	Lead string `json:"lead,omitempty"`
	// The primary reason this card exists. Other gates that fired on the same action are listed in the plan reasons.
	Gate ApprovalGate `json:"gate,omitempty"`
	// The facts that made the gates fire, grouped in gate-priority order. A card with a gate and no citations is invalid — the decision has to be able to show its work.
	Cited []PresentedFact `json:"cited,omitempty"`
	// Why the redacted send on this card is unavailable. The face stays Send redacted and is not selectable; clients render this as host copy beside the impact line and never infer unavailability themselves.
	OptionNote      string          `json:"option_note,omitempty"`
	GrantDelta      string          `json:"grant_delta,omitempty"`
	ConsequenceBand ConsequenceBand `json:"consequence_band,omitempty"`
	ConsequenceCode ConsequenceCode `json:"consequence_code,omitempty"`
	Detection       *DetectionMatch `json:"detection,omitempty"`
	// Extension approval-rule identities that governed this action.
	ApprovalRules []ApprovalRuleCitation `json:"approval_rules,omitempty"`
}

// ApprovalRecentAskRow
type ApprovalRecentAskRow struct {
	Gate ApprovalGate `json:"gate"`
	// Cards raised for this reason in the window.
	Asks    int `json:"asks"`
	Allowed int `json:"allowed"`
	// Denied, expired, or canceled.
	Denied    int        `json:"denied"`
	LastAskAt *time.Time `json:"last_ask_at,omitempty"`
	// Redaction-safe subject titles, most recent first.
	Subjects []string `json:"subjects,omitempty"`
	// When every subject in the row was a tunnel or request to hosts under one registrable site, the family pattern a Settings grant could lease. Absent otherwise.
	HostPattern string `json:"host_pattern,omitempty"`
}

// ApprovalRecentAsksResponse
type ApprovalRecentAsksResponse struct {
	WindowDays int                    `json:"window_days"`
	SinceAt    time.Time              `json:"since_at"`
	Asks       []ApprovalRecentAskRow `json:"asks"`
}

// ApprovalRepeat
type ApprovalRepeat struct {
	ReasonKey string `json:"reason_key"`
	// Distinct subjects asked about under this reason.
	Count int `json:"count"`
	// Cards this reason has raised since the last user turn, this one included. Presentation only; nothing counts it to decide.
	Asks              int      `json:"asks,omitempty"`
	Subjects          []string `json:"subjects,omitempty"`
	SubjectsTruncated bool     `json:"subjects_truncated,omitempty"`
	SuppressedCount   int      `json:"suppressed_count,omitempty"`
}

// ApprovalRule
type ApprovalRule struct {
	Category ApprovalCategory `json:"category"`
	Pattern  string           `json:"pattern"`
	Effect   ApprovalEffect   `json:"effect"`
}

// ApprovalRuleCitation
type ApprovalRuleCitation struct {
	Category string `json:"category"`
	Pattern  string `json:"pattern"`
	Effect   string `json:"effect"`
	Command  string `json:"command,omitempty"`
	UnitID   string `json:"unit_id,omitempty"`
	PackID   string `json:"pack_id,omitempty"`
	Scope    string `json:"scope,omitempty"`
}

// ApprovalSecretRecipient Reviewed handoff identity. It does not assert onward delivery or authentication.
type ApprovalSecretRecipient struct {
	Label   string                        `json:"label"`
	Surface string                        `json:"surface"`
	Kind    ApprovalSecretDestinationKind `json:"kind"`
}

// ApprovalSubject
type ApprovalSubject struct {
	Kind    ApprovalSubjectKind `json:"kind"`
	Title   string              `json:"title"`
	Summary string              `json:"summary,omitempty"`
	Targets []ApprovalTarget    `json:"targets"`
}

// ApprovalTarget
type ApprovalTarget struct {
	Kind    string         `json:"kind"`
	Label   string         `json:"label"`
	Details map[string]any `json:"details,omitempty"`
}

// ArtifactEvent A durable visual artifact was written or deleted. Identity only: clients re-read the artifact list so one tile's metadata, reference counts, and bytes all render from one place.
type ArtifactEvent struct {
	ArtifactID string `json:"artifact_id"`
	ProjectID  string `json:"project_id"`
	// Producing (holder) session, when one is known.
	SessionID string `json:"session_id,omitempty"`
	// Session tree that resolves the event, when one is known.
	RootSessionID string           `json:"root_session_id,omitempty"`
	Op            ArtifactChangeOp `json:"op"`
}

// ArtifactListItem Metadata-only durable artifact row (no bytes, no host paths)
type ArtifactListItem struct {
	ID             string               `json:"id"`
	Mime           string               `json:"mime"`
	Source         VisualArtifactSource `json:"source"`
	Caption        string               `json:"caption,omitempty"`
	EvidenceHandle string               `json:"evidence_handle,omitempty"`
	// Held browser page that produced live-tool media.
	PageID        string     `json:"page_id,omitempty"`
	SessionID     string     `json:"session_id"`
	WorkflowRunID string     `json:"workflow_run_id,omitempty"`
	ToolCallID    string     `json:"tool_call_id,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	RecordedAt    *time.Time `json:"recorded_at,omitempty"`
	DurationMS    int64      `json:"duration_ms,omitempty"`
	// Message that produced or presented this artifact when known
	OriginMessageID string `json:"origin_message_id,omitempty"`
	// What would break if this artifact were deleted, counted by kind. Absent or empty means nothing durable claims it.
	References []ArtifactReferenceCount `json:"references,omitempty"`
}

// ArtifactListResponse
type ArtifactListResponse struct {
	Artifacts []ArtifactListItem `json:"artifacts"`
	// Opaque cursor for the next project artifact page; empty at the end.
	NextCursor string `json:"next_cursor,omitempty"`
}

// ArtifactReferenceCount
type ArtifactReferenceCount struct {
	Kind  ArtifactReferenceKind `json:"kind"`
	Count int                   `json:"count"`
}

// AskQuiet One chat-keyed ask suppression (not authority). Listed beside grants by GET /v1/approval-grants; revoked via the same bulk revoke endpoint using quiet_ ids.
type AskQuiet struct {
	// Host-classified elevated authority; empty for ordinary approvals.
	ElevatedEffects []ElevatedAccessEffect `json:"elevated_effects,omitempty"`
	// Opaque id with quiet_ prefix.
	ID            string `json:"id"`
	ChatSessionID string `json:"chat_session_id"`
	// Host quiet key (reason segment, plus seam/host for secrets).
	Key   string `json:"key"`
	Label string `json:"label"`
	// Absent for the chat rung (until the chat is deleted or the quiet is revoked).
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// Asks skipped while this quiet was live.
	Suppressed int       `json:"suppressed"`
	CreatedAt  time.Time `json:"created_at"`
	// Root chat title when known, for Saved approvals grouping.
	SessionTitle string `json:"session_title,omitempty"`
}

// AttachProjectRootRequest
type AttachProjectRootRequest struct {
	Path string `json:"path"`
	// Display label doubling as the @label addressing token; unique per project (case-insensitive). Blank = derived from the folder basename.
	Label     string `json:"label,omitempty"`
	IsPrimary *bool  `json:"is_primary,omitempty"`
}

// AttachmentCapabilities Composer affordances, grouped by the plane each byte bound protects. These let the client show limits before an upload; admission is decided by the host on the upload route, never here.
type AttachmentCapabilities struct {
	// Composer plane — a UTF-8 plain-text paste at or above this size becomes a text attachment
	AutoAttachPasteBytes int `json:"auto_attach_paste_bytes"`
	// Prompt plane — largest UTF-8 user-prose body accepted inline
	MaxInlineTextBytes int `json:"max_inline_text_bytes"`
	// Byte-carrying attachments permitted on one turn
	MaxAttachments int `json:"max_attachments"`
	// Jailed coordinate references permitted on one turn
	MaxReferences int `json:"max_references"`
	MaxImages     int `json:"max_images"`
	// Transport plane — largest single upload body accepted off the wire
	MaxUploadBytes int64 `json:"max_upload_bytes"`
	// Transport plane — largest raster accepted
	MaxImageBytes int64 `json:"max_image_bytes"`
	// Materialization plane — largest body permitted to land on disk
	MaxBodyBytes int64 `json:"max_body_bytes"`
	// Materialization plane — total attachment bytes one turn may reference
	MaxTurnBytes int64 `json:"max_turn_bytes"`
	// Prompt plane — ordinary per-body preview ceiling
	MaxBodyPreviewBytes int `json:"max_body_preview_bytes"`
	// Prompt plane — per-body preview ceiling for text at or above auto_attach_paste_bytes
	MaxLargeTextPreviewBytes int `json:"max_large_text_preview_bytes"`
	// Prompt plane — aggregate body-preview bytes admitted across all attachments and references on one turn
	MaxTurnPreviewBytes int `json:"max_turn_preview_bytes"`
	// Largest body the host will parse as a document (extraction is in-memory)
	MaxDocumentBytes int64 `json:"max_document_bytes"`
	// Largest video the host decodes for its frames (the decoder holds it in memory)
	MaxVideoBytes  int64    `json:"max_video_bytes"`
	ImageMIMETypes []string `json:"image_mime_types"`
	// Video containers the host accepts; whether a file's codec plays is decided at upload
	VideoMIMETypes []string `json:"video_mime_types"`
	TextMIMETypes  []string `json:"text_mime_types"`
	TextExtensions []string `json:"text_extensions"`
	TextBasenames  []string `json:"text_basenames"`
}

// AttachmentUploadResponse Receipt for one materialized attachment body.
type AttachmentUploadResponse struct {
	// Stable sha256 id over the stored body and sanitized filename
	BlobID string `json:"blob_id"`
	// Sanitized leaf name the host stored the body under
	Filename string `json:"filename"`
	// Media type the host detected from the stored bytes
	Mime string         `json:"mime"`
	Kind AttachmentKind `json:"kind"`
	// Materialized byte count after any container was decoded away
	Bytes int64                 `json:"bytes"`
	Video *AttachmentVideoFacts `json:"video,omitempty"`
}

// AttachmentVideoFacts What the host's decoder read from a video at upload. Present only on video receipts; a video the decoder cannot play is refused instead.
type AttachmentVideoFacts struct {
	DurationMs int `json:"duration_ms"`
	Width      int `json:"width"`
	Height     int `json:"height"`
}

// AttentionRow
type AttentionRow struct {
	SessionID string `json:"session_id"`
	ProjectID string `json:"project_id"`
	// Project display name; omitted while the project is still unnamed.
	ProjectName string `json:"project_name,omitempty"`
	// Session display title; omitted until set.
	Title  string          `json:"title,omitempty"`
	Class  AttentionClass  `json:"class"`
	Reason AttentionReason `json:"reason"`
	// When the session entered this class, as the host best knows it — checkpoint or ask creation time for `needs_you`, the turn's completion time for `finished`, last session update otherwise. Clients sort on class first and use this only to break ties and render an age.
	SinceAt time.Time `json:"since_at"`
}

// AttentionView
type AttentionView struct {
	// Every non-idle session across every project, worst class first. A device-wide view: it tells the user about the projects they are not currently looking at.
	Rows []AttentionRow `json:"rows"`
}

// BackgroundProcess
type BackgroundProcess struct {
	Output    *BackgroundProcessOutput `json:"output,omitempty"`
	ProcessID string                   `json:"process_id"`
	Running   bool                     `json:"running"`
	Stages    []BackgroundProcessStage `json:"stages,omitempty"`
	ExitCode  *int                     `json:"exit_code,omitempty"`
}

// BackgroundProcessChunk
type BackgroundProcessChunk struct {
	// Byte offset of the first process output byte this chunk covers.
	Offset int64  `json:"offset"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// BackgroundProcessEvent
type BackgroundProcessEvent struct {
	ProcessID string `json:"process_id"`
	SessionID string `json:"session_id"`
	Stream    string `json:"stream"`
	Text      string `json:"text,omitempty"`
	// Byte offset just past the process output this event reflects; 0 for a held call, which has no byte stream.
	EndOffset int64 `json:"end_offset"`
	Running   bool  `json:"running"`
	ExitCode  *int  `json:"exit_code,omitempty"`
	Truncated bool  `json:"truncated,omitempty"`
	// Replace previously projected text instead of appending it.
	Reset bool `json:"reset,omitempty"`
}

// BackgroundProcessListResponse
type BackgroundProcessListResponse struct {
	Processes []BackgroundProcess `json:"processes"`
}

// BackgroundProcessOutput The screened tail of one process's retained output, bounded by the host. It is a snapshot, not a page: live output continues on the process event topic.
type BackgroundProcessOutput struct {
	ProcessID string                   `json:"process_id"`
	Chunks    []BackgroundProcessChunk `json:"chunks"`
	Running   bool                     `json:"running"`
	// Earlier output was evicted from the host buffer or cut from this snapshot.
	Truncated bool `json:"truncated,omitempty"`
	ExitCode  *int `json:"exit_code,omitempty"`
}

// BackgroundProcessStage
type BackgroundProcessStage struct {
	Command string `json:"command"`
	// Absent while the stage has not exited, and for a stage a sequencing operator skipped. A skipped stage reached no status, so reporting 0 would read as success.
	ExitCode *int `json:"exit_code,omitempty"`
	// The stage never ran: the preceding group's exit status did not satisfy the connector joining them.
	Skipped bool `json:"skipped,omitempty"`
	// Operator joining this stage to the previous one. Absent on the first stage. `|` wires the previous stage's stdout into this one; the rest gate on the previous group's exit status.
	Connector string `json:"connector,omitempty"`
}

// BackgroundProcessStopResult
type BackgroundProcessStopResult struct {
	ProcessID string `json:"process_id"`
	// The host requested termination; this does not assert process exit.
	StopRequested bool `json:"stop_requested"`
	// True until the host has observed terminal process state. Wait for the process event instead of repeating stop.
	Running  bool `json:"running"`
	ExitCode *int `json:"exit_code,omitempty"`
}

// BackupCapabilities
type BackupCapabilities struct {
	FormatVersion    int   `json:"format_version"`
	SchemaRevision   int   `json:"schema_revision"`
	MaxArchiveBytes  int64 `json:"max_archive_bytes"`
	MaxExpandedBytes int64 `json:"max_expanded_bytes"`
}

// BackupRestoreResult
type BackupRestoreResult struct {
	// True when the staged restore must be applied on the next launch
	RestartRequired bool `json:"restart_required"`
	// Absolute path of the restore transaction's recovery copy
	RecoveryCopyPath string `json:"recovery_copy_path"`
}

// BeginManagedSecretRevealRequest Starts one installed-app reveal. The native shell supplies its actual calling window label; it is bound into the proof payload and audit record, but it is not an authorization identity.
type BeginManagedSecretRevealRequest struct {
	WindowLabel string `json:"window_label"`
}

// BeginUnlockChallengeRequest Starts presence verification for one pending option that sends values a person stored while their chat is locked. The challenge binds the checkpoint, the option, the exact plan, and the deciding person.
type BeginUnlockChallengeRequest struct {
	OptionID string `json:"option_id"`
	// Native calling window label, bound into the proof; not an authorization identity.
	WindowLabel string `json:"window_label"`
}

// BlobComparisonSource
type BlobComparisonSource struct {
	Kind          string `json:"kind"`
	RootID        string `json:"root_id"`
	BlobOid       string `json:"blob_oid"`
	BeforeBlobOid string `json:"before_blob_oid,omitempty"`
	DisplayPath   string `json:"display_path,omitempty"`
}

// Blueprint
type Blueprint struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	// Project-relative path under the overlay blueprints/ directory
	Path                string          `json:"path"`
	SourceWorkflowID    string          `json:"source_workflow_id,omitempty"`
	Content             string          `json:"content"`
	Status              BlueprintStatus `json:"status"`
	Version             int             `json:"version"`
	CompatibleWorkflows []string        `json:"compatible_workflows,omitempty"`
	// When the blueprint file was last written. A file carries no creation time.
	UpdatedAt time.Time `json:"updated_at"`
}

// BlueprintApproveRequest
type BlueprintApproveRequest struct {
	Note             string `json:"note,omitempty"`
	WorkflowRunID    string `json:"workflow_run_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	ContentDigest    string `json:"content_digest"`
}

// BlueprintListResponse
type BlueprintListResponse struct {
	Blueprints []BlueprintSummary `json:"blueprints"`
	// Present and true when older blueprints were cut from this answer.
	Truncated bool `json:"truncated,omitempty"`
}

// BlueprintMeta
type BlueprintMeta struct {
	// Project-relative path under the overlay blueprints/ directory
	BlueprintPath string                    `json:"blueprint_path"`
	Revision      int                       `json:"revision"`
	RevisionKey   string                    `json:"revision_key"`
	Status        BlueprintTranscriptStatus `json:"status"`
	// Card chrome state. `rejected` is the terminal record for a run that ended with the blueprint still a draft — the human answered no, or the run was superseded; status carries which.
	Phase          BlueprintCardPhase `json:"phase"`
	PhaseLabel     string             `json:"phase_label"`
	BlueprintTitle string             `json:"blueprint_title"`
	CanApprove     bool               `json:"can_approve"`
	Collapsed      bool               `json:"collapsed"`
	ShowActions    bool               `json:"show_actions"`
}

// BlueprintSummary
type BlueprintSummary struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Project-relative path under the overlay blueprints/ directory
	Path                string          `json:"path"`
	SourceWorkflowID    string          `json:"source_workflow_id,omitempty"`
	Status              BlueprintStatus `json:"status"`
	Version             int             `json:"version"`
	CompatibleWorkflows []string        `json:"compatible_workflows,omitempty"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

// BoardDelegationEntry
type BoardDelegationEntry struct {
	ID    string          `json:"id"`
	Phase DelegationPhase `json:"phase"`
	Task  string          `json:"task,omitempty"`
}

// BoardEvent
type BoardEvent struct {
	ProjectID   string           `json:"project_id"`
	SessionID   string           `json:"session_id,omitempty"`
	Snapshot    BoardView        `json:"snapshot"`
	DetailLevel BoardDetailLevel `json:"detail_level"`
}

// BoardForensicWorkers Present only when detail_level is forensic. Full worker job records for host debugging — not for routine coordination.
type BoardForensicWorkers struct {
	Notice string       `json:"notice"`
	Tasks  []WorkerTask `json:"tasks"`
}

// BoardGitRepoLine
type BoardGitRepoLine struct {
	RepoID        string `json:"repo_id"`
	Label         string `json:"label"`
	Branch        string `json:"branch,omitempty"`
	Dirty         bool   `json:"dirty"`
	StagedCount   int    `json:"staged_count"`
	UnstagedCount int    `json:"unstaged_count"`
}

// BoardGitSlice
type BoardGitSlice struct {
	Available       bool               `json:"available"`
	RepoID          string             `json:"repo_id"`
	Label           string             `json:"label"`
	Branch          string             `json:"branch,omitempty"`
	HeadShort       string             `json:"head_short,omitempty"`
	Dirty           bool               `json:"dirty"`
	StagedCount     int                `json:"staged_count"`
	UnstagedCount   int                `json:"unstaged_count"`
	RecentCommits   []string           `json:"recent_commits,omitempty"`
	Others          []BoardGitRepoLine `json:"others"`
	OthersTruncated int                `json:"others_truncated"`
	Worktree        *BoardGitWorktree  `json:"worktree,omitempty"`
}

// BoardGitWorktree The session's own checkout when it has one. Absent for a session working directly in the project's folders.
type BoardGitWorktree struct {
	Branch      string `json:"branch"`
	BaseBranch  string `json:"base_branch"`
	AheadOfBase int    `json:"ahead_of_base"`
	BehindBase  int    `json:"behind_base"`
	Dirty       bool   `json:"dirty"`
}

// BoardScanCompareSlice
type BoardScanCompareSlice struct {
	// Previous complete assessment used for auto-compare.
	BaselineAssessmentID string                 `json:"baseline_assessment_id,omitempty"`
	ScannersCompared     int                    `json:"scanners_compared"`
	NewCount             int                    `json:"new_count"`
	ResolvedCount        int                    `json:"resolved_count"`
	NewByLevel           map[string]int         `json:"new_by_level,omitempty"`
	TopNew               []BoardScanCompareStub `json:"top_new,omitempty"`
}

// BoardScanCompareStub
type BoardScanCompareStub struct {
	File   string `json:"file"`
	RuleID string `json:"rule_id"`
}

// BoardScanSummary
type BoardScanSummary struct {
	AssessmentID     string             `json:"assessment_id"`
	SourceSnapshotID string             `json:"source_snapshot_id,omitempty"`
	CoverageStatus   ScanCoverageStatus `json:"coverage_status,omitempty"`
	ScannerCount     int                `json:"scanner_count"`
	Status           CodeScanStatus     `json:"status"`
	Categories       []ScanCategory     `json:"categories,omitempty"`
	FindingsCount    int                `json:"findings_count"`
	HeadShort        string             `json:"head_short,omitempty"`
	FindingsByLevel  map[string]int     `json:"findings_by_level,omitempty"`
	FindingsByKind   map[string]int     `json:"findings_by_kind,omitempty"`
	// SAST rows still mapped to SCAN_FINDING_UNMAPPED.
	UnmappedCount  int                    `json:"unmapped_count,omitempty"`
	TopLocations   []BoardScanTopLocation `json:"top_locations,omitempty"`
	WarningSummary []ScanWarningSummary   `json:"warning_summary,omitempty"`
	Error          string                 `json:"error,omitempty"`
	Trigger        ScanTrigger            `json:"trigger,omitempty"`
	CreatedAt      time.Time              `json:"created_at"`
	CompletedAt    *time.Time             `json:"completed_at,omitempty"`
	StartedAt      *time.Time             `json:"started_at,omitempty"`
	LongRunning    bool                   `json:"long_running"`
}

// BoardScanTopLocation
type BoardScanTopLocation struct {
	URI       string `json:"uri"`
	StartLine int    `json:"start_line,omitempty"`
}

// BoardScansSlice
type BoardScansSlice struct {
	CurrentAssessment *BoardScanSummary      `json:"current_assessment,omitempty"`
	LatestAttempt     *BoardScanSummary      `json:"latest_attempt,omitempty"`
	Compare           *BoardScanCompareSlice `json:"compare,omitempty"`
}

// BoardView Slim pack board view. Use detail_level=forensic only when you need full worker job records including workspace baselines.
type BoardView struct {
	// Coordinator session this board snapshot belongs to.
	SessionID string `json:"session_id,omitempty"`
	// One-line coordination headline (worker counts, merge status, conflicts).
	Summary           string                       `json:"summary"`
	Repo              RepoBrief                    `json:"repo"`
	Git               *BoardGitSlice               `json:"git,omitempty"`
	Scans             *BoardScansSlice             `json:"scans,omitempty"`
	Roster            []BoardWorkerRosterEntry     `json:"roster,omitempty"`
	PromotePaths      []WorkerPromoteJobPathStatus `json:"promote_paths,omitempty"`
	OverlayMergePlan  *OverlayMergePlan            `json:"overlay_merge_plan,omitempty"`
	Delegation        []BoardDelegationEntry       `json:"delegation,omitempty"`
	ActiveWorkflowRun *WorkflowRun                 `json:"active_workflow_run,omitempty"`
	ForensicWorkers   *BoardForensicWorkers        `json:"forensic_workers,omitempty"`
	Cost              *CostSummary                 `json:"cost"`
	PackContentHash   string                       `json:"pack_content_hash"`
	DetailLevel       BoardDetailLevel             `json:"detail_level"`
	Board             string                       `json:"board"`
	BoardChars        int                          `json:"board_chars"`
	Truncated         bool                         `json:"truncated"`
	TruncatedSections []string                     `json:"truncated_sections,omitempty"`
	GeneratedAt       string                       `json:"generated_at"`
	NowLine           string                       `json:"now_line"`
}

// BoardWorkerRosterEntry
type BoardWorkerRosterEntry struct {
	Dependencies []WorkerDependency `json:"dependencies,omitempty"`
	WorkerID     string             `json:"worker_id"`
	AgentType    string             `json:"agent_type"`
	Status       WorkerStatus       `json:"status"`
	MergeStatus  WorkerMergeStatus  `json:"merge_status,omitempty"`
	OverlayID    string             `json:"overlay_id,omitempty"`
	ScopeSummary string             `json:"scope_summary,omitempty"`
	ChangedPaths []string           `json:"changed_paths,omitempty"`
	TouchedPaths []string           `json:"touched_paths,omitempty"`
	// Worker assignment goal included in full board detail.
	Brief string `json:"brief,omitempty"`
	Error string `json:"error,omitempty"`
	// Effective worker tool-round ceiling when known (host default 20, maximum 120)
	MaxToolLoops int `json:"max_tool_loops,omitempty"`
	// The worker's unanswered request for more tool rounds; grant it with extend_worker_budget
	BudgetRequest *WorkerBudgetRequest `json:"budget_request,omitempty"`
	// Completed tool rounds this worker run
	ToolLoopsUsed int `json:"tool_loops_used,omitempty"`
	// Repo-relative paths held by handoff_reserve for this worker
	Reservations []string `json:"reservations,omitempty"`
	// True when checkpoint SSE reports a pending human checkpoint for this worker child session
	BlockedOnApproval bool `json:"blocked_on_approval,omitempty"`
}

// CLIOpenEvent A `lycaon open` invocation that the host resolved but did not enact. Device-scoped, so it reaches the window whatever project it is showing. `open` names a project that already exists and may be switched to directly — the human typing the command is the explicit action. `create` only proposes a root: minting a project is a durable act that needs trust, so Den confirms rather than the CLI deciding.
type CLIOpenEvent struct {
	Action CLIOpenAction `json:"action"`
	// Resolved project for `open`; null for `create`.
	ProjectID *string `json:"project_id,omitempty"`
	// Absolute canonical path the CLI resolved. The proposed root for `create`; for `open`, the path that matched, which may be a subdirectory of the project root rather than the root itself.
	Path string `json:"path"`
}

// CacheSavings Same-token input-price comparison, including cache-write premiums. May be negative; excludes unpriced cache tokens and is not a prediction of future savings.
type CacheSavings struct {
	EstimatedNanoUsd int64 `json:"estimated_nano_usd"`
	UnpricedTokens   int   `json:"unpriced_tokens"`
}

// CatastrophicDetail Support-report facts for a catastrophic result, shown on the stop screen and copyable by the user.
// This is the one payload a user can read before the app is usable, so it is what gets pasted into a public issue. Every field is an allowlisted, machine-anonymous fact. It excludes: absolute paths, anything under the user's home directory, hostname, user name, project or session ids, project names, provider ids, environment dumps, and log tails. The host builds it at one chokepoint so redaction cannot be bypassed per probe.
type CatastrophicDetail struct {
	// The user-notice code, e.g. `OS_BELOW_FLOOR`.
	Code string `json:"code,omitempty"`
	// Which declared resolution applied.
	Resolution string `json:"resolution,omitempty"`
	// Stable probe id that produced the result.
	ProbeID string `json:"probe_id,omitempty"`
	// Host version string.
	AppVersion string `json:"app_version,omitempty"`
	// VCS revision when the binary was stamped with one. Absent for unstamped builds rather than reported as empty.
	Build string `json:"build,omitempty"`
	// Durable store schema version the host expects.
	SchemaVersion int `json:"schema_version,omitempty"`
	// Operating system name, e.g. `macOS`.
	OSName string `json:"os_name,omitempty"`
	// Operating system version, when it could be read.
	OSVersion string `json:"os_version,omitempty"`
	// Minimum operating system version this build supports.
	OSFloor string `json:"os_floor,omitempty"`
	// CPU architecture, e.g. `arm64`.
	Arch string `json:"arch,omitempty"`
	// Human label for the configuration directory (e.g. `~/.config/paintedwolf`), never an absolute path.
	ConfigDirLabel string `json:"config_dir_label,omitempty"`
	// The probe's redacted detail map, passed through unchanged.
	Facts map[string]string `json:"facts,omitempty"`
	// When the host observed the condition.
	ObservedAt string `json:"observed_at,omitempty"`
}

// ChatComparisonSource
type ChatComparisonSource struct {
	Kind                 string            `json:"kind"`
	First                FileEditReference `json:"first"`
	Last                 FileEditReference `json:"last"`
	ExpectedBeforeSHA256 string            `json:"expected_before_sha256,omitempty"`
	ExpectedAfterSHA256  string            `json:"expected_after_sha256,omitempty"`
}

// ChatContentMatch
type ChatContentMatch struct {
	Offset int `json:"offset"`
	Length int `json:"length"`
}

// ChatContentPage A positional window into one retained revision. offset and end_offset are rune coordinates; reading continues by requesting offset=end_offset.
type ChatContentPage struct {
	Reference ChatContentReference `json:"reference"`
	Text      string               `json:"text"`
	// Rune offset where this window starts.
	Offset int `json:"offset"`
	// Rune offset just past this window; reference.total_runes at the end of the content.
	EndOffset int `json:"end_offset"`
	// True when this window reaches the end of the content.
	Complete bool `json:"complete"`
	// Screened provenance with rune offsets relative to this page.
	Spans []RedactedSpan   `json:"spans"`
	Rows  []ChatContentRow `json:"rows,omitempty"`
}

// ChatContentReference Retained screened content; the inline field is a bounded preview, never a replacement for this revision.
type ChatContentReference struct {
	Field      string `json:"field"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	SHA256     string `json:"sha256"`
	TotalRunes int    `json:"total_runes"`
	SizeBytes  int    `json:"size_bytes"`
	// Number of display rows in this revision.
	Rows        int              `json:"rows"`
	PreviewRows []ChatContentRow `json:"preview_rows,omitempty"`
}

// ChatContentRow
type ChatContentRow struct {
	Index  int            `json:"index"`
	Offset int            `json:"offset"`
	Text   string         `json:"text"`
	Spans  []RedactedSpan `json:"spans"`
}

// ChatContentSearchPage
type ChatContentSearchPage struct {
	Matches []ChatContentMatch `json:"matches"`
	// Opaque cursor for the next search page; absent when the search reached the end of the content.
	NextCursor string `json:"next_cursor,omitempty"`
}

// ChatVault Whether a chat may send values a person stored to recipients it already approved. Times are present only while unlocked.
type ChatVault struct {
	ChatSessionID string `json:"chat_session_id"`
	Unlocked      bool   `json:"unlocked"`
	UnlockedAt    string `json:"unlocked_at,omitempty"`
	// When the unlock ends unless another use extends it.
	ClosesAt string `json:"closes_at,omitempty"`
	// When the unlock ends however busy the chat is.
	ExpiresAt string `json:"expires_at,omitempty"`
}

// CheckpointDecisionMeta
type CheckpointDecisionMeta struct {
	CheckpointID string           `json:"checkpoint_id"`
	Kind         CheckpointKind   `json:"kind"`
	Status       CheckpointStatus `json:"status"`
	// Tool name shown on the card chip — presentation.tool when the plan carried one (argv tool for write-root / egress), else the proposed action tool.
	Tool string `json:"tool,omitempty"`
	// Human-facing grant subject of the decision — command, path, host, or other single subject shown on the approval card. Persisted so the durable decision chicklet stays informative after resolve.
	Subject string `json:"subject,omitempty"`
	// Argv that raised the card when subject is not already that command (write root, destination, secret, …). Empty when the subject is the reviewed command itself.
	CausingCommand string `json:"causing_command,omitempty"`
	// Secret identity line persisted from presentation.location (`origin → destination`). Empty on non-secret decisions.
	Location string `json:"location,omitempty"`
	// Explicit human direction attached to a denial. Informational only; it never grants authority for a replacement action.
	Guidance string `json:"guidance,omitempty"`
	// Every host-opaque ApprovalGrant.id installed by the selected option. Composed options can install several; the decision chicklet revokes them together.
	GrantIDs   []string           `json:"grant_ids,omitempty"`
	GrantScope ApprovalGrantScope `json:"grant_scope,omitempty"`
	// Reviewed host title of the selected lease.
	GrantTitle string `json:"grant_title,omitempty"`
}

// CheckpointEvent
type CheckpointEvent struct {
	ID           string               `json:"id"`
	SessionID    string               `json:"session_id"`
	Kind         CheckpointKind       `json:"kind"`
	Status       CheckpointStatus     `json:"status"`
	IssuedAt     time.Time            `json:"issued_at"`
	ToolApproval *ToolApprovalPayload `json:"tool_approval,omitempty"`
	ContentApply *ContentApplyPayload `json:"content_apply,omitempty"`
}

// CheckpointListResponse
type CheckpointListResponse struct {
	Checkpoints []CheckpointEvent `json:"checkpoints"`
}

// CheckpointResponse
type CheckpointResponse struct {
	ID         string           `json:"id"`
	SessionID  string           `json:"session_id"`
	Kind       CheckpointKind   `json:"kind"`
	Status     CheckpointStatus `json:"status"`
	Result     map[string]any   `json:"result,omitempty"`
	ResolvedAt *time.Time       `json:"resolved_at,omitempty"`
}

// ChoiceTransitionUi
type ChoiceTransitionUi struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Armed bool   `json:"armed"`
}

// CitationGrounding
type CitationGrounding struct {
	Traced                bool                              `json:"traced"`
	HostAssembled         bool                              `json:"host_assembled,omitempty"`
	HintCode              string                            `json:"hint_code,omitempty"`
	RetryCount            int                               `json:"retry_count,omitempty"`
	ObservedPathCount     int                               `json:"observed_path_count,omitempty"`
	ObservedPathsSample   []string                          `json:"observed_paths_sample,omitempty"`
	ObservedURLCount      int                               `json:"observed_url_count,omitempty"`
	ObservedURLsSample    []string                          `json:"observed_urls_sample,omitempty"`
	Checks                []CitationGroundingCheck          `json:"checks,omitempty"`
	Findings              []CitationGroundingFinding        `json:"findings,omitempty"`
	CitedURLs             []string                          `json:"cited_urls,omitempty"`
	ProseLeakCount        int                               `json:"prose_leak_count,omitempty"`
	ProseLeaksSample      []string                          `json:"prose_leaks_sample,omitempty"`
	ProseAdvisoryCount    int                               `json:"prose_advisory_count,omitempty"`
	ProseAdvisoriesSample []string                          `json:"prose_advisories_sample,omitempty"`
	CitedEvidence         []CitationGroundingCitedEvidence  `json:"cited_evidence,omitempty"`
	EvidenceRecords       []CitationGroundingEvidenceRecord `json:"evidence_records,omitempty"`
	// How many ledger records the audit drew evidence_records from. Larger than the array when the wire cap held some back, so a reader can say "24 of 61" rather than mistake the sample for the whole ledger.
	EvidenceRecordCount int                   `json:"evidence_record_count,omitempty"`
	Verification        *CitationVerification `json:"verification,omitempty"`
}

// CitationGroundingCheck
type CitationGroundingCheck struct {
	ID     string                       `json:"id"`
	Label  string                       `json:"label"`
	Status CitationGroundingCheckStatus `json:"status"`
	// Citation checks audit typed citations; lifecycle checks audit worker activity (survey, artifact).
	Kind string `json:"kind,omitempty"`
	// A passed check that had no citations to verify.
	Vacuous bool     `json:"vacuous,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Matched []string `json:"matched,omitempty"`
	Failed  []string `json:"failed,omitempty"`
}

// CitationGroundingCitedEvidence
type CitationGroundingCitedEvidence struct {
	// Host-minted evidence handle resolved from the session ledger.
	Handle  string          `json:"handle,omitempty"`
	Path    string          `json:"path,omitempty"`
	Line    int             `json:"line,omitempty"`
	Excerpt string          `json:"excerpt,omitempty"`
	Verdict CitationVerdict `json:"verdict,omitempty"`
	// True when the cited path resolves to a regular file under the project jail; false when it is a directory, missing, or outside the jail; absent when the host did not resolve the path. Den links only when openable is not false.
	Openable *bool `json:"openable,omitempty"`
}

// CitationGroundingEvidenceRecord
type CitationGroundingEvidenceRecord struct {
	Handle string `json:"handle,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Shape  string `json:"shape,omitempty"`
	// How faithfully this record reproduces what the tool observed — structured | scraped | opaque. Unrelated to Message.trust_tier, which answers whether instructions in content may be followed.
	Fidelity string `json:"fidelity,omitempty"`
	Tool     string `json:"tool,omitempty"`
	Path     string `json:"path,omitempty"`
	// Every URL this observation grounds. A web_search record carries all of its result URLs; a fetch_url record carries the single fetched page.
	URLs []string `json:"urls,omitempty"`
	Line int      `json:"line,omitempty"`
	// Last line of the first observed range; a read that returned lines 80–88 states both.
	LineEnd int `json:"line_end,omitempty"`
	// For a search record, how many matching lines the tool returned.
	MatchCount int `json:"match_count,omitempty"`
	// For a search record, how many distinct files those matches fell in.
	MatchPaths int `json:"match_paths,omitempty"`
	// Page titles the tool reported, keyed by URL. A fetch carries its page's title; a search carries one per result it returned.
	URLTitles map[string]string `json:"url_titles,omitempty"`
	Excerpt   string            `json:"excerpt,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
}

// CitationGroundingFinding
type CitationGroundingFinding struct {
	// Host-minted evidence handle resolved from the session ledger.
	Handle  string          `json:"handle,omitempty"`
	Path    string          `json:"path,omitempty"`
	Line    int             `json:"line,omitempty"`
	Excerpt string          `json:"excerpt,omitempty"`
	Verdict CitationVerdict `json:"verdict,omitempty"`
	Note    string          `json:"note,omitempty"`
	// True when the cited path resolves to a regular file under the project jail; false when it is a directory, missing, or outside the jail; absent when the host did not resolve the path. Den links only when openable is not false.
	Openable *bool `json:"openable,omitempty"`
}

// CitationVerification The validation scope the closeout declared. Retained on the grounding audit because the projected prose omits the trailer.
type CitationVerification struct {
	Method string `json:"method"`
	Reason string `json:"reason"`
}

// CloneProjectRequest
type CloneProjectRequest struct {
	URL string `json:"url"`
	// Existing folder the clone is created under.
	ParentDir string `json:"parent_dir"`
	// Folder name for the clone; derived from the url when omitted.
	Name string `json:"name,omitempty"`
}

// CodeScanEvent
type CodeScanEvent struct {
	ScanID         string                `json:"scan_id"`
	AssessmentID   string                `json:"assessment_id,omitempty"`
	Categories     []ScanCategory        `json:"categories"`
	Status         CodeScanStatus        `json:"status"`
	CoverageStatus ScanCoverageStatus    `json:"coverage_status,omitempty"`
	FailureCode    string                `json:"failure_code,omitempty"`
	FindingsCount  int                   `json:"findings_count"`
	Error          string                `json:"error,omitempty"`
	Guidance       []ScanGuidanceSummary `json:"guidance,omitempty"`
	Runtime        *ScanRuntimePolicy    `json:"runtime,omitempty"`
	Progress       *ScanProgress         `json:"progress,omitempty"`
	StartedAt      *time.Time            `json:"started_at,omitempty"`
	LongRunningAt  *time.Time            `json:"long_running_at,omitempty"`
	LongRunning    bool                  `json:"long_running"`
}

// CodeScanPage Stable newest-first keyset page of project scan history.
type CodeScanPage struct {
	Scans []CodeScan `json:"scans"`
	// Opaque cursor for the next older page, bound to the project root set.
	NextCursor string `json:"next_cursor,omitempty"`
}

// CommandInvokeContext Structured invocation coordinates. The host resolves and verifies these against machine state; a Den boolean is never authority.
type CommandInvokeContext struct {
	RootID           string `json:"root_id,omitempty"`
	Path             string `json:"path,omitempty"`
	DocumentRevision int    `json:"document_revision,omitempty"`
	StartLine        int    `json:"start_line,omitempty"`
	EndLine          int    `json:"end_line,omitempty"`
	Symbol           string `json:"symbol,omitempty"`
	FindingID        string `json:"finding_id,omitempty"`
	// Human instruction for editor actions declaring an instruction target. Prompt content only; it never widens the preset boundary.
	Instruction string `json:"instruction,omitempty"`
}

// CommandInvokeError
type CommandInvokeError struct {
	Code    ApiErrorCode `json:"code"`
	Message string       `json:"message"`
}

// CommandInvokeRequest
type CommandInvokeRequest struct {
	// Idempotency receipt; a UUID minted by the caller. An exact retry replays the stored response verbatim; reusing this id for a different invocation returns 409 idempotency_conflict.
	OperationID string `json:"operation_id"`
	// The frame the caller resolved the command from.
	FrameRevision string                `json:"frame_revision"`
	Context       *CommandInvokeContext `json:"context,omitempty"`
	// Values for the command's declared input schema.
	Args map[string]any `json:"args,omitempty"`
}

// CommandInvokeResponse
type CommandInvokeResponse struct {
	Status string `json:"status"`
	// The frame the host executed against.
	FrameRevision string           `json:"frame_revision"`
	UIEffect      *CommandUIEffect `json:"ui_effect,omitempty"`
	// The user message an accepted editor action submitted.
	MessageID string `json:"message_id,omitempty"`
	// Run id for accepted workflow starts.
	WorkflowRunID string `json:"workflow_run_id,omitempty"`
	// Tool result text for completed MCP tool calls.
	Output string              `json:"output,omitempty"`
	Error  *CommandInvokeError `json:"error,omitempty"`
}

// CommandUIEffect Closed typed UI effect returned by host-validated presentation actions.
type CommandUIEffect struct {
	Kind        string `json:"kind"`
	Text        string `json:"text,omitempty"`
	Destination string `json:"destination,omitempty"`
	URL         string `json:"url,omitempty"`
}

// CommitComparisonSource
type CommitComparisonSource struct {
	Kind         string  `json:"kind"`
	RootID       string  `json:"root_id"`
	Path         string  `json:"path"`
	ExpectedHead *string `json:"expected_head,omitempty"`
}

// CompactedChunkMeta
type CompactedChunkMeta struct {
	OriginalTokens  int       `json:"original_tokens"`
	CompactedTokens int       `json:"compacted_tokens"`
	Kind            string    `json:"kind"`
	Strategy        string    `json:"strategy"`
	CompactedAt     time.Time `json:"compacted_at"`
}

// CompleteManagedSecretRevealRequest Challenge-bound native user-presence proof. The signature covers the challenge payload plus the authenticator identifier; the ordinary API bearer is never sufficient to reveal a value.
type CompleteManagedSecretRevealRequest struct {
	Authenticator PresenceAuthenticator `json:"authenticator"`
	// Base64url without padding Ed25519 signature from the installed desktop shell.
	Signature string `json:"signature"`
}

// CompletionReportAsk The one decision a report asks of its reader, written for someone who has never seen the work.
type CompletionReportAsk struct {
	// What the reader is asked to decide or approve.
	Do     string                    `json:"do"`
	Effort CompletionReportAskEffort `json:"effort"`
	// The consequence that makes the ask worth the reader's time.
	Why string `json:"why,omitempty"`
}

// CompletionReportDefect One document requirement a stored run report failed.
type CompletionReportDefect struct {
	Code CompletionReportDefectCode `json:"code"`
	// What the check found, as it told the coordinator.
	Reason string `json:"reason"`
	// A sample of what the reason is about, such as claim ids or scanner groups.
	Subjects []string `json:"subjects,omitempty"`
	// How many subjects there are; larger than the sample when it was capped.
	Count int `json:"count,omitempty"`
}

// CompletionReportFinding One assessed conclusion a report states: what it is, how it grades, what it means if nothing is done, and what to do about it.
type CompletionReportFinding struct {
	// Stable reference a reader can quote; shared with a verdict claim when the finding was adjudicated.
	ID string `json:"id,omitempty"`
	// The finding in one line, in plain language.
	Title string `json:"title"`
	// Severity in the subject's own vocabulary; ranked labels take a tone, unranked ones render plainly.
	Severity string `json:"severity,omitempty"`
	// Where the finding stands, in the subject's own word.
	Status string `json:"status,omitempty"`
	// What it means if nothing is done.
	Impact string `json:"impact,omitempty"`
	// What to do about it.
	Action      string                             `json:"action,omitempty"`
	Where       []CompletionReportFindingLocation  `json:"where,omitempty"`
	Disposition CompletionReportFindingDisposition `json:"disposition,omitempty"`
	// Answers to the workflow's declared rating questions, keyed by question id. Absent when a claim with the same id carries them.
	Answers map[string]string `json:"answers,omitempty"`
	// Scanner groups of the run's inventory this finding assesses.
	ScanGroupIds []string `json:"scan_group_ids,omitempty"`
}

// CompletionReportFindingLocation Where a finding lives — an evidence handle, a place, or both.
type CompletionReportFindingLocation struct {
	Handle string `json:"handle,omitempty"`
	Path   string `json:"path,omitempty"`
	Line   int    `json:"line,omitempty"`
}

// CompletionReportMeta Identity of a grounded completion report. Present on every message of kind completion_report. Only a run-scoped completion from a workflow with enabled reports can produce a PDF; session and phase completions are ordinary replies without document downloads.
type CompletionReportMeta struct {
	Scope CompletionReportScope `json:"scope"`
	// Coordinator surface that delivered the report (its exit class is report).
	SurfaceID string `json:"surface_id,omitempty"`
	// Workflow phase that produced the report; empty at session scope.
	Phase string `json:"phase,omitempty"`
	// One sentence stating the report's conclusion, authored with the report. The rendered document leads with it; absent, the document leads with its title.
	Headline string `json:"headline,omitempty"`
	// The report's assessment in plain language, written for a reader who will not open the detail behind it.
	Summary string `json:"summary,omitempty"`
	// The assessed conclusions the report states, most severe first. These are the report's own findings, distinct from raw scanner rows.
	Findings []CompletionReportFinding `json:"findings,omitempty"`
	// Coverage gaps the report declares, one per entry: what was not checked, run, or reached. Rendered as a boxed list so a gap survives a reader who never reaches the closing paragraph.
	Limits []string             `json:"limits,omitempty"`
	Ask    *CompletionReportAsk `json:"ask,omitempty"`
	// Scanner groups the report accounts for without assessing them one by one, each with its reason.
	SetAsides []CompletionReportSetAside `json:"set_asides,omitempty"`
	// Run scope only. The document requirements this report still failed when its repairs ran out and the host stored it as drafted, one per failing requirement. Absent on a report the host accepted; a report that carries defects ends its run as not accepted.
	Defects []CompletionReportDefect `json:"defects,omitempty"`
}

// CompletionReportSetAside Scanner groups accounted for together: named by id, or selected as every group one scanner reported entirely inside the listed path globs.
type CompletionReportSetAside struct {
	ScanGroupIds []string `json:"scan_group_ids,omitempty"`
	Scanner      string   `json:"scanner,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	// Why these groups need no assessment of their own.
	Reason string `json:"reason"`
}

// ComposeDecisionPhase
type ComposeDecisionPhase struct {
	ID      string   `json:"id"`
	Prompt  string   `json:"prompt"`
	Options []string `json:"options"`
}

// ComposeEffectiveSummary
type ComposeEffectiveSummary struct {
	Extends                  string                           `json:"extends,omitempty"`
	Phases                   []ComposePhaseSummary            `json:"phases"`
	PhasesRemovedFromParent  []string                         `json:"phases_removed_from_parent,omitempty"`
	PostureTransitions       []ComposePostureTransition       `json:"posture_transitions,omitempty"`
	ExecutionModeTransitions []ComposeExecutionModeTransition `json:"execution_mode_transitions,omitempty"`
	DefaultExecutionMode     string                           `json:"default_execution_mode,omitempty"`
	Feedback                 []ComposeFeedbackPhase           `json:"feedback_phases,omitempty"`
	Decisions                []ComposeDecisionPhase           `json:"decision_phases,omitempty"`
	GatesByPhase             map[string][]string              `json:"gates_by_phase,omitempty"`
	CoordinatorBrief         string                           `json:"coordinator_brief"`
	RequiresIsolation        bool                             `json:"requires_isolation,omitempty"`
}

// ComposeExecutionModeTransition
type ComposeExecutionModeTransition struct {
	Phase string `json:"phase"`
	Mode  string `json:"mode"`
}

// ComposeFeedbackPhase
type ComposeFeedbackPhase struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
}

// ComposeFromTemplateRequest
type ComposeFromTemplateRequest struct {
	TemplateID string         `json:"template_id"`
	Params     map[string]any `json:"params"`
}

// ComposePhaseOnEnter
type ComposePhaseOnEnter struct {
	SetPosture       string `json:"set_posture,omitempty"`
	SetExecutionMode string `json:"set_execution_mode,omitempty"`
}

// ComposePhaseSummary
type ComposePhaseSummary struct {
	ID           string               `json:"id"`
	CompleteWhen string               `json:"complete_when,omitempty"`
	Next         string               `json:"next,omitempty"`
	OnEnter      *ComposePhaseOnEnter `json:"on_enter,omitempty"`
	Gates        []string             `json:"gates,omitempty"`
}

// ComposePostureTransition
type ComposePostureTransition struct {
	Phase   string `json:"phase"`
	Posture string `json:"posture"`
}

// ComposeValidationDetails The `details` of a `workflow_validation_failed` error.
type ComposeValidationDetails struct {
	Errors []ComposeValidationError `json:"errors"`
}

// ComposeValidationError
type ComposeValidationError struct {
	Field       string `json:"field"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Replacement string `json:"replacement,omitempty"`
}

// ComposeWorkflowResponse
type ComposeWorkflowResponse struct {
	Summary          WorkflowSummary         `json:"summary"`
	EffectiveYAML    string                  `json:"effective_yaml"`
	EffectiveSummary ComposeEffectiveSummary `json:"effective_summary"`
}

// ContentApplyHunk
type ContentApplyHunk struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Before string `json:"before"`
	After  string `json:"after"`
}

// ContentApplyPayload
type ContentApplyPayload struct {
	Tool       string  `json:"tool"`
	ToolCallID string  `json:"tool_call_id,omitempty"`
	Path       string  `json:"path"`
	Before     *string `json:"before,omitempty"`
	After      string  `json:"after"`
	// Immutable host-authored selectable edits. Hunk ids are the only client-selectable input to partial composition.
	Hunks []ContentApplyHunk `json:"hunks"`
}

// ContentReviewRule
type ContentReviewRule struct {
	Tool   string `json:"tool,omitempty"`
	Path   string `json:"path"`
	Reason string `json:"reason,omitempty"`
	// Optional pause on this rule. While this instant is in the future, edits matching the rule apply without content review; afterwards the rule asks again. Set by the edit-review card's time-boxed skip.
	DisabledUntilAt *time.Time `json:"disabled_until_at,omitempty"`
}

// ContributionAgentColors Theme-authored color family for agent chats. Den gives each chat a lightness step around one muted hue and enforces display contrast.
type ContributionAgentColors struct {
	Main          string  `json:"main"`
	LightnessStep float64 `json:"lightness_step"`
	Chroma        float64 `json:"chroma"`
}

// ContributionBindingDefault One resolved default chord per platform, scope, and chord identity. A missing active id means equal-provenance candidates deactivated each other.
type ContributionBindingDefault struct {
	Platform   string   `json:"platform"`
	Scope      string   `json:"scope"`
	Chord      string   `json:"chord"`
	Active     string   `json:"active,omitempty"`
	Candidates []string `json:"candidates"`
}

// ContributionChoice
type ContributionChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Detail      string `json:"detail,omitempty"`
}

// ContributionChoiceRequest
type ContributionChoiceRequest struct {
	FrameRevision string         `json:"frame_revision"`
	Answers       map[string]any `json:"answers,omitempty"`
}

// ContributionChoiceResponse
type ContributionChoiceResponse struct {
	FrameRevision string               `json:"frame_revision"`
	Provider      string               `json:"provider"`
	Choices       []ContributionChoice `json:"choices"`
}

// ContributionChoiceSource
type ContributionChoiceSource struct {
	Requirement string            `json:"requirement"`
	Tool        string            `json:"tool"`
	Inputs      map[string]string `json:"inputs,omitempty"`
}

// ContributionCommand
type ContributionCommand struct {
	ID       string   `json:"id"`
	Provider string   `json:"provider"`
	Title    string   `json:"title"`
	Category string   `json:"category,omitempty"`
	Scope    string   `json:"scope,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
	Icon     string   `json:"icon"`
	// Whether Crossbar offers this command. Absent means yes.
	Palette    *bool  `json:"palette,omitempty"`
	Executor   string `json:"executor"`
	Invocation string `json:"invocation"`
	ActionKind string `json:"action_kind"`
	// Stock Den handler id; present only for native_ui.
	HandlerID string `json:"handler_id,omitempty"`
	// Referenced editor-action contribution id.
	ActionRef       string                   `json:"action_ref,omitempty"`
	When            *ContributionCondition   `json:"when,omitempty"`
	Enablement      *ContributionCondition   `json:"enablement,omitempty"`
	Input           []ContributionInputField `json:"input,omitempty"`
	Interaction     *ContributionInteraction `json:"interaction,omitempty"`
	ResultTreatment string                   `json:"result_treatment"`
}

// ContributionCondition Structured predicate tree over the closed fact vocabulary. Exactly one of all, any, not, or fact is set per node.
type ContributionCondition struct {
	All  []ContributionCondition `json:"all,omitempty"`
	Any  []ContributionCondition `json:"any,omitempty"`
	Not  *ContributionCondition  `json:"not,omitempty"`
	Fact string                  `json:"fact,omitempty"`
	Is   string                  `json:"is,omitempty"`
}

// ContributionConfigurationProperty One declared setting with its effective value. `value` is desired state when present and the declared default otherwise.
type ContributionConfigurationProperty struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Default     any    `json:"default,omitempty"`
	// The value in force for this property.
	Value any `json:"value,omitempty"`
	// True when no desired-state value applied and `value` is the declaration's own default.
	IsDefault bool     `json:"is_default"`
	Scope     []string `json:"scope"`
	Enum      []string `json:"enum,omitempty"`
	Min       *float64 `json:"min,omitempty"`
	Max       *float64 `json:"max,omitempty"`
}

// ContributionEditorAction
type ContributionEditorAction struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	TargetKind     string `json:"target_kind"`
	TargetRequired bool   `json:"target_required,omitempty"`
	Preset         string `json:"preset"`
}

// ContributionFrameResponse One complete projection of a captured contribution frame. Pure projection; never an independent source of truth.
type ContributionFrameResponse struct {
	FrameRevision   string                              `json:"frame_revision"`
	Commands        []ContributionCommand               `json:"commands"`
	Menus           []ContributionMenu                  `json:"menus"`
	Keybindings     []ContributionKeybinding            `json:"keybindings"`
	BindingDefaults []ContributionBindingDefault        `json:"binding_defaults"`
	EditorActions   []ContributionEditorAction          `json:"editor_actions"`
	Themes          []ContributionTheme                 `json:"themes"`
	Configuration   []ContributionConfigurationProperty `json:"configuration"`
	Requirements    []ContributionRequirement           `json:"requirements"`
	SearchSources   []ContributionSearchSource          `json:"search_sources"`
	Operations      []ContributionOperation             `json:"operations"`
	Notes           []ContributionNote                  `json:"notes"`
}

// ContributionIconNode One shape in a themed glyph, already validated against the host's closed element and attribute vocabularies. Den builds an element from this and sets each attribute individually — no authored string ever becomes markup, so there is no sanitizer to get wrong.
type ContributionIconNode struct {
	Tag string `json:"tag"`
	// Geometry attributes. Paint is currentColor or none.
	Attrs map[string]string `json:"attrs,omitempty"`
	// Non-empty only for a group.
	Children []ContributionIconNode `json:"children,omitempty"`
}

// ContributionIconStroke The theme's handwriting, applied to every stroked glyph including the ones it never redrew. Weight multiplies each slot's host-defined width, so the product's relative weights survive a restyle. Stroke never contributes to layout, so none of this can move anything.
type ContributionIconStroke struct {
	Weight float64 `json:"weight"`
	Cap    string  `json:"cap"`
	Join   string  `json:"join"`
}

// ContributionInputField
type ContributionInputField struct {
	ID          string   `json:"id"`
	Title       string   `json:"title,omitempty"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Values      []string `json:"values,omitempty"`
	Default     any      `json:"default,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
}

// ContributionInteraction
type ContributionInteraction struct {
	Steps []ContributionInteractionStep `json:"steps"`
}

// ContributionInteractionPredicate
type ContributionInteractionPredicate struct {
	Step string `json:"step"`
	Is   any    `json:"is"`
}

// ContributionInteractionStep
type ContributionInteractionStep struct {
	ID          string                            `json:"id"`
	Title       string                            `json:"title"`
	Description string                            `json:"description,omitempty"`
	Kind        string                            `json:"kind"`
	Required    bool                              `json:"required,omitempty"`
	Values      []ContributionChoice              `json:"values,omitempty"`
	Min         *float64                          `json:"min,omitempty"`
	Max         *float64                          `json:"max,omitempty"`
	Multiple    bool                              `json:"multiple,omitempty"`
	Source      *ContributionChoiceSource         `json:"source,omitempty"`
	If          *ContributionInteractionPredicate `json:"if,omitempty"`
}

// ContributionKeybinding
type ContributionKeybinding struct {
	ID           string                 `json:"id"`
	Command      string                 `json:"command"`
	Scope        string                 `json:"scope"`
	AllowInInput bool                   `json:"allow_in_input,omitempty"`
	Bindings     map[string][]string    `json:"bindings"`
	When         *ContributionCondition `json:"when,omitempty"`
}

// ContributionMenu
type ContributionMenu struct {
	ID      string                 `json:"id"`
	Slot    string                 `json:"slot"`
	Command string                 `json:"command"`
	Group   string                 `json:"group,omitempty"`
	Order   int                    `json:"order,omitempty"`
	Label   string                 `json:"label,omitempty"`
	When    *ContributionCondition `json:"when,omitempty"`
	// Condition the toggle reflects. With state_label the item reads state_label while it holds; without it the item carries a checkmark.
	State      *ContributionCondition `json:"state,omitempty"`
	StateLabel string                 `json:"state_label,omitempty"`
}

// ContributionNote
type ContributionNote struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	UnitID  string `json:"unit_id,omitempty"`
	PackID  string `json:"pack_id,omitempty"`
}

// ContributionOperation
type ContributionOperation struct {
	ID              string                   `json:"id"`
	Provider        string                   `json:"provider"`
	Input           []ContributionInputField `json:"input,omitempty"`
	Output          []ContributionInputField `json:"output,omitempty"`
	ActionKind      string                   `json:"action_kind"`
	ActionRef       string                   `json:"action_ref,omitempty"`
	ResultTreatment string                   `json:"result_treatment"`
}

// ContributionRequirement One MCP requirement joined against the captured resource generation.
type ContributionRequirement struct {
	ID         string `json:"id"`
	ProviderID string `json:"provider_id"`
	Purpose    string `json:"purpose,omitempty"`
	Ready      bool   `json:"ready"`
	// provider_missing, provider_disabled, provider_not_ready, or tool_missing.
	Reason       string   `json:"reason,omitempty"`
	MissingTools []string `json:"missing_tools,omitempty"`
}

// ContributionSearchRequest
type ContributionSearchRequest struct {
	FrameRevision string `json:"frame_revision"`
	Query         string `json:"query"`
}

// ContributionSearchResponse
type ContributionSearchResponse struct {
	FrameRevision string                     `json:"frame_revision"`
	SourceID      string                     `json:"source_id"`
	Provider      string                     `json:"provider"`
	Results       []ContributionSearchResult `json:"results"`
}

// ContributionSearchResult
type ContributionSearchResult struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Detail      string         `json:"detail,omitempty"`
	Kind        string         `json:"kind,omitempty"`
	Arguments   map[string]any `json:"arguments,omitempty"`
}

// ContributionSearchSource
type ContributionSearchSource struct {
	ID                string `json:"id"`
	Provider          string `json:"provider"`
	Label             string `json:"label"`
	Prefix            string `json:"prefix"`
	Requirement       string `json:"requirement"`
	Tool              string `json:"tool"`
	MinQueryLength    int    `json:"min_query_length"`
	MaxResults        int    `json:"max_results"`
	Ready             bool   `json:"ready"`
	DisabledReason    string `json:"disabled_reason,omitempty"`
	ActivationCommand string `json:"activation_command"`
}

// ContributionSyntaxStyle One compiled syntax scope. Colors are literal; the three flags are the complete typography surface of a theme.
type ContributionSyntaxStyle struct {
	Color     string `json:"color"`
	Italic    bool   `json:"italic,omitempty"`
	Bold      bool   `json:"bold,omitempty"`
	Underline bool   `json:"underline,omitempty"`
}

// ContributionTheme One compiled theme. Values are total — every base token and syntax scope resolved through host derivations and the scope fold. Den applies these, and generates window colors from the resolved palette recipe.
type ContributionTheme struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Appearance string `json:"appearance"`
	// Base-plane token id to literal color.
	Tokens map[string]string                  `json:"tokens"`
	Syntax map[string]ContributionSyntaxStyle `json:"syntax"`
	// Glyphs this theme overrides, keyed by host icon slot. Partial by design: an absent slot keeps the host glyph, because a glyph has no derivation. The host defines the frame — viewBox, rendered size, stroke weight, paint — so a themed glyph can never resize a control or carry its own color.
	Icons        map[string][]ContributionIconNode `json:"icons,omitempty"`
	IconStroke   ContributionIconStroke            `json:"icon_stroke"`
	WindowColors ContributionWindowColors          `json:"window_colors"`
	AgentColors  ContributionAgentColors           `json:"agent_colors"`
	// Whether the product logomark paints under this theme. A theme may quiet the mark; it can never substitute one, so this carries a visibility and never artwork. Hiding reserves the mark's box, so a theme swap does not relayout the window.
	Logomark string `json:"logomark"`
	// Pack id that declared this theme.
	Provider string `json:"provider,omitempty"`
}

// ContributionWindowColors Theme-authored OKLCH color family for editor windows. Den samples this recipe by stable window identity and enforces display contrast.
type ContributionWindowColors struct {
	Main      string   `json:"main"`
	Anchors   []string `json:"anchors"`
	HueSpread float64  `json:"hue_spread"`
	ChromaMin float64  `json:"chroma_min"`
	ChromaMax float64  `json:"chroma_max"`
}

// CoordinatorLoopProgress
type CoordinatorLoopProgress struct {
	HostTurn bool `json:"host_turn,omitempty"`
	// 1-based index of the model turn now running.
	Iteration     int `json:"iteration,omitempty"`
	MaxIterations int `json:"max_iterations,omitempty"`
	// Tools-omitted closeout turn — reserved last iteration or early closeout (blocked loop, cancel, spend, timeout).
	ProseFinish bool   `json:"prose_finish,omitempty"`
	Surface     string `json:"surface,omitempty"`
	// Host-projected activity label from the coordinator surface catalog.
	ActivityLabel     string `json:"activity_label,omitempty"`
	Guarded           bool   `json:"guarded,omitempty"`
	ProvisionalHidden bool   `json:"provisional_hidden,omitempty"`
}

// CoordinatorRunContext
type CoordinatorRunContext struct {
	WorkflowID                   string                 `json:"workflow_id,omitempty"`
	WorkflowVersion              string                 `json:"workflow_version,omitempty"`
	RunID                        string                 `json:"run_id,omitempty"`
	RunStatus                    string                 `json:"run_status,omitempty"`
	CurrentPhase                 string                 `json:"current_phase,omitempty"`
	CoordinatorBrief             string                 `json:"coordinator_brief,omitempty"`
	FailedLeaves                 []string               `json:"failed_leaves,omitempty"`
	AllowedAgents                []string               `json:"allowed_agents,omitempty"`
	RequiresIsolation            bool                   `json:"requires_isolation,omitempty"`
	HasComposeDraft              bool                   `json:"has_compose_draft,omitempty"`
	PhaseExecutionMode           string                 `json:"phase_execution_mode,omitempty"`
	WorkflowDefaultExecutionMode string                 `json:"workflow_default_execution_mode,omitempty"`
	PendingFeedback              *PendingFeedback       `json:"pending_feedback,omitempty"`
	Request                      *WorkflowRequestState  `json:"request,omitempty"`
	FeedbackPhases               []ComposeFeedbackPhase `json:"feedback_phases,omitempty"`
	DecisionPhases               []ComposeDecisionPhase `json:"decision_phases,omitempty"`
	BatchPhase                   string                 `json:"batch_phase,omitempty"`
	BatchSeq                     int                    `json:"batch_seq,omitempty"`
	PhaseCoordinatorSurface      string                 `json:"phase_coordinator_surface,omitempty"`
	PhaseSurfaceTemplate         string                 `json:"phase_surface_template,omitempty"`
	PhaseModeRefs                []string               `json:"phase_mode_refs,omitempty"`
	WorkflowInvestigateEligible  *bool                  `json:"workflow_investigate_eligible,omitempty"`
	AdvanceWhenGateMet           string                 `json:"advance_when_gate_met,omitempty"`
	SurfaceProfile               string                 `json:"surface_profile,omitempty"`
	// The host holds the current phase until its obligations or topology legs settle; the coordinator only answers the person meanwhile.
	PhaseHostHeld bool `json:"phase_host_held,omitempty"`
}

// CopyProjectSourceRequest Same-root copy of one file or folder (recursive). Modes are preserved.
// An occupied destination is refused.
type CopyProjectSourceRequest struct {
	// Stable mutation identity; exact retries return the original result and conflicting reuse returns 409 idempotency_conflict.
	OperationID string `json:"operation_id"`
	// Attached root both paths must stay under; omitted uses the primary root
	RootID string `json:"root_id,omitempty"`
	// Existing root-relative path to copy
	From string `json:"from"`
	// Destination root-relative path in the same root; must not already exist
	To string `json:"to"`
}

// CostBreakdown
type CostBreakdown struct {
	CacheSavings     *CacheSavings `json:"cache_savings,omitempty"`
	EstimatedNanoUsd int64         `json:"estimated_nano_usd"`
	TokenTotals      TokenTotals   `json:"token_totals"`
	TaskCount        int           `json:"task_count,omitempty"`
}

// CostEvent
type CostEvent struct {
	Scope            CostScope     `json:"scope"`
	SessionID        string        `json:"session_id,omitempty"`
	EstimatedNanoUsd int64         `json:"estimated_nano_usd"`
	TokenTotals      TokenTotals   `json:"token_totals"`
	Coordinator      CostBreakdown `json:"coordinator"`
	Workers          CostBreakdown `json:"workers"`
	// Summarizer utility calls (compaction, curate, commit draft, and similar).
	Summarizer CostBreakdown `json:"summarizer"`
}

// CostPricingProvenance
type CostPricingProvenance struct {
	// Feed id, live, local, or unknown.
	Source string `json:"source"`
	// Newest source timestamp among this source's priced events.
	PricedAt *time.Time `json:"priced_at,omitempty"`
}

// CostSummary
type CostSummary struct {
	CacheSavings *CacheSavings `json:"cache_savings,omitempty"`
	Scope        CostScope     `json:"scope"`
	SessionID    string        `json:"session_id,omitempty"`
	ProjectID    string        `json:"project_id,omitempty"`
	// Estimated nano USD over the priced usage. Read it with estimate_coverage, which says whether it is the whole estimate, a lower bound, or nothing.
	EstimatedNanoUsd int64         `json:"estimated_nano_usd"`
	TokenTotals      TokenTotals   `json:"token_totals"`
	Coordinator      CostBreakdown `json:"coordinator"`
	Workers          CostBreakdown `json:"workers"`
	// Summarizer utility calls (compaction, curate, commit draft, and similar).
	Summarizer       CostBreakdown        `json:"summarizer"`
	EstimateCoverage CostEstimateCoverage `json:"estimate_coverage"`
	// Every pricing source represented in the estimate, sorted by source id.
	PricingProvenance []CostPricingProvenance `json:"pricing_provenance"`
	// Prompt + completion tokens whose individual price was unavailable. Known buckets from the same call remain in the USD subtotal. One of the two causes of a lower_bound coverage; under unpriced coverage it is every incurred token.
	UnpricedTokens int `json:"unpriced_tokens,omitempty"`
	// Provider calls with missing usage that the host could not measure, or with an incomplete provider usage report. Calls that never reached generation are voided and never counted; calls that produced content without a usage report are measured by the host instead (see host_measured_tokens). Non-zero means the token counts are incomplete.
	UnknownCalls int `json:"unknown_calls,omitempty"`
	// The unknown_calls subset on a provider that can bill for tokens. The other cause of a lower_bound coverage; the remainder ran on local inference, where an unreported call costs tokens off the counts but no money.
	UnknownChargedCalls int `json:"unknown_charged_calls,omitempty"`
	// Prompt + completion tokens the host counted itself for calls that ended without a provider usage report, the usual cause being a user interrupt mid-stream. Included in the totals and in estimated_nano_usd, and approximate: host tokenization, and no cache attribution.
	HostMeasuredTokens int `json:"host_measured_tokens,omitempty"`
}

// CreateApprovalGrantRequest Deliberate Settings creation of one durable grant with scope project or device. Category socket_path takes socket_path; category host takes host_pattern. Execution cards and model tools cannot call this.
type CreateApprovalGrantRequest struct {
	Category ApprovalGrantCategory `json:"category"`
	Scope    ApprovalGrantScope    `json:"scope"`
	// Absolute path to an existing AF_UNIX socket on this device.
	SocketPath string `json:"socket_path,omitempty"`
	// A host or registrable-site pattern the host derived (for example `*.github.com`), optionally bound to one tunnel port (`*.github.com:443`). The host normalizes it through the same derivation an approval card uses; a pattern it would not mint is refused.
	HostPattern string `json:"host_pattern,omitempty"`
	// Required for project scope.
	ProjectID string `json:"project_id,omitempty"`
	// Optional expiry. Omitted, a project-scope grant expires 7 days after creation and a device-scope grant 30 days after.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// CreateBlueprintRequest
type CreateBlueprintRequest struct {
	Title string `json:"title"`
	// Optional project-relative path under the overlay blueprints/ directory
	Path             string `json:"path,omitempty"`
	SourceWorkflowID string `json:"source_workflow_id,omitempty"`
}

// CreateCatalogScannerRequest
type CreateCatalogScannerRequest struct {
	Source    string `json:"source"`
	CatalogID string `json:"catalog_id"`
}

// CreateComposerSecretRequest Protects selected draft bytes without storing them in composer state.
type CreateComposerSecretRequest struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose,omitempty"`
	// Protected value redacted from request capture. The floor is the length the outbound screen can recognize; the host refuses a shorter selection rather than hand back a reference that protects nothing.
	SecretValue string `json:"secret_value"`
	OperationID string `json:"operation_id"`
}

// CreateCustomScannerRequest
type CreateCustomScannerRequest struct {
	Source              string             `json:"source"`
	ID                  string             `json:"id"`
	Engine              string             `json:"engine"`
	ScopeKind           string             `json:"scope_kind"`
	Command             []string           `json:"command"`
	OutputParser        string             `json:"output_parser"`
	MapperID            string             `json:"mapper_id,omitempty"`
	Categories          []string           `json:"categories"`
	Label               string             `json:"label,omitempty"`
	Description         string             `json:"description,omitempty"`
	SkipIfBinaryMissing *bool              `json:"skip_if_binary_missing,omitempty"`
	Runtime             *ScanRuntimePolicy `json:"runtime"`
	// Env var names only (never values)
	Env []string `json:"env,omitempty"`
}

// CreateDelegationRequest
type CreateDelegationRequest struct {
	// Idempotency key. Repeating a create with the same key and body answers the delegation the first create made; the same key with a different body is an idempotency_conflict.
	OperationID string `json:"operation_id"`
	ProjectID   string `json:"project_id"`
	Task        string `json:"task"`
	// Decomposition strategy. The host uses file-based when the request omits it.
	Strategy        HuntStrategy `json:"strategy,omitempty"`
	InspectMode     InspectMode  `json:"inspect_mode,omitempty"`
	WorkflowID      string       `json:"workflow_id,omitempty"`
	WorkflowVersion string       `json:"workflow_version,omitempty"`
	WorkflowRunID   string       `json:"workflow_run_id,omitempty"`
	// Project-relative path under the overlay blueprints/ directory
	BlueprintPath string `json:"blueprint_path,omitempty"`
}

// CreateManagedSecretRequest Stores a value a person entered in project settings. This is the one managed-secret path where a credential travels in a request body: every other path either generates the bytes host-side or slices them from a document the host already holds. The response carries only the capability. Ordinary reads never return the value; authenticated reveal is the sole disclosure path.
type CreateManagedSecretRequest struct {
	Name string `json:"name"`
	// Required. A project-scoped capability outlives every chat, so the next chat can only tell what it is by reading this.
	Purpose string `json:"purpose"`
	// The credential. Never echoed back and never returned by an ordinary read. The field is named for what it holds so the host's own request-capture scrubber redacts it by name; renaming it would silently stop that. The floor is the length the screen can tell apart from ordinary text: below it the host refuses rather than mint a reference nothing would ever redact.
	SecretValue string `json:"secret_value"`
	// Optional deadline, at most one year out, after which the agent may no longer substitute this capability. Authenticated human reveal remains available for recovery. Omit for no deadline. This does not alter an external credential.
	AgentUseEndsAt string `json:"agent_use_ends_at,omitempty"`
	// Idempotency key for a retried submission.
	OperationID string `json:"operation_id"`
}

// CreateMcpProviderRequest Create overlay row. Default enabled=false. Provide source=recipe with recipe_id, or source=custom with id and url or command.
type CreateMcpProviderRequest struct {
	Source      string         `json:"source"`
	RecipeID    string         `json:"recipe_id,omitempty"`
	ID          string         `json:"id,omitempty"`
	Enabled     *bool          `json:"enabled,omitempty"`
	ToolLoading McpToolLoading `json:"tool_loading,omitempty"`
	Command     string         `json:"command,omitempty"`
	Args        []string       `json:"args,omitempty"`
	// HTTP MCP URL. A host:port without a scheme is accepted: loopback becomes http://, any other host becomes https://. Remote http:// is refused.
	URL string `json:"url,omitempty"`
	// Stdio env map (write-only; values never returned on GET).
	Env map[string]string `json:"env,omitempty"`
	// Static HTTP headers (write-only).
	Headers map[string]string `json:"headers,omitempty"`
	// Write-only static token. Placement follows credential_wire (default Authorization: Bearer).
	Token          string            `json:"token,omitempty"`
	CredentialWire McpCredentialWire `json:"credential_wire,omitempty"`
	// Header name when credential_wire is header.
	CredentialHeader string `json:"credential_header,omitempty"`
}

// CreateProjectRequest
type CreateProjectRequest struct {
	Name  *string                  `json:"name,omitempty"`
	Roots []CreateProjectRootInput `json:"roots,omitempty"`
	// Materialize as an unsaved draft until the user promotes it.
	Draft bool `json:"draft,omitempty"`
}

// CreateProjectRootInput
type CreateProjectRootInput struct {
	Path string `json:"path"`
	// Display label doubling as the @label addressing token; unique per project (case-insensitive). Blank = derived from the folder basename.
	Label     string `json:"label,omitempty"`
	IsPrimary *bool  `json:"is_primary,omitempty"`
}

// CreateProjectSourceEntryRequest In-app creation of one new, empty project file or folder. Missing parent
// folders are created along the way, so a nested path needs no separate
// call per level. A new file starts empty — its content arrives through
// PUT /source like any later edit.
type CreateProjectSourceEntryRequest struct {
	// Stable mutation identity; exact retries return the original result and conflicting reuse returns 409 idempotency_conflict.
	OperationID string `json:"operation_id"`
	// Repo-relative path (slash-separated) that must not exist yet;
	// every segment stays inside the selected root.
	Path string `json:"path"`
	// Which kind of entry to create at path
	Kind string `json:"kind"`
	// Attached root the new entry lands on; omitted uses the primary root
	RootID string `json:"root_id,omitempty"`
}

// CreateProviderRequest
type CreateProviderRequest struct {
	// Instance id the caller chooses; unique among provider instances.
	ID string `json:"id"`
	// Provider type for this instance, immutable afterward. Defaults to the instance id.
	Kind string `json:"kind,omitempty"`
	// Omit to take the kind's catalog endpoint; empty for kinds whose endpoint is derived.
	BaseURL string `json:"base_url,omitempty"`
	// Human-facing display name. Empty displays the instance id.
	Label string `json:"label,omitempty"`
	// Optional catalog hint naming a conventional environment variable (docs / UI only). Keys are saved via the credential routes.
	APIKeyEnv string `json:"api_key_env,omitempty"`
	// Whether this AI provider needs an API key. Omit to take the kind's default.
	RequiresAPIKey *bool `json:"requires_api_key,omitempty"`
	// True trusts the destination this instance resolves with detected credentials.
	SecretScreenTrusted *bool `json:"secret_screen_trusted,omitempty"`
	// Configured models. Omit so live discovery fills the list.
	Models []ProviderModelConfig `json:"models,omitempty"`
}

// CreateSessionRequest
type CreateSessionRequest struct {
	ProjectID string `json:"project_id"`
	// Active root for this session; defaults to primary when omitted
	WorkspaceRootID string         `json:"workspace_root_id,omitempty"`
	Posture         SessionPosture `json:"posture"`
	// Optional coordinator provider override for this session
	ProviderID string `json:"provider_id,omitempty"`
	// Optional coordinator model override for this session
	Model string `json:"model,omitempty"`
}

// CurrentComparisonSource A read-only, immutable snapshot of the current on-disk file. Frames and search remain bounded for files above the editing limit.
type CurrentComparisonSource struct {
	Kind     string `json:"kind"`
	RootID   string `json:"root_id"`
	Path     string `json:"path"`
	DecodeAs string `json:"decode_as,omitempty"`
}

// Delegation
type Delegation struct {
	ID              string `json:"id"`
	ProjectID       string `json:"project_id"`
	WorkspaceRootID string `json:"workspace_root_id,omitempty"`
	WorkspacePath   string `json:"workspace_path,omitempty"`
	// Coordinator session created with the delegation (for prompts and grounding)
	CoordinatorSessionID string       `json:"coordinator_session_id,omitempty"`
	Task                 string       `json:"task"`
	Strategy             HuntStrategy `json:"strategy"`
	InspectMode          InspectMode  `json:"inspect_mode,omitempty"`
	WorkflowID           string       `json:"workflow_id,omitempty"`
	WorkflowVersion      string       `json:"workflow_version,omitempty"`
	WorkflowRunID        string       `json:"workflow_run_id,omitempty"`
	// Project-relative path under the overlay blueprints/ directory
	BlueprintPath string           `json:"blueprint_path,omitempty"`
	BaseHeadSHA   string           `json:"base_head_sha,omitempty"`
	Status        DelegationStatus `json:"status"`
	Reason        string           `json:"reason,omitempty"`
	Phase         DelegationPhase  `json:"phase,omitempty"`
	Legs          []Leg            `json:"legs,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
}

// DelegationEvent
type DelegationEvent struct {
	DelegationID string           `json:"delegation_id"`
	Status       DelegationStatus `json:"status"`
	LegID        string           `json:"leg_id"`
	Phase        DelegationPhase  `json:"phase"`
}

// DelegationLegListResponse
type DelegationLegListResponse struct {
	Legs []Leg `json:"legs"`
}

// DenPerfEvent
type DenPerfEvent struct {
	// Den-side observation time (RFC 3339)
	ObservedAt string `json:"observed_at,omitempty"`
	// Debug channel (always "perf" today)
	Channel string `json:"channel,omitempty"`
	// Event name (loop-stall, longtask, …)
	Event string `json:"event"`
	// Optional fields (lag_ms, recent, dur_ms, …)
	Detail map[string]any `json:"detail,omitempty"`
}

// DenPerfEventsRequest
type DenPerfEventsRequest struct {
	Events []DenPerfEvent `json:"events"`
}

// DetectionMatch
type DetectionMatch struct {
	PackID    string `json:"pack_id"`
	RuleID    string `json:"rule_id"`
	RuleTitle string `json:"rule_title"`
	Level     string `json:"level"`
	// Stable task-bounded related-alert grouping id. Informational only; never reusable authority.
	CorrelationID string `json:"correlation_id"`
}

// DetectionPack
type DetectionPack struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	// Where the pack's bytes came from. bundled and generated ship inside the app, extension arrives with an installed extension pack, and device is a folder imported into the configuration directory.
	Source string `json:"source"`
	// Extension pack carrying this detection pack. Absent only for an imported folder, which belongs to no extension.
	ProviderPackID string `json:"provider_pack_id,omitempty"`
	// Whether this pack can be deleted from the detections catalog. A pack an extension provides is removed by uninstalling that pack.
	Removable bool `json:"removable"`
	Enabled   bool `json:"enabled"`
	// Drop-in alternatives this pack's rules also match, flattened from the pack's equivalents map. Display only.
	EquivalentBinaries []string               `json:"equivalent_binaries,omitempty"`
	Rules              []DetectionRuleSummary `json:"rules"`
	// What this pack could not load — a rule disabled in extension state, refused as a foreign contribution, or that would not parse.
	LoadWarnings []string `json:"load_warnings,omitempty"`
}

// DetectionPackImportRequest
type DetectionPackImportRequest struct {
	// Absolute path to a folder containing pack.yaml.
	Path    string `json:"path"`
	DryRun  bool   `json:"dry_run,omitempty"`
	Replace bool   `json:"replace,omitempty"`
}

// DetectionPackImportResult
type DetectionPackImportResult struct {
	DryRun        bool                        `json:"dry_run"`
	Pack          DetectionPack               `json:"pack"`
	RejectedRules []DetectionPackRejectedRule `json:"rejected_rules"`
	Ignored       []string                    `json:"ignored"`
	// Cases the folder's own fixtures.yaml declared that did not hold when run through the production adapters. Informational — the pack still imports, because a rule that misses only means silence.
	Rehearsal []string `json:"rehearsal,omitempty"`
}

// DetectionPackList
type DetectionPackList struct {
	Packs []DetectionPack `json:"packs"`
	// Rows in this project's detection-packs.yaml overlay the merge refused, keyed by pack id. Present only when the request named a project whose overlay applies; a refused row changed nothing, so without this a pack the person believes they turned off is still running.
	Rejected map[string]DetectionPackRejectedRow `json:"rejected"`
}

// DetectionPackRejectedRow
type DetectionPackRejectedRow struct {
	// The pack id the row named. Absent when the row omitted one.
	ID string `json:"id,omitempty"`
	// Why the row was refused, from a closed set shared with the scanner catalogue: project_unknown_id, project_fields_forbidden, project_unreadable, duplicate_id, invalid_entry. Clients branch on this rather than on detail.
	Code string `json:"code"`
	// What was wrong, for display beside the row.
	Detail string `json:"detail,omitempty"`
}

// DetectionRuleSummary
type DetectionRuleSummary struct {
	ID                string   `json:"id"`
	Title             string   `json:"title"`
	Description       string   `json:"description,omitempty"`
	Level             string   `json:"level"`
	Supported         bool     `json:"supported"`
	UnsupportedReason string   `json:"unsupported_reason,omitempty"`
	References        []string `json:"references,omitempty"`
	// Approval asks for this rule since the app started. Process-lifetime only; not persisted across restarts.
	RecentAsks int `json:"recent_asks,omitempty"`
}

// DismissVerifySettingsRequest
type DismissVerifySettingsRequest struct {
	Dismissed bool `json:"dismissed"`
}

// DispatchRequest
type DispatchRequest struct {
	LegID string `json:"leg_id,omitempty"`
}

// DraftVersion
type DraftVersion struct {
	VersionIndex int    `json:"version_index"`
	Body         string `json:"body"`
	// Structured reject Code for superseded attempts; empty for committed/final snapshots
	OutcomeCode string `json:"outcome_code"`
	// Host-resolved short UI label for outcome_code (hint catalog); empty when code is empty — Den may fall back to Superseded
	OutcomeLabel string    `json:"outcome_label,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// DraftVersionsResponse
type DraftVersionsResponse struct {
	Versions []DraftVersion `json:"versions"`
}

// EditorCommandHistory Exact text transition of an explicit editor command, replayable independently of later shared edits.
type EditorCommandHistory struct {
	OperationID  string `json:"operation_id"`
	Epoch        int64  `json:"epoch"`
	BeforeUpdate []byte `json:"before_update"`
	Update       []byte `json:"update"`
}

// EditorDocument Accepted collaborative document for a stable logical file in the project branch.
type EditorDocument struct {
	// Original revision reserved for this save operation when its publication already has a durable journal. The document itself is the current accepted state.
	SaveRevision        int64                 `json:"save_revision,omitempty"`
	CommandHistory      *EditorCommandHistory `json:"command_history,omitempty"`
	Epoch               int64                 `json:"epoch"`
	StateVector         []byte                `json:"state_vector"`
	CRDTUpdate          []byte                `json:"crdt_update"`
	ReplicaID           uint32                `json:"replica_id,omitempty"`
	PublishedRevision   int64                 `json:"published_revision"`
	AcceptedRevision    int64                 `json:"accepted_revision,omitempty"`
	AcceptedOperationID string                `json:"accepted_operation_id,omitempty"`
	Participants        []EditorParticipant   `json:"participants"`
	ID                  string                `json:"id"`
	ProjectID           string                `json:"project_id"`
	WorkspaceID         string                `json:"workspace_id"`
	FileID              string                `json:"file_id"`
	RootID              string                `json:"root_id"`
	Path                string                `json:"path"`
	// Normalized saved base; omitted when byte-identical to the accepted text, which crdt_update carries with line endings normalized to LF.
	BaseContent  *string        `json:"base_content,omitempty"`
	BaseSHA256   string         `json:"base_sha256"`
	Encoding     SourceEncoding `json:"encoding"`
	SizeBytes    int64          `json:"size_bytes"`
	EOL          string         `json:"eol"`
	BaseEOL      string         `json:"base_eol"`
	MixedEOL     bool           `json:"mixed_eol"`
	BaseMixedEOL bool           `json:"base_mixed_eol"`
	Revision     int64          `json:"revision"`
	Dirty        bool           `json:"dirty"`
	Diverged     bool           `json:"diverged"`
	// The path has no file on disk. The document keeps its draft and saved base; a save recreates the file, and a file that reappears merges in as an outside change.
	Absent bool `json:"absent"`
	// The retained state holding an agent edit this draft carries and disk does not, so reload and discard can say it is recoverable. Omitted once a save settles the hold.
	HeldAgentVersionID *string            `json:"held_agent_version_id,omitempty"`
	SecretScreenStatus SecretScreenStatus `json:"secret_screen_status"`
	SecretScreen       *SecretScreen      `json:"secret_screen,omitempty"`
}

// EditorDocumentChange
type EditorDocumentChange struct {
	SessionID    string `json:"session_id"`
	SessionTitle string `json:"session_title"`
	Turn         int    `json:"turn"`
	Revision     int64  `json:"revision"`
	Epoch        int64  `json:"epoch"`
	OperationID  string `json:"operation_id"`
	ActorKind    string `json:"actor_kind"`
	// Person who made a user or restore change; empty for agent changes.
	PersonID string `json:"person_id"`
	// Client the person used; empty for agent changes.
	ClientID   string    `json:"client_id"`
	RevertedBy string    `json:"reverted_by"`
	CreatedAt  time.Time `json:"created_at"`
}

// EditorDocumentChanges
type EditorDocumentChanges struct {
	Changes    []EditorDocumentChange `json:"changes"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

// EditorDocumentCommandRequest
type EditorDocumentCommandRequest struct {
	// Invoking replica's state vector. Requests an exact command transition for its document undo history.
	HistoryVector []byte `json:"history_vector,omitempty"`
	// Focused chat when this change was authored; omitted when unaffiliated. The host records the chat's turn at acceptance and drops a chat it cannot place instead of refusing the change.
	SessionID   string `json:"session_id,omitempty"`
	OperationID string `json:"operation_id"`
	// Invoking editor client.
	ClientID string `json:"client_id"`
	// Document revision the command was authored against.
	ExpectedRevision int64 `json:"expected_revision"`
}

// EditorDocumentEvent Document invalidation and presentation metadata. Reconcile through state-vector synchronization; events never transport document text.
type EditorDocumentEvent struct {
	Epoch             int64               `json:"epoch,omitempty"`
	PublishedRevision int64               `json:"published_revision,omitempty"`
	Participants      []EditorParticipant `json:"participants,omitempty"`
	ID                string              `json:"id"`
	ProjectID         string              `json:"project_id"`
	WorkspaceID       string              `json:"workspace_id"`
	FileID            string              `json:"file_id"`
	RootID            string              `json:"root_id"`
	Path              string              `json:"path"`
	BaseSHA256        string              `json:"base_sha256"`
	Encoding          SourceEncoding      `json:"encoding"`
	SizeBytes         int64               `json:"size_bytes"`
	EOL               string              `json:"eol"`
	BaseEOL           string              `json:"base_eol"`
	MixedEOL          bool                `json:"mixed_eol"`
	BaseMixedEOL      bool                `json:"base_mixed_eol"`
	Revision          int64               `json:"revision"`
	Dirty             bool                `json:"dirty"`
	Diverged          bool                `json:"diverged"`
	// The path has no file on disk. The document keeps its draft and saved base; a save recreates the file, and a file that reappears merges in as an outside change.
	Absent bool `json:"absent"`
	// The retained state holding an agent edit this draft carries and disk does not. Omitted once a save settles the hold.
	HeldAgentVersionID *string            `json:"held_agent_version_id,omitempty"`
	SecretScreenStatus SecretScreenStatus `json:"secret_screen_status"`
	// Whether the accepted text changed during this transition.
	ContentChanged bool          `json:"content_changed"`
	SecretScreen   *SecretScreen `json:"secret_screen,omitempty"`
}

// EditorDocumentStatus Accepted document metadata without text, replica admission, or filesystem reconciliation.
type EditorDocumentStatus struct {
	ID                string `json:"id"`
	FileID            string `json:"file_id"`
	RootID            string `json:"root_id"`
	Path              string `json:"path"`
	BranchID          string `json:"branch_id"`
	Revision          int64  `json:"revision"`
	Epoch             int64  `json:"epoch"`
	PublishedRevision int64  `json:"published_revision"`
	Dirty             bool   `json:"dirty"`
	Diverged          bool   `json:"diverged"`
	// The path has no file on disk. The document keeps its draft and saved base; a save recreates the file, and a file that reappears merges in as an outside change.
	Absent             bool           `json:"absent"`
	BaseSHA256         string         `json:"base_sha256"`
	SizeBytes          int64          `json:"size_bytes"`
	Encoding           SourceEncoding `json:"encoding"`
	EOL                string         `json:"eol"`
	BaseEOL            string         `json:"base_eol"`
	MixedEOL           bool           `json:"mixed_eol"`
	BaseMixedEOL       bool           `json:"base_mixed_eol"`
	HeldAgentVersionID string         `json:"held_agent_version_id,omitempty"`
}

// EditorDocumentStatuses
type EditorDocumentStatuses struct {
	Documents []EditorDocumentStatus `json:"documents"`
	Missing   []string               `json:"missing"`
}

// EditorDocumentStatusesRequest
type EditorDocumentStatusesRequest struct {
	DocumentIds []string `json:"document_ids"`
}

// EditorParticipant
type EditorParticipant struct {
	ClientID string `json:"client_id"`
	// Person working in this client. A client belongs to one person.
	PersonID string `json:"person_id"`
	// Host-assigned fallback window number, stable across documents and reconnects for this host lifetime. Desktop presentation uses its native window registry.
	WindowNumber int                   `json:"window_number,omitempty"`
	Ranges       []EditorPresenceRange `json:"ranges"`
	Main         int                   `json:"main"`
}

// EditorPresenceRange
type EditorPresenceRange struct {
	Anchor []byte `json:"anchor"`
	Head   []byte `json:"head"`
}

// EditorReplicaFrame Differential accepted replica state. Text is carried by crdt_update; base_content is included only when the requested base_sha256 differs.
type EditorReplicaFrame struct {
	Epoch               int64               `json:"epoch"`
	StateVector         []byte              `json:"state_vector"`
	CRDTUpdate          []byte              `json:"crdt_update"`
	ReplicaID           uint32              `json:"replica_id,omitempty"`
	PublishedRevision   int64               `json:"published_revision"`
	AcceptedRevision    int64               `json:"accepted_revision,omitempty"`
	AcceptedOperationID string              `json:"accepted_operation_id,omitempty"`
	Participants        []EditorParticipant `json:"participants"`
	ID                  string              `json:"id"`
	ProjectID           string              `json:"project_id"`
	WorkspaceID         string              `json:"workspace_id"`
	FileID              string              `json:"file_id"`
	RootID              string              `json:"root_id"`
	Path                string              `json:"path"`
	// Normalized saved base; omitted when the request already has this base_sha256.
	BaseContent  *string        `json:"base_content,omitempty"`
	BaseSHA256   string         `json:"base_sha256"`
	Encoding     SourceEncoding `json:"encoding"`
	SizeBytes    int64          `json:"size_bytes"`
	EOL          string         `json:"eol"`
	BaseEOL      string         `json:"base_eol"`
	MixedEOL     bool           `json:"mixed_eol"`
	BaseMixedEOL bool           `json:"base_mixed_eol"`
	Revision     int64          `json:"revision"`
	Dirty        bool           `json:"dirty"`
	Diverged     bool           `json:"diverged"`
	// The path has no file on disk. The document keeps its draft and saved base; a save recreates the file, and a file that reappears merges in as an outside change.
	Absent bool `json:"absent"`
	// The retained state holding an agent edit this draft carries and disk does not, so reload and discard can say it is recoverable. Omitted once a save settles the hold.
	HeldAgentVersionID *string            `json:"held_agent_version_id,omitempty"`
	SecretScreenStatus SecretScreenStatus `json:"secret_screen_status"`
	SecretScreen       *SecretScreen      `json:"secret_screen,omitempty"`
}

// EffectComparisonSource
type EffectComparisonSource struct {
	Kind     string `json:"kind"`
	EffectID string `json:"effect_id"`
}

// ElevatedAccessRecord
type ElevatedAccessRecord struct {
	ID        string                 `json:"id"`
	Kind      string                 `json:"kind"`
	Title     string                 `json:"title"`
	Scope     ApprovalGrantScope     `json:"scope"`
	Effects   []ElevatedAccessEffect `json:"effects"`
	ExpiresAt *time.Time             `json:"expires_at,omitempty"`
}

// ElevatedAccessRevokeResult
type ElevatedAccessRevokeResult struct {
	ID          string       `json:"id"`
	Disposition string       `json:"disposition"`
	Code        ApiErrorCode `json:"code,omitempty"`
	Message     string       `json:"message,omitempty"`
}

// ElevatedAccessSummary
type ElevatedAccessSummary struct {
	// False when Advanced turns approvals off.
	ApprovalsEnabled bool                   `json:"approvals_enabled"`
	RootSessionID    string                 `json:"root_session_id"`
	Total            int                    `json:"total"`
	Records          []ElevatedAccessRecord `json:"records"`
	SharedScopes     []ApprovalGrantScope   `json:"shared_scopes"`
}

// ErrorResponse Every API error body. `code` identifies the condition and maps to exactly one HTTP status; clients branch on it, never on `message` or the status alone. The presentation fields are rendered from the host notice catalog.
type ErrorResponse struct {
	// Machine-readable error code
	Code ApiErrorCode `json:"code"`
	// User-facing message for this occurrence (rendered server-side)
	Message string `json:"message"`
	// Structured context for the code, such as `field` for `invalid_request`, `param` for `invalid_query`, or `errors` for `workflow_validation_failed`.
	Details map[string]any `json:"details,omitempty"`
	// User-facing notice title (rendered server-side)
	Title string `json:"title,omitempty"`
	// Catalog signal that the same operation may succeed when retried.
	Retryable bool `json:"retryable,omitempty"`
	// Hint for agent or user recovery
	SuggestedAction string `json:"suggested_action,omitempty"`
	// Structured in-app destinations for the remedy; clients never infer them from copy.
	Actions []NoticeAction `json:"actions,omitempty"`
	// Host-declared tier for the underlying notice; always `non_catastrophic` on an HTTP error body, which requires a request the user was able to make.
	Tier NoticeTier `json:"tier,omitempty"`
	// Host-declared scope for the underlying notice, so a failed request surfaces where it belongs rather than wherever the user happens to be.
	Scope NoticeScope `json:"scope,omitempty"`
	// Which declared resolution produced `tier`/`scope`.
	Resolution string `json:"resolution,omitempty"`
}

// ExcludedWorkflow
type ExcludedWorkflow struct {
	ID      string                   `json:"id,omitempty"`
	Version string                   `json:"version,omitempty"`
	Path    string                   `json:"path"`
	Errors  []ComposeValidationError `json:"errors"`
}

// ExtensionConfigurationRequest
type ExtensionConfigurationRequest struct {
	// Pack id to complete replacement block; an empty block removes that pack configuration
	Packs            map[string]map[string]any `json:"packs,omitempty"`
	ExpectedRevision string                    `json:"expected_revision"`
}

// ExtensionDesiredPack
type ExtensionDesiredPack struct {
	ID     string `json:"id"`
	Source string `json:"source,omitempty"`
	// SemVer constraint for release selection
	Version string `json:"version,omitempty"`
	// Exact Git ref pin; mutually exclusive with version
	Ref string `json:"ref,omitempty"`
	// Link an author directory; disk changes contribute immediately and mark the pack for Reload from disk
	Development bool  `json:"development,omitempty"`
	Enabled     *bool `json:"enabled,omitempty"`
	// Project id that accepted this pack as a suggestion.
	InstalledFrom string `json:"installed_from,omitempty"`
}

// ExtensionDesiredState
type ExtensionDesiredState struct {
	Format   int                    `json:"format"`
	Packs    []ExtensionDesiredPack `json:"packs"`
	Disabled []string               `json:"disabled"`
	Own      map[string]string      `json:"own"`
	// Per-pack behavior values (pack id → property → value)
	Configuration map[string]map[string]any `json:"configuration,omitempty"`
	// Device-wide pack ids declined from project suggestions.
	Declined []string `json:"declined,omitempty"`
	// Merged desired state as YAML for Desired state tab
	YAML string `json:"yaml,omitempty"`
	// e.g. device vs project merge note
	ScopeNote string `json:"scope_note,omitempty"`
}

// ExtensionDiagnostic
type ExtensionDiagnostic struct {
	Code          string                      `json:"code"`
	Message       string                      `json:"message"`
	Severity      ExtensionDiagnosticSeverity `json:"severity"`
	UnitID        string                      `json:"unit_id,omitempty"`
	PackID        string                      `json:"pack_id,omitempty"`
	MCPProviderID string                      `json:"mcp_provider_id,omitempty"`
	ScannerID     string                      `json:"scanner_id,omitempty"`
}

// ExtensionInstallRequest
type ExtensionInstallRequest struct {
	// Git URL, file://, or path:…
	Source string `json:"source"`
	// SemVer constraint; defaults to * and is mutually exclusive with ref
	Version string `json:"version,omitempty"`
	// Exact Git ref pin; mutually exclusive with version
	Ref              string `json:"ref,omitempty"`
	ExpectedRevision string `json:"expected_revision"`
}

// ExtensionInstallResponse An install reports the generation it committed like every other write, plus the package identity only an install resolves.
type ExtensionInstallResponse struct {
	View             ExtensionsCatalogView `json:"view"`
	PackID           string                `json:"pack_id"`
	PackageRoot      string                `json:"package_root"`
	ResolvedRevision string                `json:"resolved_revision,omitempty"`
	Version          string                `json:"version,omitempty"`
	Warnings         []string              `json:"warnings,omitempty"`
}

// ExtensionMetaPackInstallRequest
type ExtensionMetaPackInstallRequest struct {
	Source string `json:"source"`
	// SemVer constraint; defaults to * and is mutually exclusive with ref
	Version string `json:"version,omitempty"`
	// Exact Git ref; mutually exclusive with version
	Ref              string `json:"ref,omitempty"`
	ExpectedRevision string `json:"expected_revision"`
}

// ExtensionMetaPackSummary
type ExtensionMetaPackSummary struct {
	ID            string                  `json:"id"`
	Name          string                  `json:"name"`
	Version       string                  `json:"version"`
	Kind          ExtensionPackKind       `json:"kind"`
	Status        ExtensionMetaPackStatus `json:"status"`
	Members       []string                `json:"members"`
	ConflictsWith []string                `json:"conflicts_with"`
	Extends       []string                `json:"extends"`
	Diagnostics   []ExtensionDiagnostic   `json:"diagnostics"`
	Removable     bool                    `json:"removable"`
}

// ExtensionMutationResponse The catalog generation this mutation committed, plus what only a write can report. A caller applies this and is current; it never has to read back the state it just wrote.
type ExtensionMutationResponse struct {
	View     ExtensionsCatalogView `json:"view"`
	Warnings []string              `json:"warnings,omitempty"`
}

// ExtensionPackActionResponse Update and Reload report the generation they committed, plus the package identity the re-resolve settled on.
type ExtensionPackActionResponse struct {
	View             ExtensionsCatalogView `json:"view"`
	PackID           string                `json:"pack_id"`
	ResolvedRevision string                `json:"resolved_revision,omitempty"`
	Version          string                `json:"version,omitempty"`
	Message          string                `json:"message,omitempty"`
	Warnings         []string              `json:"warnings,omitempty"`
}

// ExtensionPackSummary
type ExtensionPackSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Installed canonical SemVer from extension.yaml
	Version string `json:"version"`
	// Root constraint from extensions.yaml; absent for transitive and stock packages
	VersionConstraint string `json:"version_constraint,omitempty"`
	// Exact immutable Git revision from the scope lock
	ResolvedRevision string `json:"resolved_revision,omitempty"`
	// SHA-256 package tree integrity from the scope lock
	Integrity string `json:"integrity,omitempty"`
	// Host API constraint declared by the package
	ExtensionAPI      string `json:"extension_api"`
	InstallationState string `json:"installation_state"`
	InstallationScope string `json:"installation_scope"`
	// Direct dependency package ids mapped to requested SemVer constraints
	Dependencies  map[string]string      `json:"dependencies"`
	DependencyOf  []string               `json:"dependency_of,omitempty"`
	Source        string                 `json:"source,omitempty"`
	Ref           string                 `json:"ref,omitempty"`
	Kind          ExtensionPackKind      `json:"kind"`
	Enabled       bool                   `json:"enabled"`
	Removable     bool                   `json:"removable"`
	UnitCount     int                    `json:"unit_count"`
	HasProfile    bool                   `json:"has_profile"`
	BlockedReason ExtensionBlockedReason `json:"blocked_reason,omitempty"`
	// Scanner ids that must be enabled and runnable in Scanners Settings; never auto-enabled here
	UnmetRequiresScanners []string `json:"unmet_requires_scanners,omitempty"`
	Contributing          bool     `json:"contributing"`
	// Linked folder changed on disk; the pack still contributes current bytes until Reload from disk
	NeedsReload bool `json:"needs_reload,omitempty"`
	// Optional feature/capability label from extension.yaml
	Feature string `json:"feature,omitempty"`
	// Discovered meta-pack ids that list this pack as a member
	MetaPackIDs []string `json:"meta_pack_ids"`
	// Project id that accepted this pack as a suggestion.
	InstalledFrom string `json:"installed_from,omitempty"`
	// How many projects currently suggest this pack.
	ReferencedBy int `json:"referenced_by,omitempty"`
}

// ExtensionPackUpdateStatus
type ExtensionPackUpdateStatus struct {
	PackID            string                   `json:"pack_id"`
	Available         bool                     `json:"available"`
	CurrentVersion    string                   `json:"current_version,omitempty"`
	CandidateVersion  string                   `json:"candidate_version,omitempty"`
	CurrentRevision   string                   `json:"current_revision,omitempty"`
	CandidateRevision string                   `json:"candidate_revision,omitempty"`
	Changes           []ExtensionPackageChange `json:"changes"`
	Message           string                   `json:"message,omitempty"`
}

// ExtensionPackageChange
type ExtensionPackageChange struct {
	PackID            string `json:"pack_id"`
	CurrentVersion    string `json:"current_version,omitempty"`
	CandidateVersion  string `json:"candidate_version,omitempty"`
	CurrentRevision   string `json:"current_revision,omitempty"`
	CandidateRevision string `json:"candidate_revision,omitempty"`
	Kind              string `json:"kind"`
}

// ExtensionRevisionRequest
type ExtensionRevisionRequest struct {
	// Extension-state revision the caller last observed
	ExpectedRevision string `json:"expected_revision"`
}

// ExtensionSuggestion
type ExtensionSuggestion struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	// Version constraint from the project suggestion.
	Version string `json:"version,omitempty"`
	Ref     string `json:"ref,omitempty"`
	// Untrusted display summary of what the pack would add.
	Contributes      string                    `json:"contributes,omitempty"`
	InstalledVersion string                    `json:"installed_version,omitempty"`
	Status           ExtensionSuggestionStatus `json:"status"`
	// Suggested configuration values. Display only; never applied from the repo.
	Configuration map[string]any `json:"configuration,omitempty"`
}

// ExtensionSuggestionsAcceptRequest
type ExtensionSuggestionsAcceptRequest struct {
	PackIDs          []string `json:"pack_ids"`
	ExpectedRevision string   `json:"expected_revision"`
	// Must match the proposal returned by GET; changes are rejected before install.
	ExpectedSuggestionRevision string `json:"expected_suggestion_revision"`
}

// ExtensionSuggestionsResponse
type ExtensionSuggestionsResponse struct {
	ProjectID string `json:"project_id"`
	// Digest of the exact parsed project proposal returned in this response.
	SuggestionRevision string                `json:"suggestion_revision"`
	Suggestions        []ExtensionSuggestion `json:"suggestions"`
}

// ExtensionUnitContribution
type ExtensionUnitContribution struct {
	PackID string `json:"pack_id"`
	Path   string `json:"path"`
	// Contribution body (UTF-8 YAML/md) for inspect panes
	Content string `json:"content,omitempty"`
}

// ExtensionUnitDetail
type ExtensionUnitDetail struct {
	// Whether project scope may disable this unit under the host catalog floor.
	ProjectDisableAllowed bool                        `json:"project_disable_allowed"`
	ID                    string                      `json:"id"`
	Kind                  string                      `json:"kind"`
	Title                 string                      `json:"title,omitempty"`
	Status                ExtensionUnitStatus         `json:"status"`
	WinnerPackID          string                      `json:"winner_pack_id,omitempty"`
	Contributions         []ExtensionUnitContribution `json:"contributions"`
	// Winner body when status is loaded or owned
	Content string `json:"content,omitempty"`
}

// ExtensionUnitSummary
type ExtensionUnitSummary struct {
	// Whether project scope may disable this unit under the host catalog floor.
	ProjectDisableAllowed bool                        `json:"project_disable_allowed"`
	ID                    string                      `json:"id"`
	Kind                  string                      `json:"kind"`
	Title                 string                      `json:"title,omitempty"`
	Status                ExtensionUnitStatus         `json:"status"`
	WinnerPackID          string                      `json:"winner_pack_id,omitempty"`
	Contributions         []ExtensionUnitContribution `json:"contributions"`
}

// ExtensionsCatalogView One catalog generation, whole. Packs, the units they resolve to, the diagnostics against them, and the desired state that selected them come from one resolve, so no two fields can disagree about the same pack. Every mutation returns this too — a write reports the catalog in force exactly as a read does, which is what lets a client apply the result instead of asking again.
type ExtensionsCatalogView struct {
	// Extension-state revision. Identifies this generation, carries into the next mutation as expected_revision, and is the ETag on reads.
	Revision    string                     `json:"revision"`
	Packs       []ExtensionPackSummary     `json:"packs"`
	MetaPacks   []ExtensionMetaPackSummary `json:"meta_packs"`
	Diagnostics []ExtensionDiagnostic      `json:"diagnostics"`
	// Effective units this generation resolved
	Units   []ExtensionUnitSummary `json:"units"`
	Desired ExtensionDesiredState  `json:"desired"`
	// The intent file this generation was resolved from
	DesiredPath string `json:"desired_path"`
	Conflicts   int    `json:"conflicts,omitempty"`
	// False when any unit is in conflict
	OK bool `json:"ok"`
}

// ExternalAccess
type ExternalAccess struct {
	Modes             []ExternalAccessMode            `json:"modes"`
	VisibilitySummary ExternalAccessVisibilitySummary `json:"visibility_summary"`
	// Deduped observed mediated endpoints (host+port+transport+decision) with attempt counts. Declarations never appear here.
	Endpoints []ExternalAccessEndpoint `json:"endpoints,omitempty"`
	// Applied local-service socket grants (authority available, not observed connects).
	Sockets []ExternalAccessSocket `json:"sockets,omitempty"`
	// Declared destination strings for context only. Never treated as observed endpoints.
	DeclaredDestinations []string                  `json:"declared_destinations,omitempty"`
	Direct               *ExternalAccessDirect     `json:"direct,omitempty"`
	Detections           []ExternalAccessDetection `json:"detections,omitempty"`
}

// ExternalAccessDetection
type ExternalAccessDetection struct {
	PackID    string `json:"pack_id"`
	RuleID    string `json:"rule_id"`
	RuleTitle string `json:"rule_title,omitempty"`
	Level     string `json:"level"`
	// Exact action id the winning detection citation is bound to.
	ActionID string `json:"action_id,omitempty"`
}

// ExternalAccessDirect
type ExternalAccessDirect struct {
	Scope ExternalAccessDirectScope `json:"scope"`
	// Fixed unobserved — direct actual destinations are never observed.
	ActualDestinationVisibility ExternalAccessVisibility `json:"actual_destination_visibility"`
}

// ExternalAccessEndpoint
type ExternalAccessEndpoint struct {
	Host      string                  `json:"host"`
	Port      uint16                  `json:"port"`
	Transport ExternalAccessTransport `json:"transport"`
	// Observed endpoints always use observed.
	Visibility   ExternalAccessVisibility `json:"visibility"`
	Decision     ExternalAccessDecision   `json:"decision"`
	AttemptCount int                      `json:"attempt_count"`
	AllowedCount *int                     `json:"allowed_count,omitempty"`
	DeniedCount  *int                     `json:"denied_count,omitempty"`
}

// ExternalAccessSocket
type ExternalAccessSocket struct {
	ApprovedPath string `json:"approved_path"`
	ResolvedPath string `json:"resolved_path"`
	// Grant lifetime scope (for example chat or current_action).
	Scope string `json:"scope"`
	// Fixed observed — the host applied the capability.
	CapabilityVisibility ExternalAccessVisibility `json:"capability_visibility"`
	// Fixed unobserved — the platform sandbox supplies no connect telemetry.
	ConnectionVisibility ExternalAccessVisibility `json:"connection_visibility"`
	// Fixed unobserved — daemon/container inner effects are not observed.
	InnerEffectVisibility ExternalAccessVisibility      `json:"inner_effect_visibility"`
	Authority             ExternalAccessSocketAuthority `json:"authority"`
}

// FeedbackSubject
type FeedbackSubject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// FileBriefingEvent Revision-keyed briefing update with delta, result, or error.
type FileBriefingEvent struct {
	ProjectID    string                 `json:"project_id"`
	TargetKey    string                 `json:"target_key"`
	AttemptID    string                 `json:"attempt_id"`
	RootID       string                 `json:"root_id"`
	Path         string                 `json:"path"`
	Presentation string                 `json:"presentation"`
	SourceSHA256 string                 `json:"source_sha256"`
	Status       string                 `json:"status"`
	Preview      *FileBriefingPreview   `json:"preview,omitempty"`
	Locations    []FileBriefingLocation `json:"locations,omitempty"`
	Sections     []FileBriefingSection  `json:"sections,omitempty"`
	FallbackText string                 `json:"fallback_text"`
	Delta        string                 `json:"delta,omitempty"`
	Truncated    bool                   `json:"truncated"`
	Error        string                 `json:"error,omitempty"`
	UpdatedAt    string                 `json:"updated_at"`
}

// FileBriefingLocation Host-derived declaration location used for exact inline code navigation.
type FileBriefingLocation struct {
	Line int    `json:"line"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// FileBriefingPreview Deterministic file facts available before AI generation completes.
type FileBriefingPreview struct {
	Language  string `json:"language,omitempty"`
	LineCount int    `json:"line_count"`
}

// FileBriefingRequest Exact file revision to brief; presentation fields are mutually exclusive.
type FileBriefingRequest struct {
	RootID           string `json:"root_id"`
	Path             string `json:"path"`
	Presentation     string `json:"presentation"`
	Trigger          string `json:"trigger"`
	WorkerID         string `json:"worker_id,omitempty"`
	DocumentID       string `json:"document_id,omitempty"`
	DocumentRevision int    `json:"document_revision,omitempty"`
	// Required for version; the host validates the project, root, and path association.
	VersionID string `json:"version_id,omitempty"`
}

// FileBriefingResponse
type FileBriefingResponse struct {
	TargetKey    string              `json:"target_key"`
	AttemptID    string              `json:"attempt_id"`
	RootID       string              `json:"root_id"`
	Path         string              `json:"path"`
	Presentation string              `json:"presentation"`
	SourceSHA256 string              `json:"source_sha256"`
	Status       string              `json:"status"`
	Preview      FileBriefingPreview `json:"preview"`
	// Bounded declaration inventory supplied to the summarizer; exact unique names may navigate inline.
	Locations []FileBriefingLocation `json:"locations"`
	Sections  []FileBriefingSection  `json:"sections"`
	// Bounded terminal explanation when structured sections are unavailable.
	FallbackText string `json:"fallback_text"`
	Truncated    bool   `json:"truncated"`
	Error        string `json:"error,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

// FileBriefingSection
type FileBriefingSection struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// FileEditPreview
type FileEditPreview struct {
	Path         string            `json:"path"`
	RootID       string            `json:"root_id,omitempty"`
	Reference    FileEditReference `json:"reference"`
	Added        int               `json:"added"`
	Removed      int               `json:"removed"`
	BeforeSHA256 string            `json:"before_sha256"`
	AfterSHA256  string            `json:"after_sha256"`
	Created      bool              `json:"created"`
	Deleted      bool              `json:"deleted"`
}

// FileEditReference
type FileEditReference struct {
	MessageID  string `json:"message_id"`
	ToolCallID string `json:"tool_call_id"`
	// Zero for file_edit; one-based index for a promotion file.
	Index int `json:"index"`
}

// FileEditSnapshot
type FileEditSnapshot struct {
	// Attached project root containing the file.
	RootID string `json:"root_id,omitempty"`
	// The write removed the file; its diff has no current file target.
	Deleted bool    `json:"deleted,omitempty"`
	Path    string  `json:"path"`
	Before  *string `json:"before,omitempty"`
	After   string  `json:"after"`
}

// FileSummariesSettingsResponse
type FileSummariesSettingsResponse struct {
	// Device-wide File summaries switch. When false, briefing requests are rejected, running briefing work is canceled, and stored briefings are deleted.
	Enabled bool `json:"enabled"`
}

// Finding
type Finding struct {
	ID         int64  `json:"id,omitempty"`
	HasBody    bool   `json:"has_body,omitempty"`
	Body       string `json:"body,omitempty"`
	Agent      string `json:"agent"`
	Summary    string `json:"summary"`
	Ref        string `json:"ref,omitempty"`
	RecordedAt string `json:"recorded_at,omitempty"`
}

// FindingAbsence What the host knows about a finding no longer being reported: the coverage the scan that stopped seeing it established, and whether the scanner's execution identity has moved since. Facts, not a verdict — FindingLedgerState reads them into fixed, not_observed, or unverified.
type FindingAbsence struct {
	ObservedAt     time.Time          `json:"observed_at"`
	ScanID         string             `json:"scan_id,omitempty"`
	CoverageStatus ScanCoverageStatus `json:"coverage_status,omitempty"`
	ExecutionMoved bool               `json:"execution_moved"`
}

// FindingExportRequest
type FindingExportRequest struct {
	Format FindingExportFormat       `json:"format"`
	Query  FindingLedgerQueryRequest `json:"query,omitempty"`
}

// FindingIgnore The project's decision about one finding, as the ledger cached it from the ignore file. It never claims the finding is gone: the scanner still reports it, and the entry only says nobody intends to act.
type FindingIgnore struct {
	EntryID string `json:"entry_id"`
	Reason  string `json:"reason,omitempty"`
	// The predicates the entry decided on, so a row can point at its decision without the reader opening the file.
	MatchedOn     string                     `json:"matched_on,omitempty"`
	Justification FindingIgnoreJustification `json:"justification,omitempty"`
	// A calendar day, exclusive.
	ExpiresOn string `json:"expires_on,omitempty"`
	// True for a decision that has lapsed. The finding has already returned to the open list; this is what says why it came back.
	Expired bool `json:"expired,omitempty"`
}

// FindingIgnoreEntry One decision in a project's ignore file. Every predicate is optional and they conjoin: a finding is ignored when it matches all of the ones present. They are host-owned facts wherever the host has one, so a single entry covers every scanner that reports the same thing. An entry naming no predicate is refused.
type FindingIgnoreEntry struct {
	// Assigned by the host; stable so a client can withdraw this entry.
	ID     string `json:"id,omitempty"`
	RootID string `json:"root_id,omitempty"`
	// Repo-relative glob over the finding's primary location. A bare directory covers everything under it.
	Path string      `json:"path,omitempty"`
	Kind FindingKind `json:"kind,omitempty"`
	// Narrows the entry to one engine; empty spans all of them.
	Scanner string `json:"scanner,omitempty"`
	// Engine vocabulary, matched as a glob (`*`, `?`, `[...]`).
	Rule string `json:"rule,omitempty"`
	// Any published id of one vulnerability; matches every alias the host resolved for it.
	Advisory string `json:"advisory,omitempty"`
	// Names exactly one finding.
	Fingerprint   string                     `json:"fingerprint,omitempty"`
	Reason        string                     `json:"reason"`
	Justification FindingIgnoreJustification `json:"justification,omitempty"`
	// A calendar day (YYYY-MM-DD), exclusive.
	ExpiresOn string `json:"expires_on,omitempty"`
}

// FindingIgnoreListResponse
type FindingIgnoreListResponse struct {
	ProjectID string `json:"project_id"`
	// The project overlay file the decisions are written to.
	Path  string              `json:"path"`
	Rules []FindingIgnoreRule `json:"rules"`
}

// FindingIgnoreRule One loaded entry with the provenance a client needs to know whether it can withdraw it. Bundled entries ship with the binary and belong to no project. `id` is the host's key and is always present, so a listing and a ledger row agree about which decision they mean; an entry written by hand without an `id` of its own is keyed by its predicates and is not withdrawable through this API.
type FindingIgnoreRule struct {
	ID      string      `json:"id"`
	RootID  string      `json:"root_id,omitempty"`
	Path    string      `json:"path,omitempty"`
	Kind    FindingKind `json:"kind,omitempty"`
	Scanner string      `json:"scanner,omitempty"`
	// Engine vocabulary, matched as a glob (`*`, `?`, `[...]`).
	Rule          string                     `json:"rule,omitempty"`
	Advisory      string                     `json:"advisory,omitempty"`
	Fingerprint   string                     `json:"fingerprint,omitempty"`
	Reason        string                     `json:"reason"`
	Justification FindingIgnoreJustification `json:"justification,omitempty"`
	ExpiresOn     string                     `json:"expires_on,omitempty"`
	Source        string                     `json:"source"`
	Expired       bool                       `json:"expired,omitempty"`
	// Whether this API can remove the entry. False for bundled entries and for a hand-written one with no id; those are edited in the file.
	Withdrawable bool `json:"withdrawable"`
	// How many of the project's ledger rows the entry covers now.
	Matches int `json:"matches"`
}

// FindingLedgerCounts The project's whole ledger by state, independent of a query's filters, so a filtered page never reads as the project total. The states partition the ledger exactly: every row is counted once. Fixed, not_observed, and unverified stay separate: a bounded pass is not a fix, and folding them together is how a scan gap becomes a clean bill.
type FindingLedgerCounts struct {
	Open        int `json:"open"`
	Reopened    int `json:"reopened"`
	Fixed       int `json:"fixed"`
	NotObserved int `json:"not_observed"`
	Unverified  int `json:"unverified"`
	Ignored     int `json:"ignored"`
}

// FindingLedgerEntry One finding as the project holds it now.
type FindingLedgerEntry struct {
	Finding     SecurityFinding    `json:"finding"`
	State       FindingLedgerState `json:"state"`
	ScannerID   string             `json:"scanner_id"`
	FirstSeenAt time.Time          `json:"first_seen_at"`
	// The newest observation that still stands behind the row: for an open finding, the completion of the scan whose authoritative set holds it; for one that left, the last time a scanner reported it present.
	LastSeenAt   time.Time `json:"last_seen_at"`
	Observations int       `json:"observations"`
	// The run that recorded the row's last event, for drill-down.
	LastScanID string          `json:"last_scan_id,omitempty"`
	Absence    *FindingAbsence `json:"absence,omitempty"`
	Ignore     *FindingIgnore  `json:"ignore,omitempty"`
}

// FindingLedgerQueryRequest
type FindingLedgerQueryRequest struct {
	// Case-insensitive substring over the finding message, rule id, location uri, and advisory ids. A filter over stored fields, not a ranking or a classifier.
	Text       string               `json:"text,omitempty"`
	Levels     []FindingLevel       `json:"levels,omitempty"`
	States     []FindingLedgerState `json:"states,omitempty"`
	ScannerIDs []string             `json:"scanner_ids,omitempty"`
	Kind       string               `json:"kind,omitempty"`
	RuleID     string               `json:"rule_id,omitempty"`
	// Location uri prefix.
	Path        string `json:"path,omitempty"`
	AdvisoryID  string `json:"advisory_id,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	// Hint code (properties.lycaon.hint_code).
	Code              string            `json:"code,omitempty"`
	IntroducedSinceAt *time.Time        `json:"introduced_since_at,omitempty"`
	Sort              FindingLedgerSort `json:"sort,omitempty"`
	Order             string            `json:"order,omitempty"`
	// Opaque pagination cursor.
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// FindingLedgerResponse
type FindingLedgerResponse struct {
	ProjectID string               `json:"project_id"`
	Entries   []FindingLedgerEntry `json:"entries"`
	Counts    FindingLedgerCounts  `json:"counts"`
	// The whole ledger counted by normalized level, every level present including the ones at zero.
	ByLevel    map[string]int `json:"by_level"`
	TotalMatch int            `json:"total_match"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// FindingsDigest
type FindingsDigest struct {
	Findings []Finding `json:"findings"`
	Revision uint64    `json:"revision"`
}

// FindingsEvent
type FindingsEvent struct {
	Revision uint64 `json:"revision"`
}

// FireWorkflowTransitionRequest
type FireWorkflowTransitionRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

// FolderDetect
type FolderDetect struct {
	Path string `json:"path"`
	// The project that already holds this folder as a root, when there is one. Absent for a folder no project covers. Opening a folder that carries this reopens that project; minting a second one over the same tree would lose its trust state.
	ProjectID string `json:"project_id,omitempty"`
	// Install choices available before opening the project.
	ExtensionSuggestions *ExtensionSuggestionsResponse `json:"extension_suggestions,omitempty"`
}

// FullScanRequest A complete pass of every admitted file by the named scanners, or by every selected scanner when none are named. The only way a full pass starts; automatic scanning is incremental.
type FullScanRequest struct {
	Kind       string   `json:"kind,omitempty"`
	ScannerIDs []string `json:"scanner_ids,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
}

// FullScanResponse The pass each project root owes, as it stands after the request, and the Security stage overview that already includes it. A request joins an unfinished pass that covers its scanners, widens one that has not started, and otherwise records a new pass.
type FullScanResponse struct {
	Passes   []SecurityFullPass `json:"passes"`
	Overview SecurityOverview   `json:"overview"`
}

// GitBranchEntry
type GitBranchEntry struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
}

// GitBranchesView
type GitBranchesView struct {
	Branches []GitBranchEntry `json:"branches"`
}

// GitChangeComparisonSource
type GitChangeComparisonSource struct {
	Kind     string `json:"kind"`
	ChangeID string `json:"change_id"`
	Path     string `json:"path"`
	Movement *bool  `json:"movement,omitempty"`
	Parent   *int   `json:"parent,omitempty"`
}

// GitChangesPage
type GitChangesPage struct {
	RepoID   string `json:"repo_id"`
	Revision uint64 `json:"revision"`
	// True while a newer generation is loading behind this page. The page still answers for its own revision.
	Refreshing bool           `json:"refreshing"`
	Files      []GitFileEntry `json:"files"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// GitCheckoutRequest
type GitCheckoutRequest struct {
	Branch string `json:"branch"`
	// Create the branch (checkout -b) before switching.
	Create bool `json:"create,omitempty"`
}

// GitCommitMessageResponse
type GitCommitMessageResponse struct {
	Message string `json:"message"`
}

// GitCommitRequest
type GitCommitRequest struct {
	Message string `json:"message"`
	// Paths to stage; all changes are staged when omitted.
	Paths []string `json:"paths,omitempty"`
}

// GitFileEntry
type GitFileEntry struct {
	// Repo-toplevel-relative path of the changed file (exactly what porcelain printed).
	Path string `json:"path"`
	// Two-character porcelain XY status code.
	Status string `json:"status"`
	// Project root that contains this path; empty when the path falls under no project root.
	RootID string `json:"root_id"`
	// Path relative to that project root; empty when root_id is empty.
	RootRelativePath string `json:"root_relative_path"`
}

// GitInitRequest
type GitInitRequest struct {
	// Project root in which to initialize a repository.
	RootID string `json:"root_id"`
}

// GitMutationResult
type GitMutationResult struct {
	RepoID     string `json:"repo_id"`
	Revision   uint64 `json:"revision"`
	Refreshing bool   `json:"refreshing"`
}

// GitRangeComparisonSource One file across two commits of a root's repository, read from the
// object store. An added or deleted file reads its absent side as absent.
type GitRangeComparisonSource struct {
	Kind   string `json:"kind"`
	RootID string `json:"root_id"`
	// Full commit id; empty compares with the empty tree.
	BeforeCommit string `json:"before_commit"`
	// Full commit id.
	AfterCommit string `json:"after_commit"`
	Path        string `json:"path"`
}

// GitRepoEntry
type GitRepoEntry struct {
	// Derived repository id; omitted when available is false.
	RepoID string `json:"repo_id,omitempty"`
	Label  string `json:"label"`
	// Project root ids this repository contains, in project-roots order.
	RootIDs []string `json:"root_ids"`
	// False for a root inside no repository.
	Available     bool   `json:"available"`
	Branch        string `json:"branch,omitempty"`
	HeadShort     string `json:"head_short,omitempty"`
	Upstream      string `json:"upstream,omitempty"`
	Ahead         int    `json:"ahead"`
	Behind        int    `json:"behind"`
	Dirty         bool   `json:"dirty"`
	StagedCount   int    `json:"staged_count"`
	UnstagedCount int    `json:"unstaged_count"`
	ChangedCount  int    `json:"changed_count"`
	// True while this repository's first status snapshot is loading; count and branch fields are not meaningful until it is false.
	StatusPending bool `json:"status_pending,omitempty"`
}

// GitReposView
type GitReposView struct {
	Repos []GitRepoEntry `json:"repos"`
	// Repository id for the session's active root when available; omitted when the set has no available repository.
	ActiveRepoID string `json:"active_repo_id,omitempty"`
}

// GitStashRequest
type GitStashRequest struct {
	Message string `json:"message,omitempty"`
}

// GitStatusSummary
type GitStatusSummary struct {
	Available bool     `json:"available"`
	RepoID    string   `json:"repo_id"`
	RootIDs   []string `json:"root_ids"`
	// The published status generation these values come from; zero while the first snapshot loads.
	Revision uint64 `json:"revision"`
	// True while a newer generation is loading behind these values. The values remain usable.
	Refreshing    bool   `json:"refreshing"`
	Branch        string `json:"branch,omitempty"`
	HeadShort     string `json:"head_short,omitempty"`
	Upstream      string `json:"upstream,omitempty"`
	Ahead         int    `json:"ahead"`
	Behind        int    `json:"behind"`
	Dirty         bool   `json:"dirty"`
	StagedCount   int    `json:"staged_count"`
	UnstagedCount int    `json:"unstaged_count"`
	ChangedCount  int    `json:"changed_count"`
}

// GitWorktreeBindRequest Creates a worktree for a chat in one project repository.
type GitWorktreeBindRequest struct {
	// Repository to bind; must be available in the project's repository set.
	RepoID string `json:"repo_id"`
	// New branch the worktree checks out.
	Branch string `json:"branch"`
}

// GitWorktreeLandResult Outcome of landing a session branch into the recorded base branch in the project folder. landed false with reason nothing_to_land is success with nothing to do; other reasons are refusals.
type GitWorktreeLandResult struct {
	Landed     bool   `json:"landed"`
	BaseBranch string `json:"base_branch"`
	// Commits merged; zero when landed is false.
	Commits int `json:"commits"`
	// Empty on success. Otherwise one of nothing_to_land, base_missing, base_dirty, base_on_other_branch, base_branch_missing, detached_head, conflict.
	Reason string `json:"reason"`
	// Repo-toplevel-relative conflict paths when reason is conflict.
	Conflicts []string `json:"conflicts"`
}

// GitWorktreeView One chat's worktree binding. When bound is false, every other field is absent or zero except session_id. State is absent when unbound.
type GitWorktreeView struct {
	// False means the chat has no worktree.
	Bound     bool   `json:"bound"`
	SessionID string `json:"session_id"`
	// The bound repository's derived id.
	RepoID string `json:"repo_id,omitempty"`
	// The repository's label, for copy.
	Label string `json:"label,omitempty"`
	// The session branch checked out in the worktree.
	Branch string `json:"branch,omitempty"`
	// The repository branch recorded at bind time; Land always targets this branch.
	BaseBranch string `json:"base_branch,omitempty"`
	// Absolute worktree path — shown in the stale state and nowhere else.
	Path string `json:"path,omitempty"`
	// Present only when bound: ready when the worktree directory exists; stale when the binding's path is missing.
	State string `json:"state,omitempty"`
	// The worktree has uncommitted changes.
	Dirty bool `json:"dirty"`
	// Commits on the session branch not in base_branch.
	AheadOfBase int `json:"ahead_of_base"`
	// Commits on base_branch not in the session branch.
	BehindBase int `json:"behind_base"`
	// Preflight reason Land can surface even when ahead counts are unavailable.
	LandBlockedReason string `json:"land_blocked_reason,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
}

// GroundingEvent
type GroundingEvent struct {
	SessionID    string `json:"session_id"`
	Code         string `json:"code"`
	LegID        string `json:"leg_id,omitempty"`
	DelegationID string `json:"delegation_id,omitempty"`
	Escalated    bool   `json:"escalated,omitempty"`
}

// HealthResponse
type HealthResponse struct {
	// ok when the store is open and the server is serving; recovery when this build must not open the store and only restore routes are available.
	Status string `json:"status"`
	// Host/desktop semver (sidecar binary).
	Version string `json:"version"`
	// Monotonic sidecar boot counter; Den reconciles cache when this changes.
	StoreRevision int64 `json:"store_revision"`
	// SQLite baseline marker used by this binary.
	SchemaVersion int `json:"schema_version"`
	// Optional semver the sidecar expects from Den; omit or empty skips skew UX.
	MinDenVersion string `json:"min_den_version,omitempty"`
	// App version that last opened this store before the current boot. Omitted when unchanged (ordinary boot).
	PreviousAppVersion string `json:"previous_app_version,omitempty"`
	// PRAGMA user_version read from the live store when status is recovery. Omitted on ordinary health.
	StoreSchemaVersion int `json:"store_schema_version,omitempty"`
	// Closed reason the store cannot be served. Present when status is recovery.
	RecoveryReason string `json:"recovery_reason,omitempty"`
	// Human-readable sentence matching the recovery reason. Present when status is recovery.
	RecoveryDetail string `json:"recovery_detail,omitempty"`
	// Whether a local recovery snapshot has valid metadata and a supported schema upgrade route. Restore verifies all payload hashes before use. Always false on ordinary health.
	RecoverySnapshotAvailable bool `json:"recovery_snapshot_available"`
	// RFC 3339 UTC creation time of the recovery snapshot when available. Omitted when recovery_snapshot_available is false.
	RecoverySnapshotAt time.Time `json:"recovery_snapshot_at,omitempty"`
}

// HistoryProtection
type HistoryProtection struct {
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	Protected bool   `json:"protected"`
}

// HistoryPruneCandidate
type HistoryPruneCandidate struct {
	ID               string    `json:"id"`
	Class            string    `json:"class"`
	ProjectID        string    `json:"project_id"`
	CreatedAt        time.Time `json:"created_at"`
	ReclaimableBytes int64     `json:"reclaimable_bytes"`
}

// HistoryPruneResult
type HistoryPruneResult struct {
	RemovedCount int64 `json:"removed_count"`
	// Encoded body bytes whose retained references were released; physical unlink and page reclamation may finish later.
	ReleasedBytes int64  `json:"released_bytes"`
	Complete      bool   `json:"complete"`
	PreviewToken  string `json:"preview_token"`
}

// HistoryRetentionPolicy
type HistoryRetentionPolicy struct {
	Version         int                  `json:"version"`
	Revision        int64                `json:"revision"`
	Suspended       bool                 `json:"suspended"`
	Recordings      HistoryRetentionRule `json:"recordings"`
	Checkpoints     HistoryRetentionRule `json:"checkpoints"`
	SourceRevisions HistoryRetentionRule `json:"source_revisions"`
	ScanDetail      HistoryRetentionRule `json:"scan_detail"`
	ReceiptDetail   HistoryRetentionRule `json:"receipt_detail"`
}

// HistoryRetentionPreview
type HistoryRetentionPreview struct {
	Token                string                  `json:"token"`
	PolicyRevision       int64                   `json:"policy_revision"`
	Candidates           []HistoryPruneCandidate `json:"candidates"`
	EligibleCount        int64                   `json:"eligible_count"`
	ReclaimableBytes     int64                   `json:"reclaimable_bytes"`
	SharedProtectedBytes int64                   `json:"shared_protected_bytes"`
	Complete             bool                    `json:"complete"`
}

// HistoryRetentionRequest
type HistoryRetentionRequest struct {
	Policy       HistoryRetentionPolicy `json:"policy,omitempty"`
	PreviewToken string                 `json:"preview_token,omitempty"`
}

// HistoryRetentionRule
type HistoryRetentionRule struct {
	Mode       string `json:"mode"`
	MaxAgeDays int64  `json:"max_age_days,omitempty"`
	MaxBytes   int64  `json:"max_bytes,omitempty"`
}

// HistoryStorageLane
type HistoryStorageLane struct {
	ID string `json:"id"`
	// Retained file bytes, deduplicated within the lane. This is not exclusive filesystem allocation. The upgrade-recovery lane reports zero here because its independent snapshots may share physical extents with live files.
	StoredBytes int64 `json:"stored_bytes"`
	// Uncompressed content bytes where available. For upgrade-recovery, the sum of retained snapshot file lengths, which may share physical disk space and must not be added to a physical storage total.
	LogicalBytes int64 `json:"logical_bytes"`
	SharedBytes  int64 `json:"shared_bytes"`
}

// HistoryStorageStatus
type HistoryStorageStatus struct {
	Policy      HistoryRetentionPolicy `json:"policy"`
	Lanes       []HistoryStorageLane   `json:"lanes"`
	Protections []HistoryProtection    `json:"protections"`
}

// HostInfo The host a client is connected to, the contract it serves, and who the client is on it. A client reads this before relying on any other route.
type HostInfo struct {
	// Stable identity of this install, derived from its public key. It survives restarts and store resets, and changes only when the host key is replaced.
	HostID string `json:"host_id"`
	// Ed25519 public key of this host, standard base64 of the 32 key bytes.
	HostPublicKey string `json:"host_public_key"`
	// Host product semver.
	ProductVersion string `json:"product_version"`
	// Host API contract version (MAJOR.MINOR.PATCH). A client built for a different major version must not use the host.
	ContractVersion string `json:"contract_version"`
	Caller          Person `json:"caller"`
	// Facilities the host offers this client, sorted and unique.
	Capabilities []HostCapability `json:"capabilities"`
}

// HostResource
type HostResource struct {
	ID            string                         `json:"id"`
	Family        string                         `json:"family"`
	Label         string                         `json:"label"`
	Category      string                         `json:"category"`
	Description   string                         `json:"description"`
	DocsURL       string                         `json:"docs_url,omitempty"`
	Origin        string                         `json:"origin"`
	Status        HostResourceStatus             `json:"status"`
	HostSupport   HostResourceHostSupport        `json:"host_support"`
	Access        HostResourceAccess             `json:"access"`
	AccessSetting HostResourceAccessSetting      `json:"access_setting"`
	Prompt        HostResourcePromptMode         `json:"prompt"`
	Reason        string                         `json:"reason,omitempty"`
	Surfaces      []HostResourceExecutionSurface `json:"surfaces"`
	Connections   []HostResourceConnection       `json:"connections"`
	CheckedAt     string                         `json:"checked_at"`
}

// HostResourceConnection
type HostResourceConnection struct {
	Mode      HostResourceConnectionMode        `json:"mode"`
	Transport HostResourceLocalServiceTransport `json:"transport,omitempty"`
	Target    string                            `json:"target,omitempty"`
}

// HostResourceDiagnostic
type HostResourceDiagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// HostResourcesResponse
type HostResourcesResponse struct {
	Version         int                      `json:"version"`
	Resources       []HostResource           `json:"resources"`
	Diagnostics     []HostResourceDiagnostic `json:"diagnostics"`
	UserCatalogPath string                   `json:"user_catalog_path"`
	CheckedAt       string                   `json:"checked_at"`
	Fingerprint     string                   `json:"fingerprint"`
}

// HostSecretRedactionMeta Host-recorded provenance for spans replaced in this message copy. Presence is authoritative; literal marker text in content is not a redaction signal. Every replacement in this copy is listed, so a client renders from the list rather than by searching content, and the list describes this copy alone rather than accumulating across projections of one message.
type HostSecretRedactionMeta struct {
	Spans []RedactedSpan `json:"spans"`
}

// IndexWarmingMeta
type IndexWarmingMeta struct {
	Trigger    string   `json:"trigger"`
	Tier       string   `json:"tier,omitempty"`
	Topic      string   `json:"topic,omitempty"`
	Hosts      []string `json:"hosts,omitempty"`
	Pages      int      `json:"pages,omitempty"`
	DurationMs int64    `json:"duration_ms,omitempty"`
	SkipReason string   `json:"skip_reason,omitempty"`
}

// InvocationEvidence
type InvocationEvidence struct {
	Kind     string `json:"kind"`
	Ref      string `json:"ref,omitempty"`
	OwnerRef string `json:"owner_ref,omitempty"`
}

// InvocationFailure Typed terminal outcome for any invocation that did not complete.
type InvocationFailure struct {
	Code      string `json:"code"`
	Class     string `json:"class"`
	Retryable bool   `json:"retryable"`
	OwnerRef  string `json:"owner_ref,omitempty"`
	// Subsystem-owner facts explaining the failure.
	Details map[string]any `json:"details,omitempty"`
}

// InvocationIsolation Typed isolation outcome from the generic invocation boundary. It does not replace the selected subsystem owner and may accompany either a completed attempted effect or a pre-owner rejection.
type InvocationIsolation struct {
	Code        string `json:"code"`
	Disposition string `json:"disposition"`
}

// InvocationReceipt
type InvocationReceipt struct {
	ID             string               `json:"id"`
	Tool           string               `json:"tool"`
	ToolCallID     string               `json:"tool_call_id"`
	ContractDigest string               `json:"contract_digest"`
	ArgsDigest     string               `json:"args_digest"`
	Owner          string               `json:"owner"`
	Lifecycle      string               `json:"lifecycle"`
	Reversibility  string               `json:"reversibility"`
	EvidencePolicy string               `json:"evidence_policy"`
	RecoveryPolicy string               `json:"recovery_policy"`
	Status         InvocationStatus     `json:"status"`
	Invoked        bool                 `json:"invoked"`
	Evidence       InvocationEvidence   `json:"evidence"`
	Failure        *InvocationFailure   `json:"failure,omitempty"`
	Isolation      *InvocationIsolation `json:"isolation,omitempty"`
	// Host worktree generation after this invocation settled. Verification evidence applies only while this exact generation remains current.
	SourceRevision string `json:"source_revision,omitempty"`
	// SHA-256 identity of the workspace root, without exposing its local path.
	SourceRootDigest string `json:"source_root_digest,omitempty"`
	// Terminal verdict stated by a command or verify subsystem owner, bound to source_revision. Absent for every other tool and for a launch that had not exited. This is the fact that discharges a source verification obligation; the result body never is.
	SourceVerdict string     `json:"source_verdict,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	SettledAt     *time.Time `json:"settled_at,omitempty"`
}

// InvocationReceiptList
type InvocationReceiptList struct {
	Invocations []InvocationReceipt `json:"invocations"`
	NextCursor  string              `json:"next_cursor,omitempty"`
}

// LLMCallEvent
type LLMCallEvent struct {
	CallID string `json:"call_id"`
	// Session whose coordinator turn this call belongs to. The event stream is project-scoped, so clients must attribute activity by this id rather than by whichever session they currently display.
	SessionID       string                   `json:"session_id"`
	Provider        string                   `json:"provider"`
	Model           string                   `json:"model"`
	Status          LLMCallStatus            `json:"status"`
	Tokens          LLMTokenCounts           `json:"tokens"`
	CoordinatorLoop *CoordinatorLoopProgress `json:"coordinator_loop,omitempty"`
	// 1-based index of the attempt now running. Present only while the call is being retried, so a rate-limited provider is distinguishable from a slow one; the first attempt omits it. Republished on the same call_id with status active.
	Attempt int `json:"attempt,omitempty"`
	// The initial request plus every retry the provider policy allows.
	MaxAttempts int `json:"max_attempts,omitempty"`
	// Backoff slept before this attempt, in milliseconds.
	RetryWaitMs int64          `json:"retry_wait_ms,omitempty"`
	RetryReason LLMRetryReason `json:"retry_reason,omitempty"`
	// How long the previous attempt went unanswered after its request was delivered, in milliseconds. Present only with retry_reason silent. This is the number that separates a slow model from a stalled one: both look identical while a turn is open, and only this says which.
	SilenceMs int64 `json:"silence_ms,omitempty"`
}

// LLMTokenCounts
type LLMTokenCounts struct {
	Prompt              int `json:"prompt,omitempty"`
	Completion          int `json:"completion,omitempty"`
	Total               int `json:"total,omitempty"`
	ContextWindow       int `json:"context_window,omitempty"`
	CompactionThreshold int `json:"compaction_threshold,omitempty"`
}

// LaunchBlueprintRequest
type LaunchBlueprintRequest struct {
	TargetWorkflowID string `json:"target_workflow_id,omitempty"`
	// When true, copy the source blueprint into a fresh draft and open a session without starting the workflow run. Den arms the catalog recipe; the next composer send starts the run bound to the seed.
	DeferStart bool `json:"defer_start,omitempty"`
}

// LaunchBlueprintResponse
type LaunchBlueprintResponse struct {
	SessionID string `json:"session_id"`
	// Omitted when defer_start seeded without starting a run.
	WorkflowRunID string `json:"workflow_run_id,omitempty"`
	// UUID of the seeded blueprint copy.
	BlueprintID string `json:"blueprint_id"`
}

// LeaveEditorDocumentRequest
type LeaveEditorDocumentRequest struct {
	ClientID    string `json:"client_id"`
	Incarnation string `json:"incarnation"`
}

// LocalDataBucketStatus
type LocalDataBucketStatus struct {
	ID      LocalDataBucketId `json:"id"`
	Present bool              `json:"present"`
	// Best-effort on-disk size; may be omitted when unknown
	Bytes int64 `json:"bytes,omitempty"`
}

// LocalDataClearRequest
type LocalDataClearRequest struct {
	Buckets           []LocalDataBucketId `json:"buckets,omitempty"`
	WorkspaceCacheIDs []string            `json:"workspace_cache_ids,omitempty"`
}

// LocalDataClearResponse
type LocalDataClearResponse struct {
	Results               []LocalDataClearResult      `json:"results"`
	WorkspaceCacheResults []WorkspaceCacheClearResult `json:"workspace_cache_results"`
}

// LocalDataClearResult
type LocalDataClearResult struct {
	ID   LocalDataBucketId `json:"id"`
	OK   bool              `json:"ok"`
	Code ApiErrorCode      `json:"code,omitempty"`
	// Host copy when ok is false; branch on code.
	Message string `json:"message,omitempty"`
}

// LocalDataStatus
type LocalDataStatus struct {
	Buckets         []LocalDataBucketStatus `json:"buckets"`
	WorkspaceCaches []WorkspaceCacheStatus  `json:"workspace_caches"`
}

// LockVaultRequest
type LockVaultRequest struct {
	Reason string `json:"reason"`
}

// LockVaultResponse
type LockVaultResponse struct {
	// Chats whose unlock ended.
	Locked int `json:"locked"`
}

// LogDigest
type LogDigest struct {
	Format      LogFormat            `json:"format"`
	RecordCount int                  `json:"record_count"`
	ParsedCount int                  `json:"parsed_count"`
	TimeSpan    *LogDigestTimeSpan   `json:"time_span,omitempty"`
	Fields      []LogDigestFieldStat `json:"fields,omitempty"`
	Facets      []LogDigestFacet     `json:"facets,omitempty"`
	Clusters    []LogDigestCluster   `json:"clusters,omitempty"`
	Truncated   bool                 `json:"truncated"`
}

// LogDigestCluster
type LogDigestCluster struct {
	Template  string `json:"template"`
	Count     int    `json:"count"`
	Severity  string `json:"severity,omitempty"`
	FirstLine int    `json:"first_line"`
	LastLine  int    `json:"last_line"`
}

// LogDigestFacet
type LogDigestFacet struct {
	Key    string                `json:"key"`
	Values []LogDigestFacetValue `json:"values"`
}

// LogDigestFacetValue
type LogDigestFacetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// LogDigestFieldStat
type LogDigestFieldStat struct {
	Key      string `json:"key"`
	Coverage int    `json:"coverage_pct"`
}

// LogDigestTimeSpan
type LogDigestTimeSpan struct {
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
}

// MakeProjectSourceEditableRequest Human request to add owner-write permission to the exact file bytes shown in the editor.
type MakeProjectSourceEditableRequest struct {
	// Root-relative path of the existing regular file.
	Path string `json:"path"`
	// Attached root that contains the file.
	RootID string `json:"root_id"`
	// SHA-256 from the matching source read; stale bytes are refused with 409 source_write_conflict.
	BaseSHA256 string `json:"base_sha256"`
}

// MakeProjectSourceEditableResponse Completed owner-write permission transition.
type MakeProjectSourceEditableResponse struct {
	Path   string `json:"path"`
	RootID string `json:"root_id"`
	// Permission bits before the transition.
	PreviousMode uint32 `json:"previous_mode"`
	// Permission bits after adding owner-write.
	Mode uint32 `json:"mode"`
	// True when the resulting mode grants owner-write.
	Writable bool `json:"writable"`
}

// ManagedApprovalRule
type ManagedApprovalRule struct {
	Category ApprovalCategory  `json:"category"`
	Pattern  string            `json:"pattern"`
	Effect   ApprovalEffect    `json:"effect"`
	UnitID   string            `json:"unit_id"`
	PackID   string            `json:"pack_id"`
	Scope    ApprovalRuleScope `json:"scope"`
}

// ManagedSecret Metadata for protected material. Ordinary reads are value-free. A person using the installed desktop app may reveal the current value through fresh, challenge-bound operating-system authentication.
type ManagedSecret struct {
	// Provider-safe token accepted by supported outbound agent tools.
	Reference string `json:"reference"`
	// The chat whose agents may spend chat-scoped material; absent for project scope. Scope decides who may spend a capability, never how long it lives: only an explicit revoke ends one.
	ChatSessionID *string `json:"chat_session_id,omitempty"`
	// Title of the owning chat while it exists and has one.
	ChatTitle string `json:"chat_title,omitempty"`
	// True when the owning chat was deleted. The capability stays active for people to reveal, replace, promote, or revoke; no agent can spend it, because no chat can see it.
	ChatDeleted bool   `json:"chat_deleted,omitempty"`
	Name        string `json:"name"`
	Purpose     string `json:"purpose,omitempty"`
	Scope       string `json:"scope"`
	// How the capability was created. This is immutable provenance, not a claim about where the current stored value lives or whether an external credential still matches it. `file_marked` means a person marked a value in a project file; `composer_marked` means a person protected selected draft text for a chat; `settings_entered` means a person typed a value into project settings. File marks and settings entries are project acts; composer marks initially take chat scope. `cookie_jar` means the host stored the cookies an agent's own `http_request` calls received under a named jar; every cookie value in it is screened like any other managed value. `token_jar` is the same for tokens `http_request` captured from responses.
	Origin string `json:"origin"`
	// Who supplied the current value, recorded in the encrypted vault when the bytes entered. `person` means a person gave the value to Painted Wolf Code, which may hold the only copy: revealing it or handing it to any file, process, service, or MCP server needs that person's verified presence on this device. `file` means a person marked bytes already in a project file, which governs them. `chat` means the host generated the value for one chat, which alone has held it. `host` means the host generated or captured the value for the agent's work beyond one chat. Absent when no value is readable.
	Custody string `json:"custody,omitempty"`
	// Present only for generated material.
	Format string `json:"format,omitempty"`
	// Present only for generated material.
	EntropyBits int64  `json:"entropy_bits,omitempty"`
	CreatedAt   string `json:"created_at"`
	// After this time the agent may no longer substitute the capability. Authenticated human reveal remains available for recovery. The protected bytes remain available for screening, and this deadline does not alter an external credential or a project file.
	AgentUseEndsAt *string `json:"agent_use_ends_at,omitempty"`
	// `revoked` is terminal and no administration reaches it. `agent_use_expired` means its agent-use deadline passed; a new deadline revives it. `unavailable` has metadata but no readable bytes, and supplying a value again restores it under the same reference.
	State string `json:"state"`
	// Counts stored values, not edits. Starts at 1 and rises with each stored-value replacement while the reference stays the same. Zero means no readable value was ever recorded.
	Version int64 `json:"version"`
	// When the current protected value replaced its predecessor; absent before the first replacement.
	ValueReplacedAt *string `json:"value_replaced_at,omitempty"`
	// Most recent substitution attempt, refusals included; absent when the reference has never reached a tool call.
	LastUsedAt *string `json:"last_used_at,omitempty"`
	// Substitution attempts inside the bounded history window, refusals included.
	UseCount int64 `json:"use_count"`
	// Most recent successful authenticated human reveal; absent when never revealed.
	LastRevealedAt *string `json:"last_revealed_at,omitempty"`
	// Successful authenticated human reveals retained for this capability.
	RevealCount int64 `json:"reveal_count"`
}

// ManagedSecretList
type ManagedSecretList struct {
	Secrets []ManagedSecret `json:"secrets"`
}

// ManagedSecretRevealChallenge One short-lived request for native user-presence authentication. The proof payload is opaque canonical bytes; the native shell signs it only after the operating system verifies the device user.
type ManagedSecretRevealChallenge struct {
	ChallengeID string `json:"challenge_id"`
	// Base64url without padding of the exact bytes the native shell signs.
	ProofPayload string `json:"proof_payload"`
	// Host-authored reason shown by the operating-system authentication prompt.
	Prompt    string `json:"prompt"`
	Version   int64  `json:"version"`
	ExpiresAt string `json:"expires_at"`
}

// ManagedSecretRevealResponse The current value after successful operating-system authentication. The response body is categorically withheld from debug capture. Den does not persist the value, emit it as an event, or make it available to agent tools.
type ManagedSecretRevealResponse struct {
	SecretValue   string `json:"secret_value"`
	Version       int64  `json:"version"`
	RevealedAt    string `json:"revealed_at"`
	RemaskAfterMs int64  `json:"remask_after_ms"`
}

// ManagedSecretUse One recorded attempt to substitute a reference into a tool call. Carries what the host decided and who asked, never the value and never the argument the value was going into.
type ManagedSecretUse struct {
	// Invocation identity shared with authorization history; absent for non-tool resolution.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Delivery to the local executor or transport. A handoff does not imply process startup, network delivery, or successful authentication. Pending means no terminal observation was recorded.
	Delivery string `json:"delivery"`
	UsedAt   string `json:"used_at"`
	ToolName string `json:"tool_name"`
	// `resolved` means the host substituted the value. Other outcomes are refusals.
	Outcome string `json:"outcome"`
	// Stored value the attempt reached; absent when it was refused before one was read.
	Version       int64  `json:"version,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	ChatSessionID string `json:"chat_session_id,omitempty"`
	// Recipients the release that handed the value off reviewed; empty until one did.
	Recipients []ManagedSecretUseRecipient `json:"recipients,omitempty"`
	// The unlock a value a person stored left under; the person verified presence to open it for this chat. Absent for other values and for uses that did not leave.
	UnlockID string `json:"unlock_id,omitempty"`
}

// ManagedSecretUseList A bounded rolling window of recent uses, newest first. Not a permanent ledger.
type ManagedSecretUseList struct {
	Uses []ManagedSecretUse `json:"uses"`
	// Opaque cursor for the next page of secret uses; empty or omitted at the end.
	NextCursor string `json:"next_cursor,omitempty"`
}

// ManagedSecretUseRecipient
type ManagedSecretUseRecipient struct {
	Label   string `json:"label"`
	Surface string `json:"surface"`
}

// McpCheckResponse
type McpCheckResponse struct {
	Providers []McpCheckRow `json:"providers"`
}

// McpCheckRow
type McpCheckRow struct {
	// The provider the row checks; absent for a refused overlay row that named no id.
	ProviderID string            `json:"provider_id,omitempty"`
	Status     McpCheckRowStatus `json:"status"`
	// Catalog code when the row failed a trust gate or sync.
	Code   string      `json:"code,omitempty"`
	Notice *NoticeCopy `json:"notice,omitempty"`
}

// McpOAuthCancelRequest
type McpOAuthCancelRequest struct {
	// Pending authorization to cancel; newer attempts and saved credentials are unaffected.
	State string `json:"state"`
}

// McpOAuthCompleteRequest
type McpOAuthCompleteRequest struct {
	// Single-use, PKCE-bound authorization code from the provider redirect.
	Code string `json:"code"`
	// Opaque CSRF value from the authorization request, echoed by the provider.
	State string `json:"state"`
}

// McpOAuthStartResponse
type McpOAuthStartResponse struct {
	// Authorization endpoint URL (open in system browser).
	AuthorizeURL string `json:"authorize_url"`
	// Opaque CSRF value bound to this authorization. Echoed back on the redirect and required by the manual completion path.
	State string `json:"state"`
	// Loopback URL the host is listening on for this authorization (RFC 8252 §7.3, ephemeral port). The redirect completes the exchange on its own; oauth/complete is the fallback for an authorization server that will not redirect to loopback.
	RedirectURI string `json:"redirect_uri,omitempty"`
}

// McpProvider
type McpProvider struct {
	ID          string           `json:"id"`
	Enabled     bool             `json:"enabled"`
	Class       McpProviderClass `json:"class"`
	ToolLoading McpToolLoading   `json:"tool_loading"`
	Transport   string           `json:"transport,omitempty"`
	// Catalog layer that last set url or command. Rejected trust rows use the layer that attempted the bad overlay; last_error holds the reason code.
	ConnectionSource string            `json:"connection_source,omitempty"`
	Status           McpProviderStatus `json:"status,omitempty"`
	// Catalog code for the most recent sync failure or RejectedRow reason.
	LastError string `json:"last_error,omitempty"`
	// Rendered copy for `last_error`.
	Notice    *NoticeCopy `json:"notice,omitempty"`
	ToolCount *int        `json:"tool_count,omitempty"`
	// Distro workflow profile names referencing this provider (display only).
	Profiles []string `json:"profiles,omitempty"`
	// Present when listing with project_id. default inherits device enablement; override is set in the project overlay.
	Source  string   `json:"source,omitempty"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	// Canonical HTTP URL after host inference. A host:port without a scheme is stored as http:// on loopback and https:// otherwise.
	URL string `json:"url,omitempty"`
	// True when a static token is stored (value never returned).
	TokenPresent bool `json:"token_present,omitempty"`
	// True when static HTTP headers are stored (values never returned).
	HeadersPresent bool `json:"headers_present,omitempty"`
	// True when stdio env map has keys (values never returned).
	EnvPresent bool `json:"env_present,omitempty"`
	// True when MCP OAuth tokens are present.
	SignedIn bool `json:"signed_in,omitempty"`
	// Bundled recipe id this row was added from. Empty for Custom.
	Recipe string        `json:"recipe,omitempty"`
	Auth   McpRecipeAuth `json:"auth,omitempty"`
	// Recipe-supplied label for the static token field.
	CredentialLabel string            `json:"credential_label,omitempty"`
	CredentialHint  string            `json:"credential_hint,omitempty"`
	DocsURL         string            `json:"docs_url,omitempty"`
	CredentialWire  McpCredentialWire `json:"credential_wire,omitempty"`
	// Header name when credential_wire is header.
	CredentialHeader string `json:"credential_header,omitempty"`
}

// McpProviderListResponse
type McpProviderListResponse struct {
	Providers []McpProvider `json:"providers"`
}

// McpRecipe
type McpRecipe struct {
	ID               string            `json:"id"`
	Label            string            `json:"label"`
	Hint             string            `json:"hint,omitempty"`
	DocsURL          string            `json:"docs_url,omitempty"`
	URL              string            `json:"url,omitempty"`
	Command          string            `json:"command,omitempty"`
	Args             []string          `json:"args,omitempty"`
	Auth             McpRecipeAuth     `json:"auth"`
	CredentialLabel  string            `json:"credential_label,omitempty"`
	CredentialHint   string            `json:"credential_hint,omitempty"`
	CredentialWire   McpCredentialWire `json:"credential_wire,omitempty"`
	CredentialHeader string            `json:"credential_header,omitempty"`
	// Optional stdio env key names the recipe expects the operator to fill after add (labels only; values are never shipped).
	EnvKeys []McpRecipeEnvKey `json:"env_keys,omitempty"`
	Class   McpProviderClass  `json:"class"`
	// Already present in this catalog scope.
	Added bool `json:"added"`
	// True when a project overlay could declare this recipe (loopback HTTP, no credentials). Remote and stdio recipes are device-only.
	ProjectOK bool `json:"project_ok"`
}

// McpRecipeCatalogResponse
type McpRecipeCatalogResponse struct {
	Recipes []McpRecipe `json:"recipes"`
}

// McpRecipeEnvKey
type McpRecipeEnvKey struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// McpToolInfo
type McpToolInfo struct {
	// Host-qualified tool name (mcp_<provider>_<tool>).
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// McpToolListResponse
type McpToolListResponse struct {
	Tools []McpToolInfo `json:"tools"`
}

// MessageContentPart
type MessageContentPart struct {
	Content   string           `json:"content"`
	Origin    MessageOrigin    `json:"origin"`
	Authority ContentAuthority `json:"authority"`
	TrustTier ContentTrustTier `json:"trust_tier"`
	// Optional host-stamped source identity such as a tool or attachment name.
	Source string `json:"source,omitempty"`
	// Optional MIME type for this text projection.
	MediaType string `json:"media_type,omitempty"`
	// Stable blob ID for a payload attachment, used to restage Edit/rewind.
	BlobID string `json:"blob_id,omitempty"`
	// Host-stamped locator the agent can open. Payload attachments use a host-data-relative path (`prompt-attachments/{blob_id}/{name}`). path_file / path_folder retrieval parts use the path relative to `root_id`, qualified with `@label/` when that root is not primary. Absent when the part has no openable body (search_hit, user prose, host notices).
	Path string `json:"path,omitempty"`
	// Attached root a path_file / path_folder reference resolves under. Absent on payload attachments, search hits, prose, and host notices.
	RootID string `json:"root_id,omitempty"`
	// Full decoded body size for a payload attachment, before preview truncation. Absent for coordinate-only references.
	SizeBytes int64 `json:"size_bytes,omitempty"`
	// Set on retrieval parts only. Absent for user prose, host notices, and attachment payloads, whose kind comes from origin and media_type.
	ReferenceKind MessageReferenceKind `json:"reference_kind,omitempty"`
	// Indexed row's hit_kind for a search_hit reference — the same value the evidence index resolved, not the client's request.
	HitKind string `json:"hit_kind,omitempty"`
	// Indexed row's source_ref for a search_hit reference.
	SourceRef string `json:"source_ref,omitempty"`
	// Indexed row's canonical session coordinate for a search_hit reference.
	SourceSessionID string `json:"source_session_id,omitempty"`
	// Inclusive 1-based first line of a path_file reference's scope. Absent when the reference named a whole file.
	StartLine int `json:"start_line,omitempty"`
	// Inclusive 1-based last line of a path_file reference's scope.
	EndLine int `json:"end_line,omitempty"`
}

// MessageEvent One transcript row change. message is always the full post-write row: apply it as a whole-row snapshot ordered by seq, never a field merge onto a cached row — an absent field means the row no longer has it.
type MessageEvent struct {
	SessionID string          `json:"session_id"`
	Op        MessageChangeOp `json:"op"`
	Seq       int64           `json:"seq,omitempty"`
	Message   Message         `json:"message"`
}

// MessageNavigationResponse
type MessageNavigationResponse struct {
	ContentSHA256 string                `json:"content_sha256"`
	References    []NavigationReference `json:"references"`
}

// ModelPolicy An absent coordinator or lite assigns no model at this layer.
type ModelPolicy struct {
	Coordinator *ModelRefDTO `json:"coordinator,omitempty"`
	Lite        *ModelRefDTO `json:"lite,omitempty"`
	AgentPool   AgentPoolDTO `json:"agent_pool"`
	// Overrides keyed by provider and model; omitted pairs inherit.
	ThinkingOverrides []ThinkingOverride `json:"thinking_overrides,omitempty"`
}

// ModelPolicyEvent
type ModelPolicyEvent struct {
	Scope  string `json:"scope"`
	Action string `json:"action"`
}

// ModelPolicyPatch Omitted keys stay stored. A null coordinator or lite clears this layer's assignment so it inherits. A present thinking_overrides array replaces this layer's overrides; an empty array restores inheritance.
type ModelPolicyPatch struct {
	Coordinator       *ModelRefDTO        `json:"coordinator,omitempty"`
	Lite              *ModelRefDTO        `json:"lite,omitempty"`
	AgentPool         *AgentPoolDTO       `json:"agent_pool,omitempty"`
	ThinkingOverrides *[]ThinkingOverride `json:"thinking_overrides,omitempty"`
}

// ModelRoleEligibility
type ModelRoleEligibility struct {
	// Host assignment decision. Unknown tool support does not block an otherwise chat-capable model.
	Selectable bool   `json:"selectable"`
	State      string `json:"state"`
	Code       string `json:"code"`
	Reason     string `json:"reason"`
	RuleID     string `json:"rule_id,omitempty"`
}

// ModelRoleEligibilitySet
type ModelRoleEligibilitySet struct {
	Coordinator ModelRoleEligibility `json:"coordinator"`
	AgentPool   ModelRoleEligibility `json:"agent_pool"`
	Lite        ModelRoleEligibility `json:"lite"`
}

// NavigationReference A producer destination and its bounded host resolution. Navigation never supplies citation grounding.
type NavigationReference struct {
	ID string `json:"id"`
	// Markdown occurrence kind (link, code, fence, or text).
	Syntax string `json:"syntax"`
	// Exact Markdown destination or standalone path token; independent of display label.
	Mention   string              `json:"mention"`
	ProjectID string              `json:"project_id"`
	RootID    string              `json:"root_id,omitempty"`
	Path      string              `json:"path"`
	EntryKind NavigationEntryKind `json:"entry_kind,omitempty"`
	Status    NavigationStatus    `json:"status"`
	// True only for an authored Markdown destination, rather than an inferred prose path.
	Explicit bool `json:"explicit"`
	// The exact path is absent and has retained deletion history. Availability is rechecked when opening; this never pins the link to a file or version.
	Deleted    bool               `json:"deleted,omitempty"`
	WorkerID   string             `json:"worker_id,omitempty"`
	Line       int                `json:"line,omitempty"`
	EndLine    int                `json:"end_line,omitempty"`
	Candidates []NavigationTarget `json:"candidates,omitempty"`
}

// NavigationTarget
type NavigationTarget struct {
	ProjectID string              `json:"project_id"`
	RootID    string              `json:"root_id"`
	Path      string              `json:"path"`
	EntryKind NavigationEntryKind `json:"entry_kind"`
	WorkerID  string              `json:"worker_id,omitempty"`
}

// NoticeCopy Rendered user-notice copy for the accompanying catalog code.
type NoticeCopy struct {
	Title           string         `json:"title"`
	Message         string         `json:"message"`
	SuggestedAction string         `json:"suggested_action,omitempty"`
	Actions         []NoticeAction `json:"actions,omitempty"`
}

// OAROnFireEvent One Open Agent Rules publish_event record. It is host-observed telemetry, never a fact visible to another rule at the occurrence.
type OAROnFireEvent struct {
	SessionID string `json:"session_id"`
	// Qualified OAR rule identifier.
	Rule   string `json:"rule"`
	Anchor string `json:"anchor"`
	Effect string `json:"effect"`
}

// ObserveEditorDocumentRequest
type ObserveEditorDocumentRequest struct {
	ClientID string `json:"client_id"`
}

// OpenEditorDocumentRequest
type OpenEditorDocumentRequest struct {
	// Root-relative, slash-separated file path.
	Path string `json:"path"`
	// Attached root that contains the file.
	RootID string `json:"root_id"`
	// Stable id for the client view opening the document.
	ClientID string `json:"client_id"`
	// Explicit decoding for BOM-less UTF-16 text.
	DecodeAs string           `json:"decode_as,omitempty"`
	Replica  *RetainedReplica `json:"replica,omitempty"`
}

// OverlayMergePlan
type OverlayMergePlan struct {
	PendingCount    int                      `json:"pending_count"`
	PromoteSequence []string                 `json:"promote_sequence,omitempty"`
	SharedPaths     []OverlayMergeSharedPath `json:"shared_paths,omitempty"`
	Entries         []OverlayMergePlanEntry  `json:"entries,omitempty"`
}

// OverlayMergePlanEntry
type OverlayMergePlanEntry struct {
	WorkerID      string                 `json:"worker_id"`
	AgentType     string                 `json:"agent_type,omitempty"`
	ChangedPaths  []string               `json:"changed_paths,omitempty"`
	CleanPaths    []string               `json:"clean_paths,omitempty"`
	ConflictPaths []string               `json:"conflict_paths,omitempty"`
	PromoteOrder  WorkerPromoteOrderKind `json:"promote_order,omitempty"`
	PromoteAfter  []string               `json:"promote_after,omitempty"`
	BlockedBy     []string               `json:"blocked_by,omitempty"`
	PreviewStale  bool                   `json:"preview_stale,omitempty"`
}

// OverlayMergeSharedPath
type OverlayMergeSharedPath struct {
	Path      string   `json:"path"`
	WorkerIDs []string `json:"worker_ids"`
}

// OverlayPromotion Project file snapshots recorded by the host at the merge event.
type OverlayPromotion struct {
	Files []FileEditSnapshot `json:"files"`
}

// PendingFeedback Live coordinator→human ask awaiting a response.
type PendingFeedback struct {
	// Route key and stable identity for the pending input.
	PhaseID string `json:"phase_id"`
	// Card question (ask_user one decision; markdown OK; host-capped at 800 code points so the prompt fits the 12rem composer-dock prompt area).
	Prompt string `json:"prompt"`
	// Discriminates the resolve endpoint — text answers go to feedback, choices to decisions, and secret values to the protected secret endpoint.
	ResponseType string `json:"response_type,omitempty"`
	// Choice options for single_choice/multi_choice asks.
	Options []string `json:"options,omitempty"`
	// Whether a free-text answer outside options is accepted (choice asks).
	AllowOther bool `json:"allow_other,omitempty"`
	// Exact visual artifact under review.
	ArtifactID string `json:"artifact_id,omitempty"`
	// Exact visual artifacts under comparison.
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
	Purpose     string   `json:"purpose,omitempty"`
	// Run revision that issued this request; retained with the answer for audit.
	IssuedRevision int64            `json:"issued_revision,omitempty"`
	Secret         *SecretInputMeta `json:"secret,omitempty"`
}

// PendingWorkflowStart
type PendingWorkflowStart struct {
	WorkflowID      string `json:"workflow_id"`
	WorkflowVersion string `json:"workflow_version"`
	PresetID        string `json:"preset_id,omitempty"`
	Label           string `json:"label,omitempty"`
}

// PersistWorkflowRequest
type PersistWorkflowRequest struct {
	Version string `json:"version"`
	Confirm bool   `json:"confirm"`
	Trigger string `json:"trigger,omitempty"`
}

// PersistWorkflowResponse
type PersistWorkflowResponse struct {
	Path    string          `json:"path"`
	Summary WorkflowSummary `json:"summary"`
}

// Person A human who acts on this host. Durable authorship names a person by id; the window or tab they used is a separate client id.
type Person struct {
	ID   string     `json:"id"`
	Role PersonRole `json:"role"`
}

// PinEditorDocumentRequest
type PinEditorDocumentRequest struct {
	ClientID string `json:"client_id"`
	// Reserves this snapshot for the save with this operation identity. Omit for a bounded read snapshot.
	OperationID string `json:"operation_id,omitempty"`
}

// PowerSettingsResponse
type PowerSettingsResponse struct {
	// Device preference for preventing macOS idle sleep while authoritative host work is active.
	KeepAwakeWhileWorking bool `json:"keep_awake_while_working"`
	// Whether this host supports an idle-sleep assertion.
	Supported bool `json:"supported"`
	// Whether the host currently holds the assertion.
	Inhibiting bool `json:"inhibiting"`
	// Number of distinct active host work leases keeping the assertion requested.
	ActiveWorkCount int `json:"active_work_count"`
	// Most recent assertion failure, omitted after a successful acquisition.
	LastError string `json:"last_error,omitempty"`
}

// PreflightEvent Host readiness changed while the app was running. Device-scoped: the report is about the machine, not one project, even when a probe's result names the projects it is about.
// Carries no verdict. A probe's copy, tier, and scope resolve together from the user-notice catalog when `GET /v1/preflight` runs, so re-reading is what keeps one condition described one way.
type PreflightEvent struct {
	// Probe whose verdict may have moved, e.g. `provider_configured`. A hint for clients that only care about some of them; the report is still the authority on every probe.
	ProbeID string `json:"probe_id"`
}

// PreflightProbe
type PreflightProbe struct {
	// Stable probe id, e.g. `git_engine`.
	ID     string `json:"id"`
	Status string `json:"status"`
	// User-notice code for a non-ok result, e.g. `GIT_ENGINE_UNAVAILABLE`. Absent when status is `ok`.
	Code string `json:"code,omitempty"`
	// Structured facts for the copy template (e.g. `want`/`got`). Never a path under the user's home directory and never a secret.
	Detail map[string]string `json:"detail,omitempty"`
	// Whether this result leaves the app usable. Host-declared in the user-notice catalog; clients must not derive it from `status`.
	Tier NoticeTier `json:"tier,omitempty"`
	// Where this result renders. Host-declared; clients must not infer placement from the surface the notice arrived on.
	Scope NoticeScope `json:"scope,omitempty"`
	// Which declared resolution produced `tier`/`scope`. One code can mean two different conditions — `OS_BELOW_FLOOR` blocks the app when the OS is genuinely too old but is only a missing capability when the version could not be read — so the resolution, not the code, is the unit of partitioning. `default` for unconditional notices.
	Resolution         string              `json:"resolution,omitempty"`
	CatastrophicDetail *CatastrophicDetail `json:"catastrophic_detail,omitempty"`
	// Rendered catalog title. Den displays wire copy only.
	Title string `json:"title,omitempty"`
	// Rendered catalog message.
	Message string `json:"message,omitempty"`
	// Rendered catalog remedy. Preflight reports and never remediates — the app does not run this on the user's behalf.
	SuggestedAction string `json:"suggested_action,omitempty"`
	// Structured in-app destinations for the remedy; clients never infer them from copy.
	Actions []NoticeAction `json:"actions,omitempty"`
}

// PreflightReport
type PreflightReport struct {
	// Worst status across all probes. `degraded` means a capability is unavailable while the app still works; `blocked` means it cannot function.
	Overall string `json:"overall"`
	// Probe results in registration order.
	Probes                 []PreflightProbe       `json:"probes"`
	AttachmentCapabilities AttachmentCapabilities `json:"attachment_capabilities"`
}

// PresenceChallenge One short-lived request for native user-presence verification. The proof payload is opaque canonical bytes naming its purpose and subject; the native shell signs it only after the operating system verifies the device user. The API bearer can begin a challenge but cannot complete one.
type PresenceChallenge struct {
	ChallengeID string `json:"challenge_id"`
	// Base64url without padding of the exact bytes the native shell signs.
	ProofPayload string `json:"proof_payload"`
	// Host-authored reason shown by the operating-system prompt.
	Prompt    string `json:"prompt"`
	ExpiresAt string `json:"expires_at"`
}

// PresenceProof Challenge-bound native user-presence proof. The signature covers the challenge payload plus the authenticator identifier.
type PresenceProof struct {
	ChallengeID   string                `json:"challenge_id"`
	Authenticator PresenceAuthenticator `json:"authenticator"`
	// Base64url without padding Ed25519 signature from the installed desktop shell.
	Signature string `json:"signature"`
}

// PreviewActionOverlay
type PreviewActionOverlay struct {
	Label  string   `json:"label"`
	Target string   `json:"target,omitempty"`
	X      *float64 `json:"x,omitempty"`
	Y      *float64 `json:"y,omitempty"`
	W      *float64 `json:"w,omitempty"`
	H      *float64 `json:"h,omitempty"`
}

// PreviewAttachment Held page identity for restoring a preview watch; frames are omitted
type PreviewAttachment struct {
	SessionID string `json:"session_id"`
	PageID    string `json:"page_id"`
	// Assistant transcript row that originated this preview surface.
	AssistantMessageID string `json:"assistant_message_id"`
	// Tool invocation within the containing assistant row.
	ToolCallID      string `json:"tool_call_id"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
	URL             string `json:"url,omitempty"`
	Title           string `json:"title,omitempty"`
	Idle            *bool  `json:"idle,omitempty"`
}

// PreviewEvent Read-only live page mirror (CDP screencast frames + driven-action overlay). Frames ride the authenticated host SSE channel; Den never forwards input.
type PreviewEvent struct {
	Op        PreviewEventOp `json:"op"`
	SessionID string         `json:"session_id"`
	PageID    string         `json:"page_id"`
	// Assistant transcript row that originated this preview surface.
	AssistantMessageID string `json:"assistant_message_id"`
	// Tool invocation within the containing assistant row.
	ToolCallID      string                `json:"tool_call_id"`
	ParentSessionID string                `json:"parent_session_id,omitempty"`
	Seq             int64                 `json:"seq"`
	Mime            string                `json:"mime,omitempty"`
	JpegB64         string                `json:"jpeg_b64,omitempty"`
	Width           int                   `json:"width,omitempty"`
	Height          int                   `json:"height,omitempty"`
	URL             string                `json:"url,omitempty"`
	Title           string                `json:"title,omitempty"`
	Idle            *bool                 `json:"idle,omitempty"`
	Action          *PreviewActionOverlay `json:"action,omitempty"`
}

// PreviewListResponse
type PreviewListResponse struct {
	Previews []PreviewAttachment `json:"previews"`
}

// PreviewWatchRequest
type PreviewWatchRequest struct {
	Watching bool   `json:"watching"`
	PageID   string `json:"page_id"`
}

// PreviewWatchResult
type PreviewWatchResult struct {
	Watching bool   `json:"watching"`
	PageID   string `json:"page_id"`
}

// PricingSourceMeta
type PricingSourceMeta struct {
	ID         string              `json:"id"`
	Label      string              `json:"label"`
	Kind       string              `json:"kind"`
	Enabled    bool                `json:"enabled"`
	Status     PricingSourceStatus `json:"status"`
	UpdatedAt  *time.Time          `json:"updated_at,omitempty"`
	FetchedAt  *time.Time          `json:"fetched_at,omitempty"`
	ModelCount int                 `json:"model_count,omitempty"`
	// A background fetch of this source is in flight; status and freshness still describe the last settled attempt.
	Refreshing bool `json:"refreshing,omitempty"`
}

// ProgressChange
type ProgressChange struct {
	Kind  ProgressChangeKind `json:"kind"`
	Label string             `json:"label"`
	// The step's prior label when it was re-scoped (updated rows only).
	PrevLabel string `json:"prev_label,omitempty"`
	State     string `json:"state"`
}

// ProgressCompleteMeta
type ProgressCompleteMeta struct {
	Steps []ProgressStep `json:"steps"`
	Seq   int            `json:"seq"`
}

// ProgressDigest
type ProgressDigest struct {
	Steps    []ProgressStep `json:"steps"`
	Revision uint64         `json:"revision"`
}

// ProgressEvent
type ProgressEvent struct {
	Revision uint64 `json:"revision"`
}

// ProgressStep
type ProgressStep struct {
	State string `json:"state"`
	Label string `json:"label"`
}

// ProgressUpdateMeta Incremental progress update with a plan, delta, or aggregate summary.
type ProgressUpdateMeta struct {
	Changes []ProgressChange       `json:"changes,omitempty"`
	Steps   []ProgressStep         `json:"steps,omitempty"`
	Summary *ProgressUpdateSummary `json:"summary,omitempty"`
	Initial bool                   `json:"initial,omitempty"`
	Seq     int                    `json:"seq"`
}

// ProgressUpdateSummary Aggregate progress update; the full checklist remains in ProgressDigest.
type ProgressUpdateSummary struct {
	ChangeCount int `json:"change_count"`
	TotalSteps  int `json:"total_steps"`
	Pending     int `json:"pending"`
	Done        int `json:"done"`
	NA          int `json:"na"`
}

// Project
type Project struct {
	ID              string        `json:"id"`
	Name            *string       `json:"name"`
	Roots           []ProjectRoot `json:"roots"`
	RootsGeneration int           `json:"roots_generation"`
	// Number of retained root chats, including archived chats and excluding worker sessions.
	SessionCount int  `json:"session_count"`
	Starred      bool `json:"starred"`
	// Computed from the project having exactly one primary draft root.
	IsDraft bool `json:"is_draft"`
	// Durable save-to-folder progress, or null when no transition is active.
	Promotion *ProjectPromotion `json:"promotion"`
	// Product-render VisualArtifact id for the home-page project card (capture or render).
	CoverArtifactID *string `json:"cover_artifact_id,omitempty"`
	// Root session id for fetching cover bytes via GET /v1/sessions/{id}/artifacts/{artifact_id}.
	CoverRootSessionID *string `json:"cover_root_session_id,omitempty"`
	// Producer face of the designated cover artifact.
	CoverSource *string `json:"cover_source,omitempty"`
	// When the product-render cover was last designated.
	CoverUpdatedAt *time.Time `json:"cover_updated_at,omitempty"`
	// Newest activity_at among the project's sessions; null when it has none.
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	// When the person last opened the project: created a chat in it, or had one of its chats on screen (markSessionSeen). Fetching a session does not open its project.
	LastOpenedAt time.Time `json:"last_opened_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// ProjectAgentContext Actual repository guidance available at one root-relative path. Built by the same AGENTS.md and project-skill resolvers used for model context.
type ProjectAgentContext struct {
	ProjectID           string                    `json:"project_id"`
	RootID              string                    `json:"root_id"`
	Path                string                    `json:"path"`
	InstructionsEnabled bool                      `json:"instructions_enabled"`
	SkillsEnabled       bool                      `json:"skills_enabled"`
	Instructions        []AgentContextInstruction `json:"instructions"`
	Skills              []AgentContextSkill       `json:"skills"`
}

// ProjectCostReport
type ProjectCostReport struct {
	Total                int         `json:"total"`
	SessionCount         int         `json:"session_count"`
	ArchivedSessionCount int         `json:"archived_session_count"`
	WorkerTaskCount      int         `json:"worker_task_count"`
	MaxSessionNanoUsd    int64       `json:"max_session_nano_usd"`
	MaxSessionTokens     int         `json:"max_session_tokens"`
	NextCursor           string      `json:"next_cursor,omitempty"`
	Summary              CostSummary `json:"summary"`
	// LLM utility usage attributed to the project but not to a session.
	ProjectUtilities CostSummary `json:"project_utilities"`
	// Usage retained after its session was deleted or expired.
	RetiredSessions CostSummary `json:"retired_sessions"`
	// One ordered page of top-level project sessions, including archived sessions. Totals and maxima cover the full project; total is the number matching the search.
	Sessions []ProjectCostSession `json:"sessions"`
}

// ProjectCostSession
type ProjectCostSession struct {
	Session SessionSummary `json:"session"`
	Cost    CostSummary    `json:"cost"`
}

// ProjectEvent
type ProjectEvent struct {
	ID      string             `json:"id"`
	Action  ProjectEventAction `json:"action"`
	Project *Project           `json:"project,omitempty"`
}

// ProjectListResponse
type ProjectListResponse struct {
	Projects []Project `json:"projects"`
	// Opaque cursor for the next project page; empty or omitted at the end.
	NextCursor string `json:"next_cursor,omitempty"`
}

// ProjectPromotion
type ProjectPromotion struct {
	DestinationPath string    `json:"destination_path"`
	InitGit         bool      `json:"init_git"`
	Phase           string    `json:"phase"`
	LastError       string    `json:"last_error"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ProjectRemovalAssessment
type ProjectRemovalAssessment struct {
	ProjectID         string                    `json:"project_id"`
	AssessmentToken   string                    `json:"assessment_token"`
	ExtensionRevision string                    `json:"extension_revision"`
	Complete          bool                      `json:"complete"`
	Checks            []ProjectRemovalCheck     `json:"checks"`
	Extensions        []ProjectRemovalExtension `json:"extensions"`
}

// ProjectRemovalCheck
type ProjectRemovalCheck struct {
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	State     string `json:"state"`
	Revision  string `json:"revision"`
	// Why an unknown check could not read the project's references. Absent when checked.
	Reason string `json:"reason,omitempty"`
}

// ProjectRemovalExtension
type ProjectRemovalExtension struct {
	PackID      string                 `json:"pack_id"`
	Disposition string                 `json:"disposition"`
	Reasons     []ProjectRemovalReason `json:"reasons"`
}

// ProjectRemovalReason
type ProjectRemovalReason struct {
	Code      string `json:"code"`
	SubjectID string `json:"subject_id"`
}

// ProjectRemovalRequest
type ProjectRemovalRequest struct {
	OperationID      string   `json:"operation_id"`
	AssessmentToken  string   `json:"assessment_token"`
	RemoveExtensions []string `json:"remove_extensions"`
	Force            bool     `json:"force"`
}

// ProjectRemovalResult
type ProjectRemovalResult struct {
	Assessment   *ProjectRemovalAssessment `json:"assessment,omitempty"`
	OperationID  string                    `json:"operation_id"`
	ProjectID    string                    `json:"project_id"`
	ProjectState string                    `json:"project_state"`
	CleanupState string                    `json:"cleanup_state"`
	Extensions   []string                  `json:"extensions"`
	Reason       string                    `json:"reason"`
	FailureCode  string                    `json:"failure_code"`
	Documents    int                       `json:"documents"`
	Sessions     int                       `json:"sessions"`
	Workers      int                       `json:"workers"`
	Overlays     int                       `json:"overlays"`
}

// ProjectRoot
type ProjectRoot struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// Canonical display label and case-insensitive @label addressing token. Draft roots use the temporary label Draft; attached roots default to the folder basename.
	Label         string    `json:"label"`
	IsPrimary     bool      `json:"is_primary"`
	GitRemoteHash *string   `json:"git_remote_hash,omitempty"`
	AddedAt       time.Time `json:"added_at"`
	// Engine-created draft workspace or user-attached folder.
	Kind string `json:"kind"`
}

// ProjectSourceEntryCreatedResponse Completed in-app creation. Only the normalized path comes back — a new
// file is read through GET /source like any other, which is where the
// editor gets the sha256 its first save must echo.
type ProjectSourceEntryCreatedResponse struct {
	// Normalized repo-relative path (slash-separated)
	Path string `json:"path"`
}

// ProjectSourceLifecycleResponse Canonical address a rename or copy landed on.
type ProjectSourceLifecycleResponse struct {
	// Attached root that contains the new path
	RootID string `json:"root_id"`
	// Normalized root-relative path (slash-separated)
	Path string `json:"path"`
}

// ProjectSourceReadResponse Sandbox-validated project file content or metadata for the in-app
// source viewer. Editable ceiling is 4 MiB. When over_limit or binary is
// true, content is empty and Den opens an info card or image tab (bytes
// via GET …/source/raw). Truncated-prefix reads are not returned.
// UTF-16LE and UTF-16BE text is decoded for editing; other non-UTF-8 text
// is refused with 415 unsupported_encoding rather than reported as binary.
type ProjectSourceReadResponse struct {
	// Stable identity of the logical file across moves and workspaces; absent until the source ledger has recorded this path.
	FileID string `json:"file_id,omitempty"`
	// The recorded version holding exactly the content read; absent when the ledger has not yet recorded this content (an external change the watcher has not settled). A read records nothing.
	VersionID string `json:"version_id,omitempty"`
	// Physical project or worker workspace that served this read.
	WorkspaceID   string              `json:"workspace_id"`
	WorkspaceKind SourceWorkspaceKind `json:"workspace_kind"`
	// Normalized repo-relative path (slash-separated)
	Path string `json:"path"`
	// Supported host grammar for this path, matching the editor_language contribution fact. Omitted when no supported grammar applies.
	Language string `json:"language,omitempty"`
	// Full UTF-8 editor text when editable (byte-order marks stripped);
	// empty for over-limit or binary, and empty when an open response
	// carries the editor document, whose crdt_update holds the text
	Content string `json:"content"`
	// True when size_bytes exceeds the 4 MiB editable ceiling
	OverLimit bool `json:"over_limit"`
	// True for non-text content (including images). Content is empty;
	// image tabs load bytes from GET …/source/raw.
	Binary bool `json:"binary"`
	// Original file size on disk in bytes
	SizeBytes int64 `json:"size_bytes"`
	// File modification time (UTC)
	ModifiedAt string `json:"modified_at,omitempty"`
	// Sniffed content type (not extension-derived)
	MIME string `json:"mime,omitempty"`
	// Hex SHA-256 of on-disk bytes (BOM included when present), set only
	// for full editable text reads. Echo it as base_sha256 on an editor save.
	SHA256 string `json:"sha256,omitempty"`
	// Attached root that served the read (the mirrored root for worker
	// overlay reads); path is root-relative to it. Echo it as root_id
	// to pin later reads and saves to the same root.
	RootID string `json:"root_id,omitempty"`
	// Round-trip encoding for editable text (and over-limit text metadata).
	// Omitted for binary and image reads. Echo on PUT /source.
	Encoding SourceEncoding `json:"encoding,omitempty"`
	// Present only for an absent path opened with include_deleted. Current content and SHA-256 remain empty and writable is false.
	Deleted *SourceDeletedFile `json:"deleted,omitempty"`
	// True when owner-write mode bits are set at load. Report only — the
	// write path still fails honestly if the fact was stale.
	Writable bool `json:"writable"`
}

// ProjectSourceWriteResponse Completed in-app editor save.
type ProjectSourceWriteResponse struct {
	// Normalized repo-relative path (slash-separated)
	Path string `json:"path"`
	// New file size in bytes
	SizeBytes int64 `json:"size_bytes"`
	// Hex SHA-256 of the saved content (base for the next save)
	SHA256 string `json:"sha256"`
}

// ProjectTrust What a project supplies, which of it applies, and what has moved since it was read. One read shared by the composer chip and Project configuration so those surfaces cannot disagree.
type ProjectTrust struct {
	Review ProjectTrustReview `json:"review"`
	// Distinct files whose current contents have not been marked read.
	UnreadCount int                   `json:"unread_count"`
	ProjectID   string                `json:"project_id"`
	Surfaces    []ProjectTrustSurface `json:"surfaces"`
}

// ProjectTrustReview The shared Trust comparison. Marking it read preserves its identity and files.
type ProjectTrustReview struct {
	ID        string            `json:"id"`
	ProjectID string            `json:"project_id"`
	Changes   []TrustFileChange `json:"changes"`
}

// ProjectTrustSurface One discovered surface joined to what the human enabled and last read.
type ProjectTrustSurface struct {
	ID    TrustSurfaceId    `json:"id"`
	Label string            `json:"label"`
	Group TrustSurfaceGroup `json:"group"`
	// Number of discovered items. For skills this is what the loader will use, so a name shadowed across roots is counted once.
	Count          int                `json:"count"`
	Items          []TrustSurfaceItem `json:"items"`
	DeviceEnabled  bool               `json:"device_enabled"`
	ProjectEnabled bool               `json:"project_enabled"`
	// Whether the surface currently affects the project.
	Applying bool `json:"applying"`
	// Whether the tree matches the version last read. An unseen surface still applies; reading does not change what the agent gets.
	Seen bool `json:"seen"`
}

// PromoteProjectRequest
type PromoteProjectRequest struct {
	// An empty folder that receives the draft through a restart-safe save.
	RootPath string `json:"root_path"`
	// Initialise a git repository in the staged workspace before installation.
	InitGit bool `json:"init_git,omitempty"`
}

// PromptAcceptedResponse
type PromptAcceptedResponse struct {
	// Durable submission state observed at admission or replay. queued means the turn is waiting for session execution; eligible text-only turns may also appear in the editable next-turn draft. canceled means the user removed a waiting draft item before it ran.
	Status string `json:"status"`
	// Durable idempotency identity of this prompt action.
	OperationID string `json:"operation_id"`
	// Durable message id of the transcript user row minted for this prompt. Clients correlate a submission with its transcript row by id.
	MessageID string `json:"message_id"`
	// Session event revision minted after the submission was admitted. Every session event with a greater revision reflects this submission, so the sender can treat the prompt as in flight until it applies one. Session events published earlier can still arrive after this response and describe the session before admission. 0 means the host publishes no session events.
	SessionRevision int64 `json:"session_revision"`
}

// PromptAttachmentPart Names an attachment the client already streamed to the upload route. Bodies never ride the prompt envelope, so this carries no bytes: the host re-derives filename and media type from the stored body, and the blob id is the only client-supplied value.
type PromptAttachmentPart struct {
	// Stable blob id returned by uploadAttachment
	BlobID string `json:"blob_id"`
}

// PromptRequest
type PromptRequest struct {
	// Stable client mutation identity. An exact retry replays the original submission; reusing this id with different input is rejected with 409 idempotency_conflict.
	OperationID string `json:"operation_id"`
	// User prose; may be empty when attachments or references are present, or when the active workflow accepts an empty request
	Text string `json:"text"`
	// Handles for bodies already streamed to the attachment upload route — text, documents, and rasters alike. No bytes, no URLs, no paths.
	Attachments []PromptAttachmentPart `json:"attachments,omitempty"`
	// On-disk, artifact, and search-hit references; no bytes. Counted separately from uploaded attachments.
	References []PromptReferencePart `json:"references,omitempty"`
	// Typed managed-secret selections. Raw token syntax in text is rejected.
	Secrets []PromptSecretReferencePart `json:"secrets,omitempty"`
}

// PromptSecretReferencePart A typed selection of a managed-secret reference. It never carries protected bytes.
type PromptSecretReferencePart struct {
	// Value-free managed-secret reference returned by the host.
	Reference string `json:"reference"`
}

// PromptStreamChunk
type PromptStreamChunk struct {
	// Message text chunk
	Token string `json:"token"`
	// Selects replacement instead of append
	Reset bool `json:"reset,omitempty"`
	// Replaces the in-flight tool-call list when present
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// True when this chunk terminates the stream
	Done bool `json:"done"`
}

// ProviderCapabilityEvidence
type ProviderCapabilityEvidence struct {
	State   string   `json:"state"`
	Sources []string `json:"sources,omitempty"`
}

// ProviderEvent
type ProviderEvent struct {
	ProviderID string `json:"provider_id"`
	Action     string `json:"action"`
}

// ProviderFeatures Transport-wide controls. Model assignment uses ProviderModelMeta.capabilities instead.
type ProviderFeatures struct {
	// True when the AI provider can make tool/function calls.
	ToolCalls bool `json:"tool_calls"`
	// True when the AI provider exposes a reasoning/thinking control.
	Thinking bool `json:"thinking"`
	// How the driver expresses prompt prefix caching on the wire. See docs/session.md § Prompt caching.
	PromptCache string `json:"prompt_cache"`
}

// ProviderKindListResponse
type ProviderKindListResponse struct {
	Kinds []ProviderKindTemplate `json:"kinds"`
}

// ProviderKindTemplate
type ProviderKindTemplate struct {
	Kind          string `json:"kind"`
	Label         string `json:"label"`
	BaseURL       string `json:"base_url"`
	EndpointStyle string `json:"endpoint_style,omitempty"`
	// True when instances of this kind need a Settings-saved API key.
	RequiresAPIKey bool `json:"requires_api_key"`
	// This kind's ambient credential chain when it has one (aws-sdk-chain, google-adc). Present with requires_api_key true means the user may choose between a stored key (default) and the ambient chain; present with requires_api_key false means the kind is ambient-only.
	AmbientAuth string `json:"ambient_auth,omitempty"`
	// Product host platforms this kind supports (macos, windows, linux). Empty or omitted means all platforms. Ship providers.yaml is SSOT.
	Platforms []string `json:"platforms,omitempty"`
}

// ProviderListResponse
type ProviderListResponse struct {
	Providers []ProviderMeta `json:"providers"`
}

// ProviderMeta
type ProviderMeta struct {
	ID string `json:"id"`
	// Provider kind shared by related instances.
	Kind string `json:"kind"`
	// Human-facing display name for the AI provider instance.
	Label string `json:"label,omitempty"`
	// URL, SDK region, or empty derived value according to endpoint_style.
	BaseURL       string `json:"base_url,omitempty"`
	EndpointStyle string `json:"endpoint_style,omitempty"`
	// True when the provider can make API calls. Keyed AI providers require a non-empty credential saved via PUT /v1/providers/{provider_id}/credential (environment variables do not configure). Local keyless providers are ready without credentials; ambient providers must resolve their declared credential chain. List membership is providers.local.yaml — Den does not keep a parallel hide allowlist.
	Configured bool `json:"configured"`
	// True when credentials are saved for this provider instance.
	CredentialPresent bool `json:"credential_present"`
	// Credential source selected for this instance; environment hints are never a source.
	CredentialSource string `json:"credential_source,omitempty"`
	// When the provider last accepted this instance's credentials on a live call. Omitted until the host has observed an accepted call since the credentials last changed.
	CredentialVerifiedAt *time.Time `json:"credential_verified_at,omitempty"`
	// True when the AI provider needs a Settings-saved API key; false for keyless/local providers and for instances of an ambient_auth kind that selected the ambient credential chain. Catalog api_key_env is a docs hint only and does not auto-configure.
	RequiresAPIKey bool `json:"requires_api_key"`
	// Names the credential chain this kind can authenticate with instead of a stored key (aws-sdk-chain, google-adc). Empty when a stored key is the only way in. Setting requires_api_key false on an instance of such a kind selects the ambient chain — there is no separate credential-mode field.
	AmbientAuth string `json:"ambient_auth,omitempty"`
	// True while this instance is trusted with detected credentials, so a request to it raises no secret card and sends the detected value unchanged. Transcript redaction, managed-secret references, and the credential vault are unaffected. The decision is bound to the resolved destination (kind, endpoint, endpoint style, ambient auth); changing any of those withdraws it. Omitted means false.
	SecretScreenTrusted bool `json:"secret_screen_trusted,omitempty"`
	// Product host platforms this kind supports (macos, windows, linux). Empty or omitted means all platforms. Ship providers.yaml is SSOT; the host omits unsupported kinds/instances from list projections.
	Platforms []string            `json:"platforms,omitempty"`
	Features  ProviderFeatures    `json:"features"`
	Models    []ProviderModelMeta `json:"models"`
	// Explicit model or deployment rows stored for this provider instance before live availability is applied.
	ConfiguredModels []ProviderModelMeta `json:"configured_models,omitempty"`
	UsageQuota       *ProviderUsageQuota `json:"usage_quota,omitempty"`
	// True when the provider is configured and has at least one ConversationEligible model with positive capability evidence for at least one policy slot, after role exclusions (exists-slot).
	ReadyToAssign bool `json:"ready_to_assign,omitempty"`
	// True when this kind is mapped to models.dev and a usable modelfeed document is present (catalog-first merge).
	CatalogAuthoritative bool `json:"catalog_authoritative,omitempty"`
	// models.dev document freshness for catalog merge.
	CatalogStatus string `json:"catalog_status,omitempty"`
	// Live /models discovery outcome for this list projection.
	DiscoveryStatus string `json:"discovery_status,omitempty"`
}

// ProviderModelCapabilities
type ProviderModelCapabilities struct {
	Chat             ProviderCapabilityEvidence `json:"chat"`
	Streaming        ProviderCapabilityEvidence `json:"streaming"`
	Tools            ProviderCapabilityEvidence `json:"tools"`
	Vision           ProviderCapabilityEvidence `json:"vision"`
	Reasoning        ProviderCapabilityEvidence `json:"reasoning"`
	StructuredOutput ProviderCapabilityEvidence `json:"structured_output"`
	// Whether the route caches prompt prefixes on request markers; a proxy such as LiteLLM reports it per model
	PromptCaching ProviderCapabilityEvidence `json:"prompt_caching"`
}

// ProviderModelConfig
type ProviderModelConfig struct {
	ID                 string                     `json:"id"`
	InputPer1KNanoUSD  *int64                     `json:"input_per_1k_nano_usd,omitempty"`
	OutputPer1KNanoUSD *int64                     `json:"output_per_1k_nano_usd,omitempty"`
	PricedAs           string                     `json:"priced_as,omitempty"`
	ContextLength      int                        `json:"context_length,omitempty"`
	Capabilities       *ProviderModelCapabilities `json:"capabilities,omitempty"`
}

// ProviderModelMeta
type ProviderModelMeta struct {
	Thinking           ThinkingCapabilities `json:"thinking,omitempty"`
	ID                 string               `json:"id"`
	InputPer1KNanoUSD  *int64               `json:"input_per_1k_nano_usd,omitempty"`
	OutputPer1KNanoUSD *int64               `json:"output_per_1k_nano_usd,omitempty"`
	// Underlying catalog model id used to price a customer-named deployment.
	PricedAs string `json:"priced_as,omitempty"`
	// The model's usable context window in tokens when known (pinned in config or discovered from the host); omitted when unknown.
	ContextLength int                       `json:"context_length,omitempty"`
	Capabilities  ProviderModelCapabilities `json:"capabilities"`
	Eligibility   ModelRoleEligibilitySet   `json:"eligibility"`
}

// ProviderProbeResult
type ProviderProbeResult struct {
	OK        bool         `json:"ok"`
	Code      ApiErrorCode `json:"code,omitempty"`
	Message   string       `json:"message,omitempty"`
	LatencyMs int64        `json:"latency_ms,omitempty"`
}

// ProviderUsageQuota
type ProviderUsageQuota struct {
	// Provider-native usage unit (e.g. neurons for Cloudflare Workers AI).
	Unit string `json:"unit"`
	// Consumption in unit for the current quota window.
	Used float64 `json:"used"`
	// Included allowance in unit for the current quota window.
	Limit float64 `json:"limit"`
	// True when the provider publishes a recurring daily included allowance (for example Cloudflare Workers AI daily neurons) rather than a hard lifetime cap.
	ProviderDailyAllowance bool `json:"provider_daily_allowance,omitempty"`
	// RFC3339 timestamp when the quota window resets.
	ResetsAt string `json:"resets_at,omitempty"`
	// Estimated USD overage in nano-USD for usage above the included daily allowance when applicable.
	OverageNanoUSD *int64 `json:"overage_nano_usd,omitempty"`
}

// PutProjectSourceRequest In-app editor save of one project file (full replacement content).
type PutProjectSourceRequest struct {
	// Stable mutation identity; exact retries return the original result and conflicting reuse returns 409 idempotency_conflict.
	OperationID string `json:"operation_id"`
	// Repo-relative path (slash-separated) of an existing file
	Path string `json:"path"`
	// Attached root the save lands on; omitted searches every root primary-first
	RootID string `json:"root_id"`
	// Full replacement UTF-8 editor text without a byte-order mark. Decoded text is bounded to 8 MiB to allow UTF-16 expansion; the encoded file, including its byte-order mark, must fit 4 MiB. The JSON envelope permits escaping of the full supported content.
	Content string `json:"content"`
	// Exact encoding from the matching read. The host restores UTF-8 and
	// UTF-16 byte-order marks when named by the encoding. Any missing or
	// unsupported value is 400.
	Encoding SourceEncoding `json:"encoding"`
	// sha256 reported by the read (or previous save) this edit was based
	// on; mismatched on-disk content refuses the save with 409
	// source_write_conflict.
	BaseSHA256 string `json:"base_sha256"`
}

// QueueDraft Editable projection of eligible text-only turns waiting to run.
type QueueDraft struct {
	QueueItems []QueueItem `json:"queue_items"`
	// When true, round-end draining is suppressed while the user edits the queue (drag, link, reorder).
	Hold bool `json:"hold"`
	// True while the head item or linked head group is reserved for delivery into the active turn at its next safe boundary. Reserved items cannot be edited until they land, or until the reservation is released with cancel_send. A running loop only reaches that boundary between provider calls and tool batches, so a reservation can wait — indefinitely, while a tool approval is parked — and cancel_send is the way back.
	Sending  bool   `json:"sending"`
	Revision uint64 `json:"revision"`
}

// QueueEvent
type QueueEvent struct {
	Revision uint64 `json:"revision"`
}

// QueueItem
type QueueItem struct {
	// Durable prompt submission id for the waiting turn.
	ID string `json:"id"`
	// Person who sent this message. Linked items share one sender.
	SubmittedBy string `json:"submitted_by"`
	Text        string `json:"text"`
	// Shared id for "linked" items that drain together as one coalesced turn. Absent when the item drains as its own turn.
	GroupID   string `json:"group_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

// QueueMutateRequest
type QueueMutateRequest struct {
	Op string `json:"op,omitempty"`
	// Queue revision the mutation was authored against; stale mutations are rejected atomically.
	ExpectedRevision uint64   `json:"expected_revision"`
	OperationID      string   `json:"operation_id,omitempty"`
	ItemIDs          []string `json:"item_ids,omitempty"`
	Hold             *bool    `json:"hold,omitempty"`
	// Replacement message body for op "update" (targets item_ids[0]).
	Text *string `json:"text,omitempty"`
}

// ReadOutlineResponse
type ReadOutlineResponse struct {
	Path             string      `json:"path"`
	Mode             string      `json:"mode"`
	OutlineKind      OutlineKind `json:"outline_kind,omitempty"`
	OutlineSource    string      `json:"outline_source,omitempty"`
	TotalLines       int         `json:"total_lines"`
	LogDigest        *LogDigest  `json:"log_digest,omitempty"`
	TruncationBanner string      `json:"truncation_banner,omitempty"`
}

// RedactedSpan One replacement in this message copy. Offsets index the redacted string, and length is the marker's own width — the placeholder or the capability reference — never the width of the value it replaced, so a span can be marked without disclosing how long the original was.
type RedactedSpan struct {
	// Dotted path of the redacted field within this message, such as content, tool_result.content, or tool_calls.0.args.command.
	Field string `json:"field"`
	// Rune offset of the marker within the redacted field value.
	Start int `json:"start"`
	// Rune length of the marker.
	Length int             `json:"length"`
	Kind   RedactionKind   `json:"kind"`
	Source RedactionSource `json:"source,omitempty"`
	// Catalog rule identity, when a shape rule named the value.
	RuleID string `json:"rule_id,omitempty"`
	// Human-readable rule name for presentation.
	RuleTitle string `json:"rule_title,omitempty"`
}

// RenameProjectSourceRequest Same-root rename or move of one file or folder. Missing parent folders
// on to are created. Cross-root moves are refused.
type RenameProjectSourceRequest struct {
	// Stable mutation identity; exact retries return the original result and conflicting reuse returns 409 idempotency_conflict.
	OperationID string `json:"operation_id"`
	// Attached root both paths must stay under; omitted uses the primary root
	RootID string `json:"root_id,omitempty"`
	// Existing root-relative path to rename or move
	From string `json:"from"`
	// Destination root-relative path in the same root; must not already exist
	To string `json:"to"`
}

// ReplaceEditorDocumentRequest
type ReplaceEditorDocumentRequest struct {
	// Invoking replica's state vector, which requests an exact command transition for its document undo history; null records no transition.
	HistoryVector []byte `json:"history_vector"`
	// Focused chat when this change was authored; null when unaffiliated. The host records the chat's turn at acceptance and drops a chat it cannot place instead of refusing the change.
	SessionID   *string `json:"session_id"`
	OperationID string  `json:"operation_id"`
	// Invoking editor client.
	ClientID string `json:"client_id"`
	// Document revision the update was authored against.
	ExpectedRevision int64 `json:"expected_revision"`
	// Replacement draft text; CRLF is normalized to LF. Decoded text must fit 8 MiB and the file serialized with the document encoding and requested line endings must fit 4 MiB. Invalid or oversize content returns 400 invalid_request without changing the draft or revision. The JSON envelope permits escaping of the full supported content.
	Content string `json:"content"`
	// Line ending to use on save.
	EOL string `json:"eol"`
	// Whether the draft represents mixed line endings.
	MixedEOL bool `json:"mixed_eol"`
}

// ReplaceEditorDocumentRetentionRequest
type ReplaceEditorDocumentRetentionRequest struct {
	ClientID string `json:"client_id"`
	// Live client identities from the native window inventory or browser window locks. Must include client_id. Only orphan references in the calling client’s window or browser namespace are swept; omit until the inventory is ready.
	RetainedClients []string `json:"retained_clients"`
	DocumentIds     []string `json:"document_ids"`
}

// ReplaceManagedSecretValueRequest Puts new bytes behind an unchanged reference, or restores a capability whose stored value went missing. Supported agent tooling already holding the reference keeps working. Only Painted Wolf's protected copy changes: this does not modify a project file, rotate an external credential, revoke it, or activate the replacement at its provider. The replaced value is retired, not deleted: its bytes stay readable to provider screening so a credential the agent has already seen keeps being redacted, but a retired value never resolves again.
type ReplaceManagedSecretValueRequest struct {
	// The new credential. Never echoed back. Named so the host's own request-capture scrubber redacts it by name. It holds the same length floor as minting: a replacement the screen could not recognize would silently retire a protection that worked.
	SecretValue string `json:"secret_value"`
}

// RepoBrief Project orientation summary — languages detected by content via enry, listed in dominant-bytes order.
type RepoBrief struct {
	Languages   []string   `json:"languages"`
	FileCount   int        `json:"file_count"`
	Layout      RepoLayout `json:"layout,omitempty"`
	GeneratedAt time.Time  `json:"generated_at"`
	// The orientation is warming or belongs to an older observed source generation.
	Refreshing bool `json:"refreshing,omitempty"`
	// The observed file population has incomplete coverage, including settled budget omissions or failures. Zero files cannot establish an empty repository when this is true.
	Incomplete bool `json:"incomplete,omitempty"`
}

// RepoLayout Orientation-tier layout sample — Files for tiny repos, TopLevel for small/medium.
type RepoLayout struct {
	// Tiny tier — full repo-relative file paths.
	Files []string `json:"files,omitempty"`
	// Small/medium tier — top-level dirs (trailing /) and promoted root files.
	TopLevel []string `json:"top_level,omitempty"`
}

// ResolveEditorDocumentRequest
type ResolveEditorDocumentRequest struct {
	// Invoking replica's state vector. Requests an exact command transition for its document undo history.
	HistoryVector []byte `json:"history_vector,omitempty"`
	// Focused chat when this change was authored; omitted when unaffiliated. The host records the chat's turn at acceptance and drops a chat it cannot place instead of refusing the change.
	SessionID        string `json:"session_id,omitempty"`
	ClientID         string `json:"client_id"`
	OperationID      string `json:"operation_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	DiskSHA256       string `json:"disk_sha256"`
	Content          string `json:"content"`
	EOL              string `json:"eol"`
}

// ResolveMessageNavigationRequest
type ResolveMessageNavigationRequest struct {
	MessageID     string `json:"message_id"`
	ContentSHA256 string `json:"content_sha256"`
	ReferenceID   string `json:"reference_id,omitempty"`
	// Validate this candidate of the stored ambiguous reference; requires reference_id.
	CandidateIndex *int `json:"candidate_index,omitempty"`
}

// ResolveSocketGrantRequest Resolve one absolute socket path for the deliberate Settings create flow before confirmation. No grant is written.
type ResolveSocketGrantRequest struct {
	SocketPath string `json:"socket_path"`
}

// ResolveSocketGrantResponse
type ResolveSocketGrantResponse struct {
	ApprovedPath       string                    `json:"approved_path"`
	ResolvedPath       string                    `json:"resolved_path"`
	EffectiveAuthority SocketCapabilityAuthority `json:"effective_authority"`
	AuthorityWarning   string                    `json:"authority_warning"`
}

// ResolveUserFeedbackRequest
type ResolveUserFeedbackRequest struct {
	ExpectedRevision int64                       `json:"expected_revision"`
	Response         string                      `json:"response"`
	Secrets          []PromptSecretReferencePart `json:"secrets,omitempty"`
}

// ResolveUserSecretRequest
type ResolveUserSecretRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
	// Raw value delivered only to protected storage; never returned. The floor is the length the outbound screen can recognize; a shorter answer is refused rather than protected in name only.
	SecretValue string `json:"secret_value"`
}

// ResolveWorkflowDecisionRequest
type ResolveWorkflowDecisionRequest struct {
	ExpectedRevision int64                       `json:"expected_revision"`
	Choice           string                      `json:"choice,omitempty"`
	Choices          []string                    `json:"choices,omitempty"`
	Comment          string                      `json:"comment,omitempty"`
	Secrets          []PromptSecretReferencePart `json:"secrets,omitempty"`
}

// RetainedComparisonSource
type RetainedComparisonSource struct {
	Kind       string `json:"kind"`
	ViewID     string `json:"view_id"`
	Comparison string `json:"comparison"`
	RootID     string `json:"root_id,omitempty"`
	Path       string `json:"path,omitempty"`
}

// RetainedReplica State the client already holds, from a live replica or from the synchronized checkpoint it preserved. When it names the current document and epoch, crdt_update carries only the state missing from state_vector and base_content is omitted for a matching base_sha256 (a client without the saved text sends an empty hash, which never matches); otherwise the response is a full snapshot.
type RetainedReplica struct {
	DocumentID  string `json:"document_id"`
	Epoch       int64  `json:"epoch"`
	StateVector []byte `json:"state_vector"`
	BaseSHA256  string `json:"base_sha256"`
}

// RevertEditorDocumentChangeRequest
type RevertEditorDocumentChangeRequest struct {
	// Invoking replica's state vector. Requests an exact command transition for its document undo history.
	HistoryVector []byte `json:"history_vector,omitempty"`
	// Focused chat when this change was authored; omitted when unaffiliated. The host records the chat's turn at acceptance and drops a chat it cannot place instead of refusing the change.
	SessionID   string `json:"session_id,omitempty"`
	ClientID    string `json:"client_id"`
	OperationID string `json:"operation_id"`
	Epoch       int64  `json:"epoch"`
}

// ReviewSettingsResponse
type ReviewSettingsResponse struct {
	Scope       SettingsScope       `json:"scope"`
	ReviewPaths []ContentReviewRule `json:"review_paths"`
	MergedFrom  []string            `json:"merged_from"`
}

// ReviewedComparisonSource
type ReviewedComparisonSource struct {
	Kind                   string `json:"kind"`
	FileID                 string `json:"file_id"`
	ReviewedThroughOrdinal int64  `json:"reviewed_through_ordinal"`
}

// RevokeApprovalGrantsRequest One bulk revoke for the Saved approvals group actions (revoke all, clear expired, revoke missing). Each id resolves independently. Accepts grant_ lease ids and quiet_ ask-quiet ids.
type RevokeApprovalGrantsRequest struct {
	IDs []string `json:"ids"`
}

// RevokeApprovalGrantsResponse
type RevokeApprovalGrantsResponse struct {
	Results []ApprovalGrantRevokeResult `json:"results"`
}

// RevokeElevatedAccessResponse
type RevokeElevatedAccessResponse struct {
	Results   []ElevatedAccessRevokeResult `json:"results"`
	Remaining ElevatedAccessSummary        `json:"remaining"`
}

// RewindIssue
type RewindIssue struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
	// Structured reason the host cannot safely reverse a source effect.
	Code string `json:"code"`
}

// RewindPreviewFile
type RewindPreviewFile struct {
	RootID     string `json:"root_id"`
	Path       string `json:"path"`
	TargetPath string `json:"target_path"`
}

// RewindPreviewRequest
type RewindPreviewRequest struct {
	MessageID string `json:"message_id"`
}

// RewindPreviewResponse
type RewindPreviewResponse struct {
	PlanDigest            string              `json:"plan_digest"`
	Files                 []RewindPreviewFile `json:"files"`
	Issues                []RewindIssue       `json:"issues"`
	TruncatedMessageCount int                 `json:"truncated_message_count"`
}

// RewindSessionRequest
type RewindSessionRequest struct {
	// Durable identity for this rewind; exact retries replay the committed result.
	OperationID string `json:"operation_id"`
	// The visible user message to rewind to. Only a user ask that is not an internal host kick, workflow boundary, or continuation is eligible; anything else is rejected with rewind_anchor_ineligible.
	MessageID string `json:"message_id"`
	// Digest returned by the preview; a changed plan is rejected.
	PlanDigest string     `json:"plan_digest"`
	Mode       RewindMode `json:"mode,omitempty"`
}

// RewindSessionResponse
type RewindSessionResponse struct {
	// Root-relative paths changed by reversing the entire selected suffix. Missing exact history and conflicting later contributions block the whole rewind before files or transcript rows change.
	RestoredPaths []string `json:"restored_paths"`
	// Transcript rows removed, counting the anchor itself
	TruncatedMessageCount int `json:"truncated_message_count"`
	// The anchor ask's user-authoritative prose only (never attachment fences). Den's Edit flow preloads the composer from this rather than reading the row it just asked the host to delete.
	RestoredPrompt string `json:"restored_prompt,omitempty"`
	// The anchor's mixed-origin content parts so Edit can restage attachment and reference chips after the transcript row is removed.
	RestoredContentParts []MessageContentPart `json:"restored_content_parts,omitempty"`
	// Visual artifact ids linked on the anchor ask for Edit restage.
	RestoredArtifactIDs []string `json:"restored_artifact_ids,omitempty"`
}

// SaveEditorDocumentRequest
type SaveEditorDocumentRequest struct {
	// Focused chat when this change was authored; omitted when unaffiliated. The host records the chat's turn at acceptance and drops a chat it cannot place instead of refusing the change.
	SessionID string `json:"session_id,omitempty"`
	// Invoking editor client.
	ClientID string `json:"client_id"`
	// Document revision the command was authored against.
	ExpectedRevision int64 `json:"expected_revision"`
	// Stable mutation identity; an exact retry returns the original saved document and reusing this id with different save input returns 409 idempotency_conflict.
	OperationID string `json:"operation_id"`
}

// ScanAgentBudget
type ScanAgentBudget struct {
	MinSeverity          string `json:"min_severity,omitempty"`
	MaxHintsPerInjection int    `json:"max_hints_per_injection,omitempty"`
	DedupeBy             string `json:"dedupe_by,omitempty"`
}

// ScanDelta Comparison of a path-scoped scan with its base generation. Complete comparisons contain counts; unavailable comparisons contain a reason and no counts. Unavailable history does not reduce current scan coverage.
type ScanDelta struct {
	BaseSnapshotID    string           `json:"base_snapshot_id"`
	Status            string           `json:"status"`
	Counts            *ScanDeltaCounts `json:"counts,omitempty"`
	UnavailableReason string           `json:"unavailable_reason,omitempty"`
	UnavailablePaths  []string         `json:"unavailable_paths,omitempty"`
}

// ScanDeltaCounts
type ScanDeltaCounts struct {
	Introduced int `json:"introduced"`
	Fixed      int `json:"fixed"`
	Persisted  int `json:"persisted"`
}

// ScanExecutionManifest
type ScanExecutionManifest struct {
	SchemaVersion         string            `json:"schema_version"`
	ScannerID             string            `json:"scanner_id"`
	Engine                string            `json:"engine"`
	EngineVersion         string            `json:"engine_version,omitempty"`
	EngineSHA256          string            `json:"engine_sha256,omitempty"`
	Driver                string            `json:"driver"`
	ScopeKind             string            `json:"scope_kind"`
	ParserID              string            `json:"parser_id,omitempty"`
	MapperID              string            `json:"mapper_id,omitempty"`
	DefinitionFingerprint string            `json:"definition_fingerprint"`
	RulesSHA256           string            `json:"rules_sha256,omitempty"`
	ExclusionsSHA256      string            `json:"exclusions_sha256,omitempty"`
	FingerprintScheme     string            `json:"fingerprint_scheme"`
	Runtime               ScanRuntimePolicy `json:"runtime"`
}

// ScanGuidanceSummary
type ScanGuidanceSummary struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
	Severity string `json:"severity,omitempty"`
	RuleID   string `json:"rule_id,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Count    int    `json:"count,omitempty"`
}

// ScanIgnoredFinding A finding one of the project's ignore entries covered. The finding stays in the scan's set; this says which decision kept it out of the agent's view, and why.
type ScanIgnoredFinding struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	RuleID      string `json:"rule_id,omitempty"`
	File        string `json:"file,omitempty"`
	EntryID     string `json:"entry_id,omitempty"`
	// The predicates the entry decided on, in the ignore file's own vocabulary, so a reader can find the entry without opening it.
	MatchedOn string `json:"matched_on,omitempty"`
	Reason    string `json:"reason"`
	ExpiresOn string `json:"expires_on,omitempty"`
}

// ScanProgress How far a chunked scan has come. An engine receives the generation's files in bounded invocations; `completed` counts the invocations done out of `chunks`, over `files` in total. Present only while a scan runs in more than one invocation.
type ScanProgress struct {
	Chunks    int `json:"chunks"`
	Completed int `json:"completed"`
	Files     int `json:"files"`
}

// ScanQueryRequest
type ScanQueryRequest struct {
	// Normalized finding level (critical, high, medium, low, info).
	Level string `json:"level,omitempty"`
	// properties.lycaon.kind (sast, sca, secret, container, custom).
	Kind string `json:"kind,omitempty"`
	// OSV, CVE, or GHSA identifier.
	AdvisoryID string `json:"advisory_id,omitempty"`
	// Exact fingerprints.primary match.
	Fingerprint string `json:"fingerprint,omitempty"`
	RuleID      string `json:"rule_id,omitempty"`
	// Location uri prefix.
	Path string `json:"path,omitempty"`
	// Hint code (properties.lycaon.hint_code).
	Code string `json:"code,omitempty"`
	// Opaque pagination cursor.
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Dedupe bool   `json:"dedupe,omitempty"`
	// Only findings this scanner series first observed at or after this time.
	IntroducedSinceAt *time.Time `json:"introduced_since_at,omitempty"`
	// Also return findings this series recorded as fixed at or after this time, in fixed_findings.
	FixedSinceAt *time.Time `json:"fixed_since_at,omitempty"`
}

// ScanQueryResponse
type ScanQueryResponse struct {
	ScanID     string                `json:"scan_id"`
	Findings   []SecurityFinding     `json:"findings,omitempty"`
	Guidance   []ScanGuidanceSummary `json:"guidance"`
	TotalMatch int                   `json:"total_match"`
	Truncated  bool                  `json:"truncated,omitempty"`
	NextCursor string                `json:"next_cursor,omitempty"`
	// Findings fixed since the requested time, as they were last observed.
	FixedFindings []SecurityFinding `json:"fixed_findings,omitempty"`
}

// ScanRuntimePolicy
type ScanRuntimePolicy struct {
	// Runtime after which the scan is reported as long-running but continues.
	SoftLimitMs int `json:"soft_limit_ms"`
	// Optional terminal runtime ceiling for one immutable generation. Zero allows the scan to finish without a runtime deadline; owner cancellation still applies.
	HardLimitMs int `json:"hard_limit_ms"`
	// Weighted CPU admission units reserved while the scanner runs.
	CPUUnits int `json:"cpu_units"`
	// Scanner-internal worker limit.
	Parallelism int `json:"parallelism"`
}

// ScanWarning
type ScanWarning struct {
	Kind    ScanWarningKind `json:"kind"`
	RuleID  string          `json:"rule_id,omitempty"`
	File    string          `json:"file,omitempty"`
	Message string          `json:"message,omitempty"`
	// Structured construct code from the engine or source parser; never inferred from diagnostic text.
	Construct   string `json:"construct,omitempty"`
	StartLine   int    `json:"start_line,omitempty"`
	StartColumn int    `json:"start_column,omitempty"`
}

// ScanWarningSummary
type ScanWarningSummary struct {
	Kind  ScanWarningKind `json:"kind"`
	Count int             `json:"count"`
	Files int             `json:"files"`
	Rules int             `json:"rules"`
}

// ScannerCatalogEntry
type ScannerCatalogEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// The CLI the user must install; the host ships no scanner binaries
	Binary     string         `json:"binary"`
	Categories []ScanCategory `json:"categories"`
	Hint       string         `json:"hint,omitempty"`
	// Command that installs the binary, e.g. brew install trivy
	Install string `json:"install,omitempty"`
	DocsURL string `json:"docs_url,omitempty"`
	// Env var names the tool needs (never values)
	Env []string `json:"env,omitempty"`
	// Argv joined without expanding tokens
	CommandSummary string `json:"command_summary,omitempty"`
	// Already present in the user scanner catalog
	Added bool `json:"added"`
	// PATH hit; the install check is the authoritative probe
	BinaryFound bool `json:"binary_found"`
}

// ScannerCatalogResponse
type ScannerCatalogResponse struct {
	Scanners []ScannerCatalogEntry `json:"scanners"`
}

// ScannerCheckResponse
type ScannerCheckResponse struct {
	Rows []ScannerCheckRow `json:"rows"`
}

// ScannerCheckRow
type ScannerCheckRow struct {
	ScannerID string `json:"scanner_id"`
	OK        bool   `json:"ok"`
	// Tool version line on success, or the failure reason
	Detail string `json:"detail,omitempty"`
	// Separates not-installed from installed-but-broken
	BinaryFound bool `json:"binary_found"`
}

// ScannerListResponse
type ScannerListResponse struct {
	Scanners []ScannerSummary `json:"scanners"`
	// Rows of the user or project scanners.yaml the merge refused, keyed by scanner id; a row that named no id is keyed by the empty string. A refused row changed nothing.
	Rejected map[string]ScannerRejectedRow `json:"rejected"`
	// Device path hint for scanners.yaml (no secrets)
	UserScannersPath string `json:"user_scanners_path,omitempty"`
}

// ScannerRejectedRow
type ScannerRejectedRow struct {
	// The scanner id the row named. Absent when the row omitted one.
	ID string `json:"id,omitempty"`
	// Why the row was refused, from the closed set shared with detection packs. Clients branch on this rather than on detail.
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// ScannerSummary
type ScannerSummary struct {
	ID string `json:"id"`
	// library | bundled | external
	Driver     string         `json:"driver"`
	Engine     string         `json:"engine"`
	ScopeKind  string         `json:"scope_kind"`
	Categories []ScanCategory `json:"categories"`
	// True when this scanner currently occupies its category slot.
	Enabled     bool `json:"enabled"`
	CheckOK     bool `json:"check_ok"`
	BinaryFound bool `json:"binary_found,omitempty"`
	// Optional display label for the external binary
	RequiresBinary string   `json:"requires_binary,omitempty"`
	OutputParser   string   `json:"output_parser,omitempty"`
	CatalogSource  string   `json:"catalog_source"`
	Issues         []string `json:"issues,omitempty"`
	// Argv joined without expanding secrets; never env values
	CommandSummary string            `json:"command_summary,omitempty"`
	Label          string            `json:"label,omitempty"`
	Description    string            `json:"description,omitempty"`
	MapperID       string            `json:"mapper_id,omitempty"`
	Runtime        ScanRuntimePolicy `json:"runtime"`
}

// ScopeComparisonSource
type ScopeComparisonSource struct {
	Kind                     string `json:"kind"`
	FileID                   string `json:"file_id"`
	Baseline                 string `json:"baseline"`
	MarkUserEdits            *bool  `json:"mark_user_edits,omitempty"`
	PresentationAfterOrdinal *int64 `json:"presentation_after_ordinal,omitempty"`
}

// SearchExportRequest Carries the same match flags as SearchRequest so an export reproduces exactly what the search view showed.
type SearchExportRequest struct {
	Query           string             `json:"query"`
	OriginProjectID string             `json:"origin_project_id,omitempty"`
	Format          SearchExportFormat `json:"format"`
	Regex           bool               `json:"regex,omitempty"`
	CaseSensitive   bool               `json:"case_sensitive,omitempty"`
	WholeWord       bool               `json:"whole_word,omitempty"`
	Include         []string           `json:"include,omitempty"`
	Exclude         []string           `json:"exclude,omitempty"`
}

// SearchFacet
type SearchFacet struct {
	Key    string             `json:"key"`
	Values []SearchFacetValue `json:"values"`
}

// SearchFacetValue
type SearchFacetValue struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// SearchHistogramBucket
type SearchHistogramBucket struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

// SearchHit
type SearchHit struct {
	// Stable identity for the same indexed row or source coordinate across pages and refreshed generations.
	HitID           string  `json:"hit_id"`
	HitKind         string  `json:"hit_kind"`
	Source          string  `json:"source"`
	Score           float64 `json:"score,omitempty"`
	SessionID       string  `json:"session_id,omitempty"`
	SourceRef       string  `json:"source_ref,omitempty"`
	LegID           string  `json:"leg_id,omitempty"`
	Handle          string  `json:"handle,omitempty"`
	ParentSessionID string  `json:"parent_session_id,omitempty"`
	WorkerID        string  `json:"worker_id,omitempty"`
	ProjectID       string  `json:"project_id"`
	// Attached root for live file and code hits, when known.
	RootID      string `json:"root_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"`
	Snippet     string `json:"snippet,omitempty"`
	// Scannable host-projected label.
	Title string `json:"title"`
	// Host-projected location or source context.
	Context string `json:"context,omitempty"`
	// Repo-relative file path for file-anchored hits.
	Path string `json:"path,omitempty"`
	// 1-based line within path, when known.
	Line int `json:"line,omitempty"`
	// External URL for web hits.
	URL        string           `json:"url,omitempty"`
	Trust      string           `json:"trust,omitempty"`
	Verified   *bool            `json:"verified,omitempty"`
	HintCode   string           `json:"hint_code,omitempty"`
	SymbolKind SourceSymbolKind `json:"symbol_kind,omitempty"`
	// Code point ranges of `title` the query matched; symbol hits carry them.
	TitleHighlights []SourceSearchHighlight `json:"title_highlights,omitempty"`
	CreatedAt       time.Time               `json:"created_at,omitempty"`
}

// SearchInterpretation
type SearchInterpretation struct {
	Scope    SearchScopeMode           `json:"scope"`
	Slug     string                    `json:"slug,omitempty"`
	FTSTerms []string                  `json:"fts_terms,omitempty"`
	Filters  []SearchInterpretedFilter `json:"filters,omitempty"`
	// The content scan skipped dependency and build trees; an explicit path filter or include glob re-admits them.
	DependencyTreesExcluded bool `json:"dependency_trees_excluded,omitempty"`
}

// SearchInterpretedFilter One recognized filter with its polarity preserved
type SearchInterpretedFilter struct {
	Field   string `json:"field"`
	Value   string `json:"value"`
	Negated bool   `json:"negated,omitempty"`
}

// SearchIssue
type SearchIssue struct {
	Executor string            `json:"executor"`
	Reason   SearchIssueReason `json:"reason"`
	Limit    int               `json:"limit,omitempty"`
	// Affected-item count for reasons that carry one (files_skipped, catalog_warming, catalog_incomplete, catalog_bounded, index_warming, symbol_budget, and result_limit from the symbol leg)
	Count int `json:"count,omitempty"`
	// Underlying failure for executor_error
	Message string `json:"message,omitempty"`
}

// SearchReplaceApplyFile
type SearchReplaceApplyFile struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	// 0-based indices into the preview hunk list for this file
	Hunks []int `json:"hunks"`
}

// SearchReplaceApplyRequest
type SearchReplaceApplyRequest struct {
	// Client-generated idempotency key for the complete multi-file replace command; an exact retry returns the original outcome and conflicting reuse returns 409 idempotency_conflict
	OperationID     string                   `json:"operation_id"`
	Query           string                   `json:"query"`
	Replacement     string                   `json:"replacement"`
	OriginProjectID string                   `json:"origin_project_id"`
	Regex           bool                     `json:"regex,omitempty"`
	CaseSensitive   bool                     `json:"case_sensitive,omitempty"`
	WholeWord       bool                     `json:"whole_word,omitempty"`
	Include         []string                 `json:"include,omitempty"`
	Exclude         []string                 `json:"exclude,omitempty"`
	Files           []SearchReplaceApplyFile `json:"files"`
}

// SearchReplaceApplyResponse
type SearchReplaceApplyResponse struct {
	Files []SearchReplaceFileOutcome `json:"files"`
	// Source-ledger batch shared by every applied file; used for one-click revert
	BatchID string `json:"batch_id"`
}

// SearchReplaceFileOutcome
type SearchReplaceFileOutcome struct {
	RootID  string `json:"root_id"`
	Path    string `json:"path"`
	Applied bool   `json:"applied"`
	Skipped bool   `json:"skipped"`
	Reason  string `json:"reason,omitempty"`
	Matches int    `json:"matches,omitempty"`
}

// SearchReplaceFilePreview
type SearchReplaceFilePreview struct {
	RootID string              `json:"root_id"`
	Path   string              `json:"path"`
	SHA256 string              `json:"sha256"`
	Hunks  []SearchReplaceHunk `json:"hunks"`
}

// SearchReplaceHunk
type SearchReplaceHunk struct {
	// 1-based first line of the matched span
	Line int `json:"line"`
	// 1-based last line of the matched span (equals line for single-line matches)
	EndLine int `json:"end_line"`
	// Full text of the spanned lines before the replacement
	Before string `json:"before"`
	// Full text of the spanned lines after the replacement
	After   string                   `json:"after"`
	Context SearchReplaceHunkContext `json:"context"`
}

// SearchReplacePreviewRequest
type SearchReplacePreviewRequest struct {
	Query           string   `json:"query"`
	Replacement     string   `json:"replacement"`
	OriginProjectID string   `json:"origin_project_id,omitempty"`
	Regex           bool     `json:"regex,omitempty"`
	CaseSensitive   bool     `json:"case_sensitive,omitempty"`
	WholeWord       bool     `json:"whole_word,omitempty"`
	Include         []string `json:"include,omitempty"`
	Exclude         []string `json:"exclude,omitempty"`
}

// SearchReplacePreviewResponse
type SearchReplacePreviewResponse struct {
	State SearchReplacePreviewState  `json:"state"`
	Files []SearchReplaceFilePreview `json:"files"`
	// A file or hunk limit bounded the preview.
	Truncated bool          `json:"truncated"`
	Issues    []SearchIssue `json:"issues"`
}

// SearchRequest
type SearchRequest struct {
	Query           string       `json:"query"`
	OriginProjectID string       `json:"origin_project_id,omitempty"`
	Cursor          string       `json:"cursor,omitempty"`
	Limit           int          `json:"limit,omitempty"`
	Budget          SearchBudget `json:"budget,omitempty"`
	// When true, treat the code pattern as a Go regexp
	Regex         bool `json:"regex,omitempty"`
	CaseSensitive bool `json:"case_sensitive,omitempty"`
	WholeWord     bool `json:"whole_word,omitempty"`
	// Root-relative globs that must match (`**/*.go`)
	Include []string `json:"include,omitempty"`
	// Root-relative globs that exclude a path (wins over include)
	Exclude []string `json:"exclude,omitempty"`
}

// SearchResponse
type SearchResponse struct {
	Hits   []SearchHit        `json:"hits"`
	Status SearchResultStatus `json:"status"`
	// True only when every executor completed without a limit or error.
	Exhaustive bool `json:"exhaustive"`
	// Hits available in this generation before transport paging.
	TotalHits     int                 `json:"total_hits"`
	CountRelation SearchCountRelation `json:"count_relation"`
	// False when facets describe a limited or partial generation.
	FacetsExhaustive bool                    `json:"facets_exhaustive"`
	Facets           []SearchFacet           `json:"facets,omitempty"`
	Histogram        []SearchHistogramBucket `json:"histogram,omitempty"`
	NextCursor       string                  `json:"next_cursor,omitempty"`
	Interpreted      SearchInterpretation    `json:"interpreted,omitempty"`
	Issues           []SearchIssue           `json:"issues,omitempty"`
}

// SecretIgnoreCandidate
type SecretIgnoreCandidate struct {
	Value string `json:"value"`
}

// SecretIgnoreEntry
type SecretIgnoreEntry struct {
	ID        string `json:"id"`
	Value     string `json:"value"`
	Reason    string `json:"reason"`
	ExpiresOn string `json:"expires_on,omitempty"`
}

// SecretIgnoreList
type SecretIgnoreList struct {
	Rules []SecretIgnoreRule `json:"rules"`
}

// SecretIgnoreRule
type SecretIgnoreRule struct {
	ID        string `json:"id"`
	Value     string `json:"value"`
	Reason    string `json:"reason"`
	ExpiresOn string `json:"expires_on,omitempty"`
	RootID    string `json:"root_id"`
	Path      string `json:"path"`
	Status    string `json:"status"`
}

// SecretInputMeta
type SecretInputMeta struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose,omitempty"`
	// Chat scope lets only this chat and its workers use the reference; project scope lets later chats use it too.
	Scope string `json:"scope"`
	// Optional lifetime before the agent can no longer substitute the capability. Authenticated human reveal remains available for recovery, and this does not alter an external credential.
	AgentUseTTLMs int64 `json:"agent_use_ttl_ms,omitempty"`
}

// SecretMarkPreview What marking a range would capture. The host decides eligibility so the client never has to guess what will be refused after a person has already named the capability.
type SecretMarkPreview struct {
	Eligible bool `json:"eligible"`
	// Present only when eligible is false. too_short is the mint floor, set at the shortest length a real credential takes so a PIN is accepted: only one-to-three character selections are refused, because those occur constantly in ordinary text and an exact match would redact unrelated prose rather than protect anything. already_protected means the selection holds bytes a managed secret in this project already protects; a second capability would duplicate that secret rather than protect anything new.
	Reason     string `json:"reason,omitempty"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
	RuneLength int    `json:"rune_length"`
	ByteLength int    `json:"byte_length"`
	// Synthetic character classes and run lengths for the captured bytes.
	Shape           string `json:"shape,omitempty"`
	TrimmedLeading  int    `json:"trimmed_leading,omitempty"`
	TrimmedTrailing int    `json:"trimmed_trailing,omitempty"`
}

// SecretMarkRange A rune range in one editor document. The client sends offsets, never bytes: the host slices the value from its own copy of the draft, so a credential never travels in a request body, an access log, or an error path.
type SecretMarkRange struct {
	// Document revision the range was selected against. A mismatch is refused.
	Revision int64 `json:"revision"`
	Start    int   `json:"start"`
	End      int   `json:"end"`
	// Strip surrounding whitespace and one matched quote layer before capturing. Omitted means trim: the quote around a credential is syntax in the file, not part of the value that travels.
	Trim *bool `json:"trim,omitempty"`
}

// SecretMarkRequest Mint a project-scoped managed secret from bytes already present in a project file.
type SecretMarkRequest struct {
	Range   SecretMarkRange `json:"range"`
	Name    string          `json:"name"`
	Purpose string          `json:"purpose"`
	// Idempotency key. Repeating a mark with the same key returns the same capability.
	OperationID string `json:"operation_id,omitempty"`
}

// SecretScreen Provenance for one completed text screen. Presence means screening ran; absence means screening was unavailable. An empty span list does not say the text is free of credentials — no pattern catalog recognizes every one. Spans use rune offsets into the exact text carried beside this screen.
type SecretScreen struct {
	// Whether the screen covered only a prefix of the document.
	Truncated bool `json:"truncated"`
	// Bytes actually screened.
	ScreenedBytes int `json:"screened_bytes,omitempty"`
	// Editor-document revision the spans were computed against. Omitted for immutable comparison content.
	ScreenedRevision int64 `json:"screened_revision,omitempty"`
	// Vendored detection catalog the screen ran against. Empty when no vendor catalog is loaded.
	CatalogVersion string       `json:"catalog_version,omitempty"`
	Spans          []SecretSpan `json:"spans,omitempty"`
}

// SecretSpan One screened range in an editor document. Offsets are rune positions into the document draft, matching the redaction-span convention. A span never carries the matched bytes; clients render from this list rather than by searching content, and must not infer a span from text.
type SecretSpan struct {
	// Rune offset of the first matched character in the draft.
	Start int `json:"start"`
	// Rune offset one past the last matched character.
	End   int             `json:"end"`
	State SecretSpanState `json:"state"`
	// Catalog rule identity, or the managed-capability evidence id.
	RuleID string `json:"rule_id"`
	// Human-readable rule name for presentation.
	RuleTitle string `json:"rule_title,omitempty"`
	// Managed capability token. Present only when state is `tracked`. A project screen carries it for every live capability, whichever chat created it; a jar entry has none.
	Reference string `json:"reference,omitempty"`
	// Synthetic character classes and run lengths — a receipt for which bytes matched, never the bytes.
	Shape string `json:"shape,omitempty"`
}

// SecurityBaseline The generation automatic scanning measures change against.
type SecurityBaseline struct {
	SnapshotID            string    `json:"snapshot_id"`
	CreatedAt             time.Time `json:"created_at"`
	FileCount             int       `json:"file_count"`
	UnobservedDirectories int       `json:"unobserved_directories"`
}

// SecurityFinding
type SecurityFinding struct {
	RuleID       string                      `json:"rule_id"`
	Level        FindingLevel                `json:"level"`
	Message      string                      `json:"message"`
	Locations    []SecurityFindingLocation   `json:"locations"`
	Dataflow     *SecurityFindingDataflow    `json:"dataflow,omitempty"`
	Fingerprints SecurityFindingFingerprints `json:"fingerprints"`
	Tool         ToolDescriptor              `json:"tool"`
	Properties   *SecurityFindingProperties  `json:"properties,omitempty"`
	History      *SecurityFindingHistory     `json:"history,omitempty"`
}

// SecurityFindingCallTrace
type SecurityFindingCallTrace struct {
	Location      SecurityFindingLocation   `json:"location"`
	Intermediates []SecurityFindingLocation `json:"intermediates,omitempty"`
	Callee        *SecurityFindingCallTrace `json:"callee,omitempty"`
}

// SecurityFindingDataflow Engine-reported flow evidence. Presence does not establish path feasibility or exploitability.
type SecurityFindingDataflow struct {
	Source        *SecurityFindingCallTrace `json:"source,omitempty"`
	Intermediates []SecurityFindingLocation `json:"intermediates,omitempty"`
	Sink          *SecurityFindingCallTrace `json:"sink,omitempty"`
}

// SecurityFindingFingerprints
type SecurityFindingFingerprints struct {
	Primary string `json:"primary"`
}

// SecurityFindingHistory The generation that first showed this finding to its scanner series. A full pass that re-observes a known finding keeps the history its first observation gave it.
type SecurityFindingHistory struct {
	IntroducedAt         time.Time `json:"introduced_at"`
	IntroducedSnapshotID string    `json:"introduced_snapshot_id,omitempty"`
	IntroducedScanID     string    `json:"introduced_scan_id,omitempty"`
}

// SecurityFindingLocation
type SecurityFindingLocation struct {
	URI         string `json:"uri"`
	StartLine   int    `json:"start_line,omitempty"`
	StartColumn int    `json:"start_column,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
	EndColumn   int    `json:"end_column,omitempty"`
}

// SecurityFindingLycaonProperties
type SecurityFindingLycaonProperties struct {
	Kind        FindingKind        `json:"kind,omitempty"`
	Categories  []ScanCategory     `json:"categories,omitempty"`
	Advisory    *AdvisoryRef       `json:"advisory,omitempty"`
	HintCode    string             `json:"hint_code,omitempty"`
	Disposition FindingDisposition `json:"disposition,omitempty"`
	Sources     []string           `json:"sources,omitempty"`
}

// SecurityFindingProperties
type SecurityFindingProperties struct {
	Lycaon *SecurityFindingLycaonProperties `json:"lycaon,omitempty"`
}

// SecurityFullPass One requested full pass. Every member reads every admitted file of the same source generation, so members start together once each scanner has finished earlier work. The pass is finished when no member is waiting and every started member's scan is terminal.
type SecurityFullPass struct {
	// The pass id, which is also its security assessment id once it starts.
	AssessmentID string    `json:"assessment_id"`
	RequestedAt  time.Time `json:"requested_at"`
	// When the members were dispatched together; absent while any member waits.
	StartedAt      *time.Time               `json:"started_at,omitempty"`
	CompletedAt    *time.Time               `json:"completed_at,omitempty"`
	CoverageStatus ScanCoverageStatus       `json:"coverage_status,omitempty"`
	Trigger        ScanTrigger              `json:"trigger,omitempty"`
	Members        []SecurityFullPassMember `json:"members"`
}

// SecurityFullPassMember One scanner of a full pass and how far it has come.
type SecurityFullPassMember struct {
	ScannerID string              `json:"scanner_id"`
	Phase     FullPassMemberPhase `json:"phase"`
	Scan      *CodeScan           `json:"scan,omitempty"`
}

// SecurityOverview What the Security stage states above its findings: what change is measured against, whether a full pass exists, and what is running now.
type SecurityOverview struct {
	ProjectID               string                 `json:"project_id"`
	Enabled                 bool                   `json:"enabled"`
	Coverage                ScanCoverageStatus     `json:"coverage_status,omitempty"`
	Baseline                *SecurityBaseline      `json:"baseline,omitempty"`
	LastFull                *SecurityFullPass      `json:"last_full,omitempty"`
	Running                 *SecurityFullPass      `json:"running,omitempty"`
	Scanners                []SecurityScannerState `json:"scanners"`
	IntroducedSinceBaseline int                    `json:"introduced_since_baseline"`
	FixedSinceBaseline      int                    `json:"fixed_since_baseline"`
}

// SecurityScannerState
type SecurityScannerState struct {
	ID          string         `json:"id"`
	Label       string         `json:"label"`
	Categories  []ScanCategory `json:"categories"`
	Available   bool           `json:"available"`
	Unavailable string         `json:"unavailable_reason,omitempty"`
	LastFullAt  *time.Time     `json:"last_full_at,omitempty"`
	Running     bool           `json:"running"`
	// True once the scanner holds a series for this project, so admitted writes arm it. Automatic scanning is change-driven: an empty delta produces no scan record, so an absence of recent runs under a watching scanner means the tree was quiet, not that scanning lapsed.
	Watching bool `json:"watching"`
	// When this scanner's series last finished a scan of any target.
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	// True when the execution identity the series last covered is not the one this scanner would run now — a rule or engine change since its results were established. Requires positive evidence of drift: a series that has covered nothing has no pass to invalidate, and an identity that cannot be resolved is never reported as a change.
	PassSuperseded bool `json:"pass_superseded"`
}

// SecurityScannersSettingsResponse
type SecurityScannersSettingsResponse struct {
	// Main switch for Security scanners. When false, Context → Security scanners is hidden, project-open and tool/workflow scans do not run, and scan tools reject.
	Enabled    bool     `json:"enabled"`
	MergedFrom []string `json:"merged_from"`
	// Effective landed-change scan scope.
	LandedChangeScope LandedChangeScope `json:"landed_change_scope"`
	// Effective source re-read policy for scan publications.
	SourceVerify SourceVerify `json:"source_verify"`
}

// SelectScannerSlotRequest
type SelectScannerSlotRequest struct {
	// Empty clears the slot
	ScannerID string `json:"scanner_id"`
}

// Session
type Session struct {
	ID string `json:"id"`
	// Display title (auto until the user renames); omitted until set
	Title     string `json:"title,omitempty"`
	ProjectID string `json:"project_id"`
	// Person who started the session. Child sessions share their parent's owner. Read-only; ignored on input.
	OwnerPersonID string `json:"owner_person_id"`
	// Active root for this session; omitted when no roots
	WorkspaceRootID string `json:"workspace_root_id,omitempty"`
	// Computed on read from workspace_root_id or primary root; omitted when project has no roots
	WorkspacePath   string         `json:"workspace_path,omitempty"`
	Posture         SessionPosture `json:"posture"`
	AgentType       string         `json:"agent_type,omitempty"`
	ProviderID      string         `json:"provider_id,omitempty"`
	Model           string         `json:"model,omitempty"`
	Status          SessionStatus  `json:"status"`
	ParentSessionID string         `json:"parent_session_id,omitempty"`
	// Effective worker tool-loop cap. Omitted task budgets default to 12 for read scope and 20 for write scope; maximum 120. The final iteration is prose-only.
	MaxToolLoops         int `json:"max_tool_loops,omitempty"`
	CompactionGeneration int `json:"compaction_generation,omitempty"`
	// Server-derived: true once this session has ingested untrusted external content (web tools, MCP, or web-researcher worker summary). Read-only; ignored on input.
	UntrustedContent bool `json:"untrusted_content,omitempty"`
	// Server-derived 1-based ordinal of the session's newest user turn, and the same number the source ledger stamps on writes made during it. A client addressing a turn (source `baseline=turn:{session_id},{n}`) cites this value rather than counting transcript messages itself; `baseline=turn:{session_id}` lets the host resolve it. 0 means no user turn has started. Read-only; ignored on input.
	CurrentTurn int `json:"current_turn,omitempty"`
	// Server-derived: true while the host holds an admitted human prompt it will run without further human action and whose turn has not begun. That covers a prompt between admission and its turn, a command that ends without a turn while it runs, and next-turn draft items the host will drain, unless the draft is held for editing and the item is not reserved by Send. The gap between one turn's idle boundary and the next turn's busy is therefore never a session at rest. Omitted when false. Read-only; ignored on input.
	PromptPending bool            `json:"prompt_pending,omitempty"`
	UI            *SessionUiState `json:"ui,omitempty"`
	// Set while the session is archived (out of the working set); omitted when active
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
	// Order among the project's pinned chats, lowest first; omitted when unpinned. Archiving the session or moving it to another project unpins it. Read-only; ignored on input.
	PinRank *int `json:"pin_rank,omitempty"`
	// When the person last had this session readable on screen. A turn that completes after this stamp is a result they have not seen, which is what puts the session in the attention view's `finished` class. Omitted until the session has been read. Written by POST /v1/sessions/{id}/seen; ignored on other input.
	SeenAt    *time.Time `json:"seen_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	// When the transcript last gained a visible message, or the creation time before that. Viewing, pinning, renaming, and other record changes leave it alone. Read-only; ignored on input.
	ActivityAt time.Time `json:"activity_at"`
	// Last change to the session record, including lifecycle and read state
	UpdatedAt time.Time `json:"updated_at"`
}

// SessionBackgroundDirectJob
type SessionBackgroundDirectJob struct {
	ProcessID  string                        `json:"process_id"`
	ToolCallID string                        `json:"tool_call_id,omitempty"`
	Status     SessionBackgroundDirectStatus `json:"status"`
}

// SessionBackgroundExecutionJob
type SessionBackgroundExecutionJob struct {
	ProcessID  string                        `json:"process_id"`
	ToolCallID string                        `json:"tool_call_id,omitempty"`
	Capability string                        `json:"capability"`
	Status     SessionBackgroundDirectStatus `json:"status"`
}

// SessionBootstrap
type SessionBootstrap struct {
	// Replay boundary captured before the snapshot was read
	EventCursor string  `json:"event_cursor"`
	Session     Session `json:"session"`
	// Current host work leases for this session. Omitted when no leases remain.
	Activities        []ActivityEvent           `json:"activities,omitempty"`
	Transcript        SessionTranscriptPage     `json:"transcript"`
	Progress          ProgressDigest            `json:"progress"`
	TurnClock         TurnClock                 `json:"turn_clock"`
	Findings          FindingsDigest            `json:"findings"`
	Queue             QueueDraft                `json:"queue"`
	Coordinator       CoordinatorRunContext     `json:"coordinator"`
	Workers           []WorkerTask              `json:"workers"`
	Checkpoints       []CheckpointEvent         `json:"checkpoints"`
	BackgroundOutputs []BackgroundProcessOutput `json:"background_outputs"`
	Previews          []PreviewAttachment       `json:"previews"`
}

// SessionCompactResponse
type SessionCompactResponse struct {
	Generation      int `json:"generation"`
	TokensBefore    int `json:"tokens_before"`
	TokensAfter     int `json:"tokens_after"`
	ChunksCompacted int `json:"chunks_compacted"`
	// Whether a conversation summary was published.
	SessionCompacted bool `json:"session_compacted"`
	// Target in the host transcript estimate units.
	TargetTokens int `json:"target_tokens"`
	// Whether the resulting transcript estimate fits the target.
	TargetMet bool `json:"target_met"`
	// Compaction outcome or reason no session summary was accepted.
	Reason string `json:"reason"`
	// An unchanged unproductive summary attempt was reused without a provider call.
	CacheHit bool `json:"cache_hit"`
	// Ordinary text encoding or estimated; excludes provider framing and media tokens.
	MeasurementMethod string `json:"measurement_method"`
	// Projected text units before compaction, including trust markers and tool arguments.
	TextTokensBefore int `json:"text_tokens_before"`
	// Projected text units after compaction, using the same measurement method.
	TextTokensAfter int `json:"text_tokens_after"`
}

// SessionContextResponse
type SessionContextResponse struct {
	EstimatedTokens      int `json:"estimated_tokens"`
	BudgetRemaining      int `json:"budget_remaining"`
	CompactionGeneration int `json:"compaction_generation"`
	OversizedChunks      int `json:"oversized_chunks"`
}

// SessionEvent
type SessionEvent struct {
	ID string `json:"id"`
	// Associated project; explicit so device-wide subscriptions never infer scope.
	ProjectID string             `json:"project_id"`
	Action    SessionEventAction `json:"action"`
	// Display title (auto until the user renames); omitted until set
	Title       string        `json:"title,omitempty"`
	Status      SessionStatus `json:"status"`
	LastMessage string        `json:"last_message,omitempty"`
	// Server-derived session untrusted-content fact (same semantics as Session.untrusted_content).
	UntrustedContent bool `json:"untrusted_content,omitempty"`
	// Session.current_turn, carried on the event so a client reading a chat's current turn (`baseline=turn:{session_id}`) knows to read it again when a new turn starts, without refetching the session.
	CurrentTurn     int                    `json:"current_turn,omitempty"`
	UI              *SessionUiState        `json:"ui,omitempty"`
	HostError       *SessionHostError      `json:"host_error,omitempty"`
	IdleDisposition SessionIdleDisposition `json:"idle_disposition,omitempty"`
	// Session.prompt_pending as of this event, omitted when false. Every session event states it, so a client applying the newest event by revision never shows a session at rest while an admitted prompt waits to start.
	PromptPending bool `json:"prompt_pending,omitempty"`
}

// SessionHostError
type SessionHostError struct {
	Code            NoticeCode     `json:"code,omitempty"`
	Title           string         `json:"title,omitempty"`
	Message         string         `json:"message,omitempty"`
	SuggestedAction string         `json:"suggested_action,omitempty"`
	Actions         []NoticeAction `json:"actions,omitempty"`
	// Host-declared tier. Always `non_catastrophic` here: a condition that blocks the app cannot be delivered on a session event, because that needs a session the user cannot reach.
	Tier NoticeTier `json:"tier,omitempty"`
	// Host-declared scope. Usually `session` — the enclosing event's `id` names which one — but a condition wider than the chat that raised it (an unconfigured provider, say) is declared at its true scope so it renders once rather than in every chat.
	Scope NoticeScope `json:"scope,omitempty"`
	// Which declared resolution produced `tier`/`scope`.
	Resolution string `json:"resolution,omitempty"`
}

// SessionListPage One seek page of a project's chats plus the total under the same filter
type SessionListPage struct {
	Sessions []SessionSummary `json:"sessions"`
	Total    int              `json:"total"`
	// Opaque cursor for the next page; omitted on the last page
	NextCursor string `json:"next_cursor,omitempty"`
}

// SessionProtectionState Persistent app/session protection chrome. Mediation unavailable and full sandbox bypass are distinct — never one ambiguous uncontained banner.
type SessionProtectionState struct {
	// A startup override removes the app configuration read floor. Protected key material remains subject to its independent rules.
	ControlPlaneReadsAllowed bool `json:"control_plane_reads_allowed,omitempty"`
	// Resolved folders added to default write access by a startup override. Protected paths within these folders remain restricted.
	AdditionalWriteRoots []string `json:"additional_write_roots,omitempty"`
	// Network mediation cannot start. Outbound connections are blocked; commands that do not use the network are unaffected.
	MediationUnavailable bool `json:"mediation_unavailable,omitempty"`
	// Sandbox protections are turned off. Commands may access the machine and network without mediation.
	SandboxBypass bool `json:"sandbox_bypass,omitempty"`
	// Active or reconstructed background direct-IP processes.
	BackgroundDirect []SessionBackgroundDirectJob `json:"background_direct,omitempty"`
	// Processes with approved process control or host execution that have not been observed exiting.
	BackgroundExecution []SessionBackgroundExecutionJob `json:"background_execution,omitempty"`
}

// SessionSummary Metadata-only chat list row; worker child sessions are never listed
type SessionSummary struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	// Person who started the session.
	OwnerPersonID string         `json:"owner_person_id"`
	Title         string         `json:"title,omitempty"`
	Posture       SessionPosture `json:"posture"`
	Status        SessionStatus  `json:"status"`
	ArchivedAt    *time.Time     `json:"archived_at,omitempty"`
	// Order among the project's pinned chats, lowest first; omitted when unpinned
	PinRank      *int      `json:"pin_rank,omitempty"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	// When the transcript last gained a visible message, or the creation time before that
	ActivityAt time.Time `json:"activity_at"`
}

// SessionTranscriptPage
type SessionTranscriptPage struct {
	// One contiguous Ord window in ascending order (default = newest page / live tail)
	Messages []Message `json:"messages"`
	// Durable clocks of the visible user turns opened by messages in this page, keyed by opening message id. Worker transcript pages carry none.
	TurnClocks map[string]TurnClock `json:"turn_clocks"`
	// Decision-engine receipts of the turns opened by messages in this page, keyed by opening message id, each list in recorded order.
	TurnLoads map[string][]TurnLoad `json:"turn_loads"`
	// Transcript mutation clock at read time, taken in the same transaction as the rows; Den installs the page as a baseline and keeps rows whose seq exceeds it
	Watermark int64 `json:"watermark"`
	// Opaque pagination cursor to fetch the previous (older) window of messages. Omitted when no older messages exist.
	BeforeCursor string `json:"before_cursor,omitempty"`
	// Opaque pagination cursor to fetch the next (newer) window of messages. Omitted when no newer messages exist.
	AfterCursor string `json:"after_cursor,omitempty"`
}

// SessionUiState
type SessionUiState struct {
	PendingWorkflowStart *PendingWorkflowStart   `json:"pending_workflow_start,omitempty"`
	Protection           *SessionProtectionState `json:"protection,omitempty"`
}

// SetProviderCredentialRequest
type SetProviderCredentialRequest struct {
	APIKey string `json:"api_key"`
}

// SettingsEvent
type SettingsEvent struct {
	Area   SettingsArea  `json:"area"`
	Scope  SettingsScope `json:"scope"`
	Action string        `json:"action"`
}

// SettingsLimitsPatch Merge selected limits. Omitted fields remain unchanged; null restores the global value for project scope or the built-in default for global scope.
type SettingsLimitsPatch struct {
	// Tool-loop iteration cap per prompt turn.
	MaxIterations *int `json:"max_iterations,omitempty"`
	// Iteration cap for coordinator overlay promote surface.
	OverlayPromoteMaxIterations *int `json:"overlay_promote_max_iterations,omitempty"`
	// Tool results larger than this are truncated before LLM history append.
	MaxToolResultBytes *int `json:"max_tool_result_bytes,omitempty"`
	// When true (default), host may auto-prompt coordinator after worker legs.
	CoordinatorLoop *bool `json:"coordinator_loop,omitempty"`
	// Per workflow run cap on coordinator loop wake cycles.
	MaxCoordinatorLoopCycles *int `json:"max_coordinator_loop_cycles,omitempty"`
	// Wall-clock cap (milliseconds) for one LLM stream completion, in whole seconds.
	LLMTurnTimeoutMs *int `json:"llm_turn_timeout_ms,omitempty"`
	// Wall-clock cap (milliseconds) for one host-initiated coordinator prompt turn, in whole seconds.
	CoordinatorHostTurnTimeoutMs *int `json:"coordinator_host_turn_timeout_ms,omitempty"`
	// Maximum wait() sleep duration (milliseconds) for the coordinator loop, in whole seconds.
	CoordinatorMaxSleepMs *int `json:"coordinator_max_sleep_ms,omitempty"`
	// Wall-clock cap (milliseconds) blocking until parent-session workers finish, in whole seconds.
	AwaitParentWorkersTimeoutMs *int `json:"await_parent_workers_timeout_ms,omitempty"`
	// Worker tool-round ceiling applied when neither the task nor its planned leg sets max_tool_loops. The host never raises a ceiling on its own; a worker asks with request_budget and the coordinator grants with extend_worker_budget.
	WorkerToolBudgetDefault *int `json:"worker_tool_budget_default,omitempty"`
	// Smallest worker tool-round ceiling the coordinator may set (host floor is 2).
	WorkerToolBudgetMin *int `json:"worker_tool_budget_min,omitempty"`
	// Largest worker tool-round ceiling the coordinator may set or grant.
	WorkerToolBudgetMax *int `json:"worker_tool_budget_max,omitempty"`
	// Opt-in per-session nano-USD spend limit (enforced only when spend_ceiling_enabled is true and spend is priced).
	SessionSpendCeilingNanoUSD *int64 `json:"session_spend_ceiling_nano_usd,omitempty"`
	// Share of the session spend limit at which the host emits a one-shot warning.
	SpendWarningRatio *float64 `json:"spend_warning_ratio,omitempty"`
	// When true and session_spend_ceiling_nano_usd is greater than zero, land and pause the loop once estimated session spend reaches the ceiling.
	SpendCeilingEnabled *bool `json:"spend_ceiling_enabled,omitempty"`
	// When true (default), a coordinator that crosses the ceiling while already running receives one final tool-capable wind-down round. A response without tool calls is final; after tool calls, the host requests one prose-only closeout. New prompts and worker sessions remain blocked at the ceiling.
	SpendSoftStop *bool `json:"spend_soft_stop,omitempty"`
}

// SettingsLimitsResponse
type SettingsLimitsResponse struct {
	Scope SettingsScope `json:"scope"`
	// Tool-loop iteration cap per prompt turn.
	MaxIterations int `json:"max_iterations"`
	// Iteration cap for coordinator overlay promote surface.
	OverlayPromoteMaxIterations int `json:"overlay_promote_max_iterations"`
	// Tool results larger than this are truncated before LLM history append.
	MaxToolResultBytes int `json:"max_tool_result_bytes"`
	// When true (default), host may auto-prompt coordinator after worker legs.
	CoordinatorLoop *bool `json:"coordinator_loop,omitempty"`
	// Per workflow run cap on coordinator loop wake cycles.
	MaxCoordinatorLoopCycles int `json:"max_coordinator_loop_cycles,omitempty"`
	// Wall-clock cap (milliseconds) for one LLM stream completion, in whole seconds.
	LLMTurnTimeoutMs int `json:"llm_turn_timeout_ms"`
	// Wall-clock cap (milliseconds) for one host-initiated coordinator prompt turn, in whole seconds.
	CoordinatorHostTurnTimeoutMs int `json:"coordinator_host_turn_timeout_ms"`
	// Maximum wait() sleep duration (milliseconds) for the coordinator loop, in whole seconds.
	CoordinatorMaxSleepMs int `json:"coordinator_max_sleep_ms"`
	// Wall-clock cap (milliseconds) blocking until parent-session workers finish, in whole seconds.
	AwaitParentWorkersTimeoutMs int `json:"await_parent_workers_timeout_ms"`
	// Worker tool-round ceiling applied when neither the task nor its planned leg sets max_tool_loops. The host never raises a ceiling on its own; a worker asks with request_budget and the coordinator grants with extend_worker_budget.
	WorkerToolBudgetDefault int `json:"worker_tool_budget_default"`
	// Smallest worker tool-round ceiling the coordinator may set (host floor is 2).
	WorkerToolBudgetMin int `json:"worker_tool_budget_min"`
	// Largest worker tool-round ceiling the coordinator may set or grant.
	WorkerToolBudgetMax int `json:"worker_tool_budget_max"`
	// Opt-in per-session nano-USD spend limit (enforced only when spend_ceiling_enabled is true and spend is priced).
	SessionSpendCeilingNanoUSD int64 `json:"session_spend_ceiling_nano_usd,omitempty"`
	// Share of the session spend limit at which the host emits a one-shot warning.
	SpendWarningRatio float64 `json:"spend_warning_ratio,omitempty"`
	// When true and session_spend_ceiling_nano_usd is greater than zero, land and pause the loop once estimated session spend reaches the ceiling.
	SpendCeilingEnabled bool `json:"spend_ceiling_enabled,omitempty"`
	// When true, a coordinator that crosses the ceiling while already running receives one final tool-capable wind-down round. A response without tool calls is final; after tool calls, the host requests one prose-only closeout. New prompts and worker sessions remain blocked at the ceiling.
	SpendSoftStop bool     `json:"spend_soft_stop"`
	MergedFrom    []string `json:"merged_from"`
}

// SettingsPricing
type SettingsPricing struct {
	CostTrackingEnabled bool `json:"cost_tracking_enabled,omitempty"`
	// Present when tracking is on; stamped on off→on.
	CostTrackingSinceAt *time.Time `json:"cost_tracking_since_at,omitempty"`
	// Feed rows with at most one selected (`enabled: true`); tracking on requires exactly one.
	Sources []SettingsPricingSource `json:"sources,omitempty"`
}

// SettingsPricingResponse
type SettingsPricingResponse struct {
	CostTrackingEnabled bool `json:"cost_tracking_enabled"`
	// Present when tracking is on; stamped on off→on.
	CostTrackingSinceAt *time.Time `json:"cost_tracking_since_at,omitempty"`
	// Catalog feed rows with at most one selected (`enabled: true`); tracking on requires exactly one.
	Sources          []SettingsPricingSource `json:"sources"`
	AvailableSources []PricingSourceMeta     `json:"available_sources"`
}

// SettingsPricingSource
type SettingsPricingSource struct {
	ID string `json:"id"`
	// True for the single selected pricing source.
	Enabled bool `json:"enabled"`
}

// SkillActivation
type SkillActivation struct {
	// Skill id from SKILL.md frontmatter.
	Name string `json:"name"`
	// Full authored description, not the bounded index projection.
	Description string `json:"description,omitempty"`
	// SKILL.md body the activation delivered, without the host's directory and file trailer. Carried so the card renders the skill rather than re-deriving it from the envelope text.
	Instructions string `json:"instructions"`
	// Provenance label for where the skill was read from — an absolute host path for a project or installed skill, a config-relative path for one that ships in the binary. It is a label, not an openable path.
	Dir string `json:"dir,omitempty"`
	// Bundled files listed in the activation, relative to the skill directory.
	Resources []string `json:"resources,omitempty"`
	// Regular resources beyond the listing bound.
	ResourcesOmitted int `json:"resources_omitted,omitempty"`
	// Skill came from the project overlay rather than a host or bundled pack.
	Project       bool   `json:"project,omitempty"`
	PackID        string `json:"pack_id,omitempty"`
	License       string `json:"license,omitempty"`
	Compatibility string `json:"compatibility,omitempty"`
	// Author's allowed-tools frontmatter. Shown so a skill that declares tool limits reads honestly; this host never honors it as a permission.
	AllowedTools string `json:"allowed_tools,omitempty"`
}

// SourceAttributedText Character range in UTF-16 code units after CRLF is normalized to LF. The comparison endpoints retain their original text and saved hashes.
type SourceAttributedText struct {
	Index        uint32              `json:"index"`
	Length       uint32              `json:"length"`
	Contributors []SourceContributor `json:"contributors"`
	// Authored in the selected chat or turn, subject to the marking choice.
	Selected bool `json:"selected"`
	// False when the person's edit is left unmarked.
	Visible bool `json:"visible"`
}

// SourceAttributionInterval Inclusive 1-based line span containing text attributed to an agent change. Spans may overlap when several agents contributed to the same line.
type SourceAttributionInterval struct {
	StartLine  int       `json:"start_line"`
	EndLine    int       `json:"end_line"`
	SessionID  *string   `json:"session_id,omitempty"`
	Turn       int       `json:"turn"`
	ToolCallID *string   `json:"tool_call_id,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

// SourceAttributionResponse Line provenance for one file. head_sha256 is the tip after hash the
// intervals describe; clients render marks only when the buffer base
// sha matches and the buffer is clean.
type SourceAttributionResponse struct {
	HeadSha256 string                      `json:"head_sha256"`
	Intervals  []SourceAttributionInterval `json:"intervals"`
}

// SourceChange One host-authoritative or external filesystem transition. Rename carries from_path; agent/user changes may carry attribution and after_sha256.
type SourceChange struct {
	RootID string `json:"root_id"`
	// Root-relative path after the change (destination for rename).
	Path string `json:"path"`
	// Root-relative path before a rename; omit for other ops.
	FromPath string             `json:"from_path,omitempty"`
	Op       SourceChangeOp     `json:"op"`
	Origin   SourceChangeOrigin `json:"origin"`
	// Entry kind after an upsert; omitted when unknown or deleted.
	IsDir *bool `json:"is_dir,omitempty"`
	// Producing session attribution; never event-delivery scope.
	SessionID  string `json:"session_id,omitempty"`
	WorkerID   string `json:"worker_id,omitempty"`
	Turn       int    `json:"turn,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Hex SHA-256 of content after a host write; omit for external and delete.
	AfterSHA256 string    `json:"after_sha256,omitempty"`
	ChangedAt   time.Time `json:"changed_at"`
}

// SourceChangesEvent A bounded set of filesystem transitions for one physical source workspace. Resync means the path set overflowed or exact transitions are unavailable; clients rehydrate their loaded projection.
type SourceChangesEvent struct {
	ProjectID     string              `json:"project_id"`
	WorkspaceID   string              `json:"workspace_id"`
	WorkspaceKind SourceWorkspaceKind `json:"workspace_kind"`
	Changes       []SourceChange      `json:"changes"`
	Resync        bool                `json:"resync"`
	// A git ref surface moved in this window — possibly with no file changes at all (a commit). Consumers refresh git-derived comparisons even when changes is empty.
	GitChanged bool `json:"git_changed,omitempty"`
}

// SourceCommandWindow One host-run command's observation window: the span of a command or
// verify tool call during which reconcile passes attribute the file
// changes they observe to that command, and admit files the window's
// start inventory did not hold. Effects reference it by command_id. It
// states when a change was observed, never that the process wrote the
// bytes: an edit made in another editor while the command ran lands
// here too, and the label says so.
type SourceCommandWindow struct {
	ID string `json:"id"`
	// Chat whose tool call ran the command; its effects answer that chat's turn and session lenses.
	SessionID  string  `json:"session_id"`
	Turn       int     `json:"turn"`
	ToolCallID *string `json:"tool_call_id,omitempty"`
	// The tool that ran the command — command or verify.
	ToolName *string `json:"tool_name"`
	// The canonical command line, with secret references rather than values.
	CommandLine string                   `json:"command_line"`
	State       SourceCommandWindowState `json:"state"`
	// How the pass that ended the window selected files. `scope` means every directory the root's source scope admitted was observed; `scope_bounded` means a walk budget stopped the observation short of the scope. Omitted while running.
	AdmissionMode *string `json:"admission_mode,omitempty"`
	// Position on the project's source-history clock, taken when the command started.
	Ordinal   int64     `json:"ordinal"`
	StartedAt time.Time `json:"started_at"`
	// When the window ended; omitted while running.
	EndedAt *time.Time `json:"ended_at,omitempty"`
}

// SourceCommitComparison Current Git comparison addressed by root_id and path, independent of
// recorded effects. No event, timestamp or author is inferred from Git
// status. file_id on the containing row is empty without retained history.
type SourceCommitComparison struct {
	// Full HEAD object id; empty before the first commit.
	Head string `json:"head"`
	// Two-column porcelain-v2 index/worktree status, or ?? for untracked.
	Status string         `json:"status"`
	Op     SourceChangeOp `json:"op"`
	// Availability of current content; metadata-only paths remain listed.
	Availability string `json:"availability"`
	// Only the latest recorded history fits this row; all retained versions remain available in the file version picker.
	HistoryTruncated bool `json:"history_truncated"`
}

// SourceCommitRestoreRequest Restores the file's bytes as one commit holds them. The bytes come
// from the repository object store and are verified against blob_oid
// before anything is written; the mutation lands through the ledger's
// write door as an ordinary attributed restore.
type SourceCommitRestoreRequest struct {
	// Idempotency key for this restore attempt.
	OperationID string `json:"operation_id"`
	// Logical file identity shown by the version picker.
	FileID string `json:"file_id"`
	// Root whose repository holds the commit and receives the restore.
	RootID string `json:"root_id"`
	// The file's current root-relative path — the restore target.
	Path string `json:"path"`
	// The file's path at the commit, toplevel-relative, from the listed lineage.
	SourcePath string `json:"source_path"`
	// Object id of the bytes being restored; the fetched content must hash to it.
	BlobOid string `json:"blob_oid"`
	// Reviewed current tip for compare-and-swap protection.
	Base SourceTip `json:"base"`
}

// SourceCommitRestoreResponse
type SourceCommitRestoreResponse struct {
	Commit string `json:"commit"`
	// The retained pre-restore state, the exact Undo target.
	PreviousVersionID string         `json:"previous_version_id,omitempty"`
	FileID            string         `json:"file_id"`
	RootID            string         `json:"root_id"`
	Path              string         `json:"path"`
	State             SourceTipState `json:"state"`
	Sha256            string         `json:"sha256,omitempty"`
	Changed           bool           `json:"changed"`
}

// SourceCommitRoot
type SourceCommitRoot struct {
	RootID string `json:"root_id"`
	// Full HEAD object id; empty before the first commit.
	Head      string `json:"head"`
	Available bool   `json:"available"`
	Error     string `json:"error,omitempty"`
}

// SourceComparison Exact endpoints painted by a Walk effect or version selection. Both
// endpoints are present when in_range is true and omitted when it is
// false — a lens with no effect for the file has no endpoints to
// describe, and inventing empty ones would report states outside the
// values each side declares.
type SourceComparison struct {
	// True when content exceeds the inline byte cap and full diff requires paged comparison views.
	Truncated bool `json:"truncated"`
	// The unread boundary used for this presentation comparison. Open views retain it until the reader leaves.
	PresentationAfterOrdinal *int64 `json:"presentation_after_ordinal,omitempty"`
	// Present when the endpoints share text identities; both endpoints retain their actual saved bytes.
	Attribution *SourceComparisonAttribution `json:"attribution,omitempty"`
	// False only when a requested lens contains no effect for the file.
	InRange  bool           `json:"in_range"`
	EffectID string         `json:"effect_id,omitempty"`
	FileID   string         `json:"file_id,omitempty"`
	Op       SourceChangeOp `json:"op,omitempty"`
	// Present only when in_range is true.
	Before *SourceComparisonSide `json:"before,omitempty"`
	// Present only when in_range is true.
	After *SourceComparisonSide `json:"after,omitempty"`
	// True when root_id or path differs between the endpoints.
	LocationChanged bool `json:"location_changed"`
	// True when attribution marks omit the person's own edits. Saved endpoints are unchanged.
	UserEditsUnmarked bool `json:"user_edits_unmarked,omitempty"`
}

// SourceComparisonAnchor
type SourceComparisonAnchor struct {
	Row int `json:"row"`
}

// SourceComparisonAttribution Changed ranges in UTF-16 coordinates, using collaborative character identities or recorded file deltas. Other chats remain visible as context. Missing authors remain unknown.
type SourceComparisonAttribution struct {
	Before []SourceAttributedText `json:"before"`
	After  []SourceAttributedText `json:"after"`
}

// SourceComparisonDetails
type SourceComparisonDetails struct {
	Summary                  *SourceComparisonSummary `json:"summary,omitempty"`
	InRange                  bool                     `json:"in_range"`
	LocationChanged          bool                     `json:"location_changed"`
	EffectID                 string                   `json:"effect_id,omitempty"`
	FileID                   string                   `json:"file_id,omitempty"`
	Op                       SourceChangeOp           `json:"op,omitempty"`
	PresentationAfterOrdinal *int64                   `json:"presentation_after_ordinal,omitempty"`
	UserEditsUnmarked        bool                     `json:"user_edits_unmarked,omitempty"`
	Before                   *SourceReaderEndpoint    `json:"before,omitempty"`
	After                    *SourceReaderEndpoint    `json:"after,omitempty"`
}

// SourceComparisonDigest One comparison's measure. A failed source carries failure and nothing else.
type SourceComparisonDigest struct {
	// False when the source holds no change for the file, which is a valid answer.
	InRange bool                     `json:"in_range"`
	Summary *SourceComparisonSummary `json:"summary,omitempty"`
	// Rows a changes presentation of this comparison holds: every changed and context row, and one per folded run of unchanged rows.
	ChangesRows int `json:"changes_rows,omitempty"`
	// Folded runs among changes_rows.
	ChangesFolds int                `json:"changes_folds,omitempty"`
	Failure      *SourceViewFailure `json:"failure,omitempty"`
}

// SourceComparisonDigestRequest Comparisons to measure without opening a view. Retained, current and text sources need a view or carry their own text, so they are rejected.
type SourceComparisonDigestRequest struct {
	SessionID string                     `json:"session_id,omitempty"`
	Sources   []SourceComparisonSelector `json:"sources"`
}

// SourceComparisonDigests
type SourceComparisonDigests struct {
	// One digest per requested source, in request order.
	Digests []SourceComparisonDigest `json:"digests"`
}

// SourceComparisonFold
type SourceComparisonFold struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// SourceComparisonFrame
type SourceComparisonFrame struct {
	Kind               string           `json:"kind"`
	ViewID             string           `json:"view_id"`
	IntentRevision     string           `json:"intent_revision"`
	ProjectionRevision string           `json:"projection_revision"`
	Extent             SourceViewExtent `json:"extent"`
	Span               SourceViewSpan   `json:"span"`
	// Resolved anchor plus offset before subtracting before, in this snapshot. Present on anchored reads, including when leading context is clamped at the beginning.
	Target *int64                 `json:"target,omitempty"`
	Anchor SourceComparisonAnchor `json:"anchor"`
	Rows   []SourceReaderRow      `json:"rows"`
}

// SourceComparisonIntent
type SourceComparisonIntent struct {
	Mode     string                 `json:"mode"`
	Expanded []SourceComparisonFold `json:"expanded,omitempty"`
}

// SourceComparisonLocation
type SourceComparisonLocation struct {
	Kind               string                 `json:"kind"`
	ViewID             string                 `json:"view_id"`
	ProjectionRevision string                 `json:"projection_revision"`
	Index              int64                  `json:"index"`
	Visible            bool                   `json:"visible"`
	Anchor             SourceComparisonAnchor `json:"anchor"`
}

// SourceComparisonSearchPage
type SourceComparisonSearchPage struct {
	Kind               string              `json:"kind"`
	ViewID             string              `json:"view_id"`
	ProjectionRevision string              `json:"projection_revision"`
	NextCursor         string              `json:"next_cursor"`
	Complete           bool                `json:"complete"`
	Matches            []SourceReaderMatch `json:"matches"`
}

// SourceComparisonSide One exact immutable endpoint. Availability is explicit because version
// identity and metadata remain explicit for metadata-only captures and
// unexpectedly unavailable content.
type SourceComparisonSide struct {
	// Empty for working-file, repository-object, and carried-start endpoints.
	VersionID    string `json:"version_id,omitempty"`
	RootID       string `json:"root_id,omitempty"`
	Path         string `json:"path,omitempty"`
	State        string `json:"state"`
	Sha256       string `json:"sha256,omitempty"`
	SizeBytes    int64  `json:"size_bytes"`
	Availability string `json:"availability"`
	Reason       string `json:"reason,omitempty"`
	// Exact decoded text when availability is available; otherwise empty.
	Content string `json:"content"`
	// Secret spans computed over content when availability is available. Offsets refer to the exact content string in this side. Omitted when screening is unavailable or this side has no readable text.
	SecretScreen *SecretScreen `json:"secret_screen,omitempty"`
}

// SourceComparisonSummary
type SourceComparisonSummary struct {
	Before          SourceReaderSide         `json:"before"`
	After           SourceReaderSide         `json:"after"`
	Added           int                      `json:"added"`
	Removed         int                      `json:"removed"`
	Rows            int                      `json:"rows"`
	ChangeAreas     []SourceReaderChangeArea `json:"change_areas"`
	ChangeAreaCount int                      `json:"change_area_count"`
}

// SourceComparisonView
type SourceComparisonView struct {
	Kind               string                   `json:"kind"`
	ID                 string                   `json:"id"`
	IntentRevision     string                   `json:"intent_revision"`
	ProjectionRevision string                   `json:"projection_revision"`
	State              string                   `json:"state"`
	Extent             SourceViewExtent         `json:"extent"`
	ExpiresAt          time.Time                `json:"expires_at"`
	Failure            *SourceViewFailure       `json:"failure,omitempty"`
	Intent             SourceComparisonIntent   `json:"intent"`
	Comparison         *SourceComparisonDetails `json:"comparison,omitempty"`
}

// SourceComparisonViewCreate
type SourceComparisonViewCreate struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
	// Stable window identity shared with editor requests; grants no authority.
	ClientID  string                   `json:"client_id"`
	SessionID string                   `json:"session_id,omitempty"`
	Source    SourceComparisonSelector `json:"source"`
	Intent    SourceComparisonIntent   `json:"intent"`
}

// SourceComparisonViewUpdate
type SourceComparisonViewUpdate struct {
	Kind                   string                 `json:"kind"`
	OperationID            string                 `json:"operation_id"`
	ExpectedIntentRevision string                 `json:"expected_intent_revision"`
	Intent                 SourceComparisonIntent `json:"intent"`
}

// SourceContext Host-recorded source locations supplied with this message; independent of evidence and filesystem inventory.
type SourceContext struct {
	Locations []NavigationTarget `json:"locations"`
	Truncated bool               `json:"truncated"`
}

// SourceContributor One author of a change. Identity fields the contribution does not have are omitted.
type SourceContributor struct {
	Origin SourceChangeOrigin `json:"origin"`
	// Chat that made the change; omitted for external contributions.
	SessionID string `json:"session_id,omitempty"`
	Turn      int    `json:"turn"`
	// Person who wrote user-origin text; omitted for agent and external contributions.
	PersonID   string `json:"person_id,omitempty"`
	ActorLabel string `json:"actor_label"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	WorkerID   string `json:"worker_id,omitempty"`
}

// SourceDefinitionCandidate One AST-confirmed declaration. Mentions never appear here.
type SourceDefinitionCandidate struct {
	RootID string `json:"root_id"`
	// Root-relative path of the declaring file
	Path string `json:"path"`
	// 1-based declaration line
	Line int              `json:"line"`
	Kind SourceSymbolKind `json:"kind"`
	// Trimmed declaration line text
	Snippet string `json:"snippet"`
}

// SourceDefinitionRequest Resolve declarations for one identifier under the active root. Den
// sends the word under the caret; the host narrows whole-word search
// hits to AST-confirmed declarations. Empty symbol is a client no-op.
type SourceDefinitionRequest struct {
	// Pin resolution to one attached root (active root)
	RootID string `json:"root_id,omitempty"`
	// Root-relative path of the buffer that issued the request
	Path string `json:"path"`
	// Identifier under the caret (client word grab)
	Symbol string `json:"symbol"`
	// 1-based caret line (ranking origin; optional)
	Line int `json:"line,omitempty"`
}

// SourceDefinitionResponse Ranked declaration candidates for go-to-definition. Zero candidates
// is a successful answer. truncated is true when caps, incomplete discovery,
// read failures, or unavailable roots prevent an exhaustive lookup.
type SourceDefinitionResponse struct {
	Candidates []SourceDefinitionCandidate `json:"candidates"`
	// True whenever the lookup could not establish exhaustive coverage
	Truncated bool `json:"truncated"`
}

// SourceDeletedFile The last recorded deletion at this exact path in this workspace, with its preceding immutable state. Never an editable working file.
type SourceDeletedFile struct {
	DeletedAt time.Time                `json:"deleted_at"`
	Previous  SourceReaderEndpoint     `json:"previous"`
	Source    *VersionComparisonSource `json:"source,omitempty"`
}

// SourceDirEntry One immediate child of a browsed directory.
type SourceDirEntry struct {
	Name string `json:"name"`
	// True for directories (symlinks classify by their target)
	IsDir bool `json:"is_dir"`
}

// SourceDirListing One directory level under an attached root for the Files tree.
// Entries are sorted folders first, then case-insensitive by name.
type SourceDirListing struct {
	// Physical root-set identity used to produce this listing.
	WorkspaceID string `json:"workspace_id"`
	RootID      string `json:"root_id"`
	// Normalized root-relative directory; "." is the root folder
	Dir string `json:"dir"`
	// Whether host filesystem observation covers this directory. A truncated watch covers some directories and not others, so this is answered per listing rather than per root. False tells visible clients to revalidate this directory on a bounded timer.
	WatchComplete bool             `json:"watch_complete"`
	Entries       []SourceDirEntry `json:"entries"`
}

// SourceEditorConfig EditorConfig pairs in effect for one root-addressed path. Every property
// is omitted when no `.editorconfig` declares it.
type SourceEditorConfig struct {
	// Normalized repo-relative path (slash-separated)
	Path string `json:"path"`
	// Attached root that holds the path
	RootID      string `json:"root_id"`
	IndentStyle string `json:"indent_style,omitempty"`
	// Columns per indentation level; omitted when indent_size_tab is true.
	IndentSize int `json:"indent_size,omitempty"`
	// True when `indent_size = tab`, so a level follows tab_width.
	IndentSizeTab          bool   `json:"indent_size_tab,omitempty"`
	TabWidth               int    `json:"tab_width,omitempty"`
	EndOfLine              string `json:"end_of_line,omitempty"`
	TrimTrailingWhitespace *bool  `json:"trim_trailing_whitespace,omitempty"`
	InsertFinalNewline     *bool  `json:"insert_final_newline,omitempty"`
	Charset                string `json:"charset,omitempty"`
}

// SourceFileCommit One commit of the file's git lineage, followed across renames. A
// commit whose blob equals a retained version's bytes names it in
// matches_version_id — one state, both identities. A commit whose bytes
// the tracked working tree never held either names the observed ref
// movement that brought it (arrival_git_change_id) or stands in the
// git-history era. Timestamps are git's own claims and are display
// metadata, never an ordering authority.
type SourceFileCommit struct {
	Commit      string    `json:"commit"`
	Subject     string    `json:"subject"`
	AuthorName  *string   `json:"author_name,omitempty"`
	AuthoredAt  time.Time `json:"authored_at"`
	CommittedAt time.Time `json:"committed_at"`
	// The file's path at this commit, relative to the repository toplevel — the address the commit resolves, whatever the file is called today.
	SourcePath string `json:"source_path"`
	// Full git object id of the file's bytes at this commit; omitted when the commit removed the file.
	BlobOid *string `json:"blob_oid,omitempty"`
	// Retained version holding byte-identical content, by derived blob object id; the picker renders one row carrying both identities.
	MatchesVersionID *string `json:"matches_version_id,omitempty"`
	// Id of the entry in this response's arrivals list — the observed ref movement whose window first made this commit reachable. Omitted when unknown or pre-tracking.
	ArrivalGitChangeID *string `json:"arrival_git_change_id,omitempty"`
}

// SourceFileVersion One retained state of a logical file. States a recorded effect produced
// carry operation_id, effect_id, op, origin, and cause; states the ledger
// kept without a causing action — a tracked baseline, a preserved
// pre-image — omit all five. Absence is the signal that no action is
// known, never an empty action.
//
// An agent edit held in the person's open document is the third kind: it
// names its operation, origin, and cause, but no effect and no op,
// because nothing reached the working file. landing states that, and is
// the only place a state's bytes were never the file's own.
type SourceFileVersion struct {
	// Authors of the publication; omitted for retained states without a producing effect.
	Contributors []SourceContributor `json:"contributors,omitempty"`
	ID           string              `json:"id"`
	FileID       string              `json:"file_id"`
	// Which kind of tree retained this state. A worker state names its overlay through worker_id.
	WorkspaceKind        SourceWorkspaceKind `json:"workspace_kind"`
	ParentVersionID      *string             `json:"parent_version_id,omitempty"`
	DerivedFromVersionID *string             `json:"derived_from_version_id,omitempty"`
	OperationID          *string             `json:"operation_id,omitempty"`
	EffectID             *string             `json:"effect_id,omitempty"`
	RootID               string              `json:"root_id"`
	Path                 string              `json:"path"`
	State                string              `json:"state"`
	ContentSha256        string              `json:"content_sha256,omitempty"`
	SizeBytes            int64               `json:"size_bytes"`
	CaptureState         string              `json:"capture_state"`
	CaptureReason        string              `json:"capture_reason,omitempty"`
	CaptureQuality       string              `json:"capture_quality"`
	// Where these bytes landed. working_file is every state the file itself held; editor_document is an agent edit that landed in the person's open document and never reached disk, retained so they can restore it after a reload or a discard.
	Landing string `json:"landing"`
	// Omitted when no recorded effect produced this state.
	Op *SourceChangeOp `json:"op,omitempty"`
	// Position on the project's source-history clock, and the exclusive cursor for the next page.
	Ordinal int64 `json:"ordinal"`
	// Omitted when no recorded operation produced this state.
	Origin     *SourceChangeOrigin `json:"origin,omitempty"`
	Cause      *string             `json:"cause,omitempty"`
	ActorLabel *string             `json:"actor_label,omitempty"`
	SessionID  *string             `json:"session_id,omitempty"`
	WorkerID   *string             `json:"worker_id,omitempty"`
	Turn       int                 `json:"turn"`
	ToolCallID *string             `json:"tool_call_id,omitempty"`
	BatchID    *string             `json:"batch_id,omitempty"`
	// The observed ref movement the producing operation traced this state to; omitted for states with no git cause.
	GitChange *SourceGitChange `json:"git_change,omitempty"`
	// The command observation window open when this state was observed; omitted for states no window covered.
	Command   *SourceCommandWindow `json:"command,omitempty"`
	CreatedAt time.Time            `json:"created_at"`
}

// SourceFileVersionsResponse
type SourceFileVersionsResponse struct {
	FileID string `json:"file_id"`
	// Tracked primary-workspace head for this logical file; absent also covers a worker-only file.
	Current  SourceTip           `json:"current"`
	Versions []SourceFileVersion `json:"versions"`
	// One page of the file's git lineage, newest first. Empty under every git_history_state except available, where an empty list is the answer: the file has no commits under its tracked path.
	Commits []SourceFileCommit `json:"commits"`
	// The observed ref movements this page's commits name as their arrival.
	Arrivals []SourceGitChange `json:"arrivals"`
	// Whether the lane was asked and what came back. Read it before reading commits: only available makes an empty list a statement about the file, and timed_out and failed are facts about the query that must never be presented as an absence of history.
	GitHistoryState SourceGitHistoryState `json:"git_history_state"`
	// When the ledger first retained a state of this file — the boundary between the observed timeline and history reconstructed from git.
	TrackedAt *time.Time `json:"tracked_at,omitempty"`
	// Opaque cursor continuing every requested lane that holds more. Omitted when all are exhausted.
	NextCursor string `json:"next_cursor,omitempty"`
}

// SourceGitChange One observed git ref movement, stated from git's own records (HEAD and
// the reflog) at observation time. Effects recorded by the pass that
// observed the movement reference it as their cause; a movement that
// changed no tracked file bytes — a bare commit — stands alone. The
// invoking chat, turn, and tool call are absent for an unaffiliated
// movement.
type SourceGitChange struct {
	// Invoking chat.
	SessionID  string              `json:"session_id,omitempty"`
	Turn       int                 `json:"turn,omitempty"`
	ToolCallID string              `json:"tool_call_id,omitempty"`
	ToolName   string              `json:"tool_name,omitempty"`
	ID         string              `json:"id"`
	RootID     string              `json:"root_id"`
	Kind       SourceGitChangeKind `json:"kind"`
	FromCommit *string             `json:"from_commit,omitempty"`
	ToCommit   *string             `json:"to_commit,omitempty"`
	// Short branch name before the movement; omitted when detached or unknown.
	FromRef *string `json:"from_ref,omitempty"`
	// Short branch name after the movement; omitted when detached or unknown.
	ToRef *string `json:"to_ref,omitempty"`
	// Git's own subject for the movement — a commit message subject, a checkout description, or the raw reflog action for kind other.
	Detail *string `json:"detail,omitempty"`
	// Position on the project's source-history clock, shared with effects and versions.
	Ordinal    int64     `json:"ordinal"`
	ObservedAt time.Time `json:"observed_at"`
}

// SourceGitCommitDetails
type SourceGitCommitDetails struct {
	Hash        string    `json:"hash"`
	Parents     []string  `json:"parents"`
	Message     string    `json:"message"`
	AuthorName  string    `json:"author_name"`
	AuthoredAt  time.Time `json:"authored_at"`
	CommittedAt time.Time `json:"committed_at"`
}

// SourceGitReview
type SourceGitReview struct {
	Change SourceGitChange        `json:"change"`
	Commit SourceGitCommitDetails `json:"commit"`
	// True compares the destination commit with its selected parent; false compares the recorded movement endpoints.
	CommitComparison bool `json:"commit_comparison"`
	// Empty only for an initial commit compared with the empty tree.
	BeforeCommit string                `json:"before_commit"`
	AfterCommit  string                `json:"after_commit"`
	Files        []SourceGitReviewFile `json:"files"`
	// Complete count within the attached root.
	FilesTotal int    `json:"files_total"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// SourceGitReviewFile
type SourceGitReviewFile struct {
	// Root-relative path in the comparison; deleted paths retain their old address.
	Path       string         `json:"path"`
	BeforePath string         `json:"before_path"`
	Op         SourceChangeOp `json:"op"`
	BeforeMode string         `json:"before_mode"`
	AfterMode  string         `json:"after_mode"`
	BeforeOid  string         `json:"before_oid"`
	AfterOid   string         `json:"after_oid"`
	Insertions int            `json:"insertions"`
	Deletions  int            `json:"deletions"`
	Binary     bool           `json:"binary"`
}

// SourceHistoryAction The host-owned lifecycle operation currently available in one history direction.
type SourceHistoryAction struct {
	ID string `json:"id"`
	// Sentence-case action label suitable for a button tooltip or menu row.
	Label string `json:"label"`
	// trash is a Files deletion that moved the entry to the system Trash; delete is an agent deletion that removed it from disk. Both keep a complete recovery copy.
	Kind   string `json:"kind"`
	RootID string `json:"root_id"`
	// Root-relative path the history action will create, remove, or move.
	Path     string `json:"path"`
	FromPath string `json:"from_path,omitempty"`
	ToPath   string `json:"to_path,omitempty"`
	IsDir    bool   `json:"is_dir"`
	// True when the action removes or retargets the current project path and dirty editor state must be resolved first.
	RemovesPath bool `json:"removes_path"`
}

// SourceHistoryMutationRequest
type SourceHistoryMutationRequest struct {
	// Stable mutation identity; exact retries return the original result.
	OperationID string `json:"operation_id"`
	// Exact undo or redo head displayed to the user; a moved head returns 409.
	ExpectedEntryID string `json:"expected_entry_id"`
}

// SourceHistoryMutationResponse Canonical filesystem transition produced by undo or redo.
type SourceHistoryMutationResponse struct {
	EntryID  string         `json:"entry_id"`
	RootID   string         `json:"root_id"`
	Path     string         `json:"path"`
	FromPath string         `json:"from_path,omitempty"`
	Op       SourceChangeOp `json:"op"`
	IsDir    bool           `json:"is_dir"`
}

// SourceHistoryState Current host-authoritative lifecycle history heads for one project.
type SourceHistoryState struct {
	Undo *SourceHistoryAction `json:"undo"`
	Redo *SourceHistoryAction `json:"redo"`
}

// SourceIndexResource
type SourceIndexResource struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// SourceIndexRootCoverage Readability and coverage are independent; completed discovery can still have omissions.
type SourceIndexRootCoverage struct {
	RootID             string           `json:"root_id"`
	State              SourceIndexState `json:"state"`
	DiscoveryComplete  bool             `json:"discovery_complete"`
	Refreshing         bool             `json:"refreshing"`
	BoundedDirectories int              `json:"bounded_directories"`
	FailedDirectories  int              `json:"failed_directories"`
	Error              string           `json:"error,omitempty"`
}

// SourceIndexRootSummary
type SourceIndexRootSummary struct {
	RootID    string                `json:"root_id"`
	FileCount int                   `json:"file_count"`
	Resources []SourceIndexResource `json:"resources"`
}

// SourceIndexSummary Counts and resources from readable generations, with explicit omissions and pending work for every selected root.
type SourceIndexSummary struct {
	State      SourceIndexState          `json:"state"`
	Revision   uint64                    `json:"revision"`
	Refreshing bool                      `json:"refreshing"`
	Coverage   []SourceIndexRootCoverage `json:"coverage"`
	// Milliseconds until the next poll while results are pending; increases with elapsed work time.
	RetryAfterMs int                      `json:"retry_after_ms,omitempty"`
	FileCount    int                      `json:"file_count"`
	Roots        []SourceIndexRootSummary `json:"roots"`
}

// SourceInventoryState Project-scoped background source inventory lifecycle. Session creation never waits for this state.
type SourceInventoryState struct {
	Status string `json:"status"`
	// True only when the indexed snapshot matches the current roots generation.
	Complete        bool  `json:"complete"`
	RootsGeneration int   `json:"roots_generation"`
	IndexedFiles    int64 `json:"indexed_files"`
	// Content-addressed identity of the complete published source manifest.
	SourceSnapshotID string `json:"source_snapshot_id,omitempty"`
	// Process generation vector proven stable when the snapshot was published.
	WorktreeEpoch string     `json:"worktree_epoch,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
}

// SourceOperationEvent A file operation's status after a persisted transition or progress save; the same shape the operation list returns.
type SourceOperationEvent struct {
	ProjectID string                `json:"project_id"`
	Operation SourceOperationStatus `json:"operation"`
}

// SourceOperationList
type SourceOperationList struct {
	Operations []SourceOperationStatus `json:"operations"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

// SourceOperationStatus Durable file operation identified by its mutation operation_id. Disconnects leave accepted work running; terminal results survive restart.
type SourceOperationStatus struct {
	RootID   string `json:"root_id"`
	Path     string `json:"path"`
	FromPath string `json:"from_path"`
	// Original history head for an undo or redo request; omitted for other operations.
	ExpectedEntryID string `json:"expected_entry_id,omitempty"`
	OperationID     string `json:"operation_id"`
	Complete        bool   `json:"complete"`
	State           string `json:"state"`
	// Current work phase. Progress counts describe that phase and may reset on phase changes.
	Phase     string `json:"phase"`
	Operation string `json:"operation"`
	// Cancellation is available before filesystem publication; published changes finish reconciliation and support undo through file history.
	Cancelable       bool           `json:"cancelable"`
	EntriesProcessed int64          `json:"entries_processed"`
	BytesProcessed   int64          `json:"bytes_processed"`
	Result           any            `json:"result,omitempty"`
	Error            *ErrorResponse `json:"error,omitempty"`
	// When the request was first submitted; a retry keeps it.
	CreatedAt time.Time `json:"created_at"`
	// When the request reached its terminal state; present exactly when complete.
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// SourcePin Named source comparison boundary.
type SourcePin struct {
	ID        string             `json:"id"`
	ProjectID string             `json:"project_id"`
	Label     string             `json:"label,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	GitHeads  []SourcePinGitHead `json:"git_heads,omitempty"`
}

// SourcePinCreate
type SourcePinCreate struct {
	Label string `json:"label,omitempty"`
}

// SourcePinGitHead The git position one root held when the pin was taken, as last observed before the boundary.
type SourcePinGitHead struct {
	RootID     string `json:"root_id"`
	RepoState  string `json:"repo_state"`
	HeadCommit string `json:"head_commit,omitempty"`
	HeadRef    string `json:"head_ref,omitempty"`
}

// SourcePinList
type SourcePinList struct {
	Pins       []SourcePin `json:"pins"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// SourcePinUpdate
type SourcePinUpdate struct {
	Label *string `json:"label,omitempty"`
}

// SourcePresentation
type SourcePresentation struct {
	ID   string     `json:"id"`
	View SourceView `json:"view"`
}

// SourcePresentationCompletion The newest effect a Files or Review pane painted for the file. The look
// covers every visible primary-tree effect of the file up to it, whoever
// made them.
type SourcePresentationCompletion struct {
	FileID   string `json:"file_id"`
	EffectID string `json:"effect_id"`
	Ordinal  int64  `json:"ordinal"`
}

// SourcePresentationCreate
type SourcePresentationCreate struct {
	OperationID    string `json:"operation_id"`
	IntentRevision string `json:"intent_revision"`
}

// SourceReaderChangeArea
type SourceReaderChangeArea struct {
	// One-based insertion point or first changed line in the after document.
	AfterLine int `json:"after_line"`
	Added     int `json:"added"`
	Removed   int `json:"removed"`
}

// SourceReaderEndpoint One exact immutable endpoint. Availability is explicit because version
// identity and metadata remain explicit for metadata-only captures and
// unexpectedly unavailable content.
type SourceReaderEndpoint struct {
	// Absent for working-file, repository-object, and carried-start endpoints.
	VersionID    string `json:"version_id,omitempty"`
	RootID       string `json:"root_id,omitempty"`
	Path         string `json:"path,omitempty"`
	State        string `json:"state"`
	Sha256       string `json:"sha256,omitempty"`
	SizeBytes    int64  `json:"size_bytes"`
	Availability string `json:"availability"`
	Reason       string `json:"reason,omitempty"`
	// Screening summary for this endpoint. Exact spans arrive with requested rows. Omitted when screening is unavailable or this side has no readable text.
	SecretScreen *SecretScreen `json:"secret_screen,omitempty"`
}

// SourceReaderMatch
type SourceReaderMatch struct {
	Row  int `json:"row"`
	From int `json:"from"`
	To   int `json:"to"`
}

// SourceReaderRow
type SourceReaderRow struct {
	Index int              `json:"index"`
	End   int              `json:"end"`
	Kind  string           `json:"kind"`
	Text  string           `json:"text"`
	Peer  *SourceReaderRow `json:"peer,omitempty"`
	// UTF-16 offset within the original line for a bounded fragment.
	Column       int                 `json:"column,omitempty"`
	BeforeLine   int                 `json:"before_line"`
	AfterLine    int                 `json:"after_line"`
	Syntax       []SourceReaderToken `json:"syntax,omitempty"`
	Changed      []SourceReaderSpan  `json:"changed"`
	SecretScreen *SecretScreen       `json:"secret_screen,omitempty"`
	Contributors []SourceContributor `json:"contributors,omitempty"`
}

// SourceReaderSide
type SourceReaderSide struct {
	Path         string `json:"path"`
	SHA256       string `json:"sha256"`
	Lines        int    `json:"lines"`
	Availability string `json:"availability"`
}

// SourceReaderSpan
type SourceReaderSpan struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// SourceReaderToken
type SourceReaderToken struct {
	From int    `json:"from"`
	To   int    `json:"to"`
	Kind string `json:"kind"`
}

// SourceRevisionComparison A typed revision resolved in one root's repository to two immutable
// commits. A commit compares with its first parent; the checked-out
// branch compares its tip with its parent; any other local branch
// compares its merge base with HEAD with HEAD; A..B compares A with B,
// and A...B compares their merge base with B.
type SourceRevisionComparison struct {
	RootID string `json:"root_id"`
	// The revision as typed.
	Spec string `json:"spec"`
	Kind string `json:"kind"`
	// Short name for the comparison, such as "a1b2c3d Fix parser" or "topic...HEAD".
	Label string `json:"label"`
	// Full commit id; empty only for a root commit compared with the empty tree.
	BeforeCommit string `json:"before_commit"`
	// Full commit id.
	AfterCommit string `json:"after_commit"`
	// Commit subject when the comparison is one commit against its parent.
	Subject string `json:"subject,omitempty"`
}

// SourceRevisionReview
type SourceRevisionReview struct {
	RootID string `json:"root_id"`
	// Empty compares with the empty tree.
	BeforeCommit string                `json:"before_commit"`
	AfterCommit  string                `json:"after_commit"`
	Files        []SourceGitReviewFile `json:"files"`
	// Complete count within the attached root.
	FilesTotal int    `json:"files_total"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
	NextCursor string `json:"next_cursor,omitempty"`
}

// SourceRevisionsResponse
type SourceRevisionsResponse struct {
	// One comparison per root where the revision resolves, primary root first. Empty when it names nothing.
	Comparisons []SourceRevisionComparison `json:"comparisons"`
}

// SourceSearchHighlight A half-open range of Unicode code point offsets into a matched path or name.
type SourceSearchHighlight struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// SourceSearchLocation The one-based line and column, or inclusive line range, a location on the query named. Opening a match goes there.
type SourceSearchLocation struct {
	Line    int `json:"line"`
	Column  int `json:"column,omitempty"`
	EndLine int `json:"end_line,omitempty"`
}

// SourceSearchMatch
type SourceSearchMatch struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
	// Ascending, disjoint parts of path the query matched. Empty when the query matched only the root folder's path or was empty.
	Highlights []SourceSearchHighlight `json:"highlights"`
}

// SourceSearchOutside An absolute query that exists on this host outside every selected root.
type SourceSearchOutside struct {
	// The path in the host's own spelling.
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// SourceSearchResponse Available file matches, including while other selected roots are preparing or failed. Refresh while refreshing is true; inspect coverage before treating absence as exhaustive.
type SourceSearchResponse struct {
	State      SourceIndexState          `json:"state"`
	Revision   uint64                    `json:"revision"`
	Refreshing bool                      `json:"refreshing"`
	Coverage   []SourceIndexRootCoverage `json:"coverage"`
	// Milliseconds until the next poll while results are pending; increases with elapsed work time.
	RetryAfterMs int `json:"retry_after_ms,omitempty"`
	// Files in rank order. Clients render this order as-is.
	Matches    []SourceSearchMatch   `json:"matches"`
	Location   *SourceSearchLocation `json:"location,omitempty"`
	Outside    *SourceSearchOutside  `json:"outside,omitempty"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

// SourceSeenFile A file whose latest look is still current, with the effects that look covered.
type SourceSeenFile struct {
	FileID string    `json:"file_id"`
	RootID string    `json:"root_id"`
	Path   string    `json:"path"`
	Tip    SourceTip `json:"tip"`
	// When the look completed.
	SeenAt time.Time `json:"seen_at"`
	// Names the look. Withdrawing it quotes this value.
	ThroughOrdinal int64 `json:"through_ordinal"`
	// Effects the look covered, newest first.
	Effects []SourceWalkEffect `json:"effects"`
	// More effects than listed fell within the look.
	EffectsTruncated bool `json:"effects_truncated"`
}

// SourceSeenList
type SourceSeenList struct {
	// Newest look first.
	Files []SourceSeenFile `json:"files"`
	// Opaque cursor for the next page. Omitted on the final page.
	NextCursor string `json:"next_cursor,omitempty"`
}

// SourceStorage
type SourceStorage struct {
	Inventory SourceInventoryState `json:"inventory"`
	Storage   StorageUsageReport   `json:"storage"`
}

// SourceSymbol One declaration in document order for in-file navigation.
type SourceSymbol struct {
	Name string           `json:"name"`
	Kind SourceSymbolKind `json:"kind"`
	// 1-based line of the declaration
	Line int `json:"line"`
}

// SourceSymbolsResponse Per-file outline for symbol navigation. Unknown languages return an
// empty list with 200 — never an error. Capped at 2 000 symbols.
type SourceSymbolsResponse struct {
	// Editable source revision analyzed; empty when the source is not editable.
	SHA256  string         `json:"sha256"`
	Symbols []SourceSymbol `json:"symbols"`
	// True when the listing stopped at the 2 000 symbol cap
	Truncated bool `json:"truncated"`
}

// SourceTip What a file ends at. A deletion is an outcome, not a missing one: state
// absent carries no sha256, so a deleted file stays addressable.
type SourceTip struct {
	State SourceTipState `json:"state"`
	// Content hash. Present only when state is content.
	Sha256 string `json:"sha256,omitempty"`
}

// SourceTreeAddress
type SourceTreeAddress struct {
	RootID string `json:"root_id"`
	Path   string `json:"path"`
}

// SourceTreeAncestor
type SourceTreeAncestor struct {
	Row   SourceTreeRow `json:"row"`
	Index int64         `json:"index"`
	End   int64         `json:"end"`
}

// SourceTreeDisclose
type SourceTreeDisclose struct {
	Kind string `json:"kind"`
	// Ordered disclosure changes applied as one intent revision.
	Disclosures []SourceTreeDisclosure `json:"disclosures"`
}

// SourceTreeDisclosure
type SourceTreeDisclosure struct {
	Address   SourceTreeAddress `json:"address"`
	Open      bool              `json:"open"`
	Recursive bool              `json:"recursive"`
}

// SourceTreeFilter
type SourceTreeFilter struct {
	Kind  string `json:"kind"`
	Query string `json:"query"`
}

// SourceTreeFrame
type SourceTreeFrame struct {
	Kind               string           `json:"kind"`
	ViewID             string           `json:"view_id"`
	IntentRevision     string           `json:"intent_revision"`
	ProjectionRevision string           `json:"projection_revision"`
	Extent             SourceViewExtent `json:"extent"`
	Span               SourceViewSpan   `json:"span"`
	// Resolved anchor plus offset before subtracting before, in this snapshot. Present on anchored reads, including when leading context is clamped at the beginning.
	Target *int64 `json:"target,omitempty"`
	// Reusable prefix proof for this frame. May be absent for filtered or review projections.
	Prefix *SourceTreeFramePrefix `json:"prefix,omitempty"`
	// Largest supplied prefix proven unchanged in this snapshot. Cached rows below end retain their positions; ancestor bounds beyond end require fresh context.
	RetainedPrefix *SourceTreeFramePrefix `json:"retained_prefix,omitempty"`
	Anchor         SourceTreeAddress      `json:"anchor"`
	Rows           []SourceTreeRow        `json:"rows"`
	// Complete root-first ancestor chain for the frame's first row.
	Ancestors []SourceTreeAncestor `json:"ancestors"`
}

// SourceTreeFramePrefix
type SourceTreeFramePrefix struct {
	// Exclusive end of the validated row prefix.
	End int64 `json:"end"`
	// Opaque fingerprint of the intent, root order, and preceding tree rows.
	Fingerprint string `json:"fingerprint"`
}

// SourceTreeIntent
type SourceTreeIntent struct {
	Disclosures []SourceTreeDisclosure `json:"disclosures,omitempty"`
	Filter      string                 `json:"filter,omitempty"`
	Review      *SourceTreeReviewScope `json:"review,omitempty"`
}

// SourceTreeLocation
type SourceTreeLocation struct {
	Kind               string `json:"kind"`
	ViewID             string `json:"view_id"`
	ProjectionRevision string `json:"projection_revision"`
	Index              int64  `json:"index"`
	// Directory facts on this path are not ready; reveal the path to request them and retry after the projection changes.
	Pending bool              `json:"pending"`
	Visible bool              `json:"visible"`
	Address SourceTreeAddress `json:"address"`
}

// SourceTreeReveal
type SourceTreeReveal struct {
	Kind    string            `json:"kind"`
	Address SourceTreeAddress `json:"address"`
}

// SourceTreeReview
type SourceTreeReview struct {
	Kind  string                 `json:"kind"`
	Scope *SourceTreeReviewScope `json:"scope,omitempty"`
}

// SourceTreeReviewScope
type SourceTreeReviewScope struct {
	Baseline      string `json:"baseline"`
	MarkUserEdits *bool  `json:"mark_user_edits,omitempty"`
}

// SourceTreeRow
type SourceTreeRow struct {
	Address  SourceTreeAddress `json:"address"`
	Name     string            `json:"name"`
	Kind     string            `json:"kind"`
	Depth    int               `json:"depth"`
	Expanded bool              `json:"expanded"`
	Symlink  bool              `json:"symlink,omitempty"`
	Deleted  bool              `json:"deleted,omitempty"`
	FileID   string            `json:"file_id,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// SourceTreeSearchMatch
type SourceTreeSearchMatch struct {
	Address SourceTreeAddress `json:"address"`
	Index   int64             `json:"index"`
	From    int               `json:"from"`
	To      int               `json:"to"`
}

// SourceTreeSearchPage
type SourceTreeSearchPage struct {
	Kind               string                  `json:"kind"`
	ViewID             string                  `json:"view_id"`
	ProjectionRevision string                  `json:"projection_revision"`
	NextCursor         string                  `json:"next_cursor"`
	Complete           bool                    `json:"complete"`
	Matches            []SourceTreeSearchMatch `json:"matches"`
}

// SourceTreeToggle
type SourceTreeToggle struct {
	Kind    string            `json:"kind"`
	Address SourceTreeAddress `json:"address"`
	// When the folder is open, collapse its descendants and leave it open. A closed folder opens normally.
	CollapseDescendants bool `json:"collapse_descendants,omitempty"`
}

// SourceTreeView
type SourceTreeView struct {
	Kind               string             `json:"kind"`
	ID                 string             `json:"id"`
	IntentRevision     string             `json:"intent_revision"`
	ProjectionRevision string             `json:"projection_revision"`
	State              string             `json:"state"`
	Extent             SourceViewExtent   `json:"extent"`
	ExpiresAt          time.Time          `json:"expires_at"`
	Failure            *SourceViewFailure `json:"failure,omitempty"`
	WorkspaceID        string             `json:"workspace_id"`
	Intent             SourceTreeIntent   `json:"intent"`
	Roots              []ProjectRoot      `json:"roots"`
	// Directory reads still pending for this view.
	LoadingDirectories int `json:"loading_directories"`
}

// SourceTreeViewCreate
type SourceTreeViewCreate struct {
	Kind        string `json:"kind"`
	OperationID string `json:"operation_id"`
	// Stable window identity shared with editor requests; grants no authority.
	ClientID    string           `json:"client_id"`
	SessionID   string           `json:"session_id,omitempty"`
	WorkspaceID string           `json:"workspace_id"`
	Intent      SourceTreeIntent `json:"intent"`
}

// SourceTreeViewUpdate
type SourceTreeViewUpdate struct {
	Kind                   string `json:"kind"`
	OperationID            string `json:"operation_id"`
	ExpectedIntentRevision string `json:"expected_intent_revision"`
	// Resolve a toggle against the displayed presentation while preserving current disclosure intent. A presentation this view no longer presents answers source_view_revision_changed.
	BasePresentationID string            `json:"base_presentation_id,omitempty"`
	Command            SourceTreeCommand `json:"command"`
}

// SourceVersionRestoreRequest
type SourceVersionRestoreRequest struct {
	// Idempotency key for this restore attempt.
	OperationID string `json:"operation_id"`
	// Logical file identity shown by the version picker.
	FileID string `json:"file_id"`
	// Attached project root that contains the working-file target.
	RootID string `json:"root_id"`
	// Current root-relative working-file path. Historical renames do not move it.
	Path string    `json:"path"`
	Base SourceTip `json:"base"`
}

// SourceVersionRestoreResponse
type SourceVersionRestoreResponse struct {
	// Immutable retained version selected by the user.
	VersionID string `json:"version_id"`
	// Exact pre-restore state for one-click Undo, when the logical file already had a primary-workspace head.
	PreviousVersionID string         `json:"previous_version_id,omitempty"`
	FileID            string         `json:"file_id"`
	RootID            string         `json:"root_id"`
	Path              string         `json:"path"`
	State             SourceTipState `json:"state"`
	// Exact restored-byte hash when state is content.
	Sha256 string `json:"sha256,omitempty"`
	// False when the selected state already matched the working file.
	Changed bool `json:"changed"`
}

// SourceViewEvent
type SourceViewEvent struct {
	ViewID             string `json:"view_id"`
	Kind               string `json:"kind"`
	IntentRevision     string `json:"intent_revision"`
	ProjectionRevision string `json:"projection_revision"`
	Invalidated        bool   `json:"invalidated"`
	Terminal           bool   `json:"terminal"`
}

// SourceViewExtent
type SourceViewExtent struct {
	Rows     int64 `json:"rows"`
	Complete bool  `json:"complete"`
}

// SourceViewFailure
type SourceViewFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SourceViewSpan
type SourceViewSpan struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// SourceViewportInterest Replaceable viewport demand in one immutable presentation. Visible rows precede symmetric nearby preparation; direction breaks ties. This resource is ephemeral and does not change disclosure intent or extent.
type SourceViewportInterest struct {
	PresentationID string `json:"presentation_id"`
	Sequence       int64  `json:"sequence"`
	Start          int64  `json:"start"`
	// Exclusive; greater than start.
	End       int64 `json:"end"`
	Direction int   `json:"direction"`
}

// SourceWalkEffect
type SourceWalkEffect struct {
	ID              string  `json:"id"`
	ProjectID       string  `json:"project_id"`
	OperationID     string  `json:"operation_id"`
	FileID          string  `json:"file_id"`
	BeforeVersionID *string `json:"before_version_id,omitempty"`
	AfterVersionID  string  `json:"after_version_id"`
	// Which kind of tree this effect landed in. A worker effect names its overlay through worker_id; the project's own tree is the default.
	WorkspaceKind SourceWorkspaceKind `json:"workspace_kind"`
	RootID        string              `json:"root_id"`
	Path          string              `json:"path"`
	FromRootID    *string             `json:"from_root_id,omitempty"`
	FromPath      *string             `json:"from_path,omitempty"`
	EntryKind     string              `json:"entry_kind"`
	Op            SourceChangeOp      `json:"op"`
	Origin        SourceChangeOrigin  `json:"origin"`
	SessionID     *string             `json:"session_id,omitempty"`
	WorkerID      *string             `json:"worker_id,omitempty"`
	Turn          int                 `json:"turn"`
	ToolCallID    *string             `json:"tool_call_id,omitempty"`
	// Tool that caused the source effect, when invoked through the tool plane
	ToolName *string `json:"tool_name,omitempty"`
	// Shared by every effect one observation batch or one batch write recorded together; omitted for a single write.
	BatchID        *string `json:"batch_id,omitempty"`
	Ordinal        int64   `json:"ordinal"`
	Cause          string  `json:"cause"`
	CaptureQuality string  `json:"capture_quality"`
	ActorLabel     *string `json:"actor_label,omitempty"`
	// Id of the entry in the response's git_changes list this effect's operation traced its changes to; omitted for operations with no git cause.
	GitChangeID *string `json:"git_change_id,omitempty"`
	// Id of the entry in the response's commands list whose window was open when this effect was observed; omitted for operations no window covered.
	CommandID    *string             `json:"command_id,omitempty"`
	ObservedAt   time.Time           `json:"observed_at"`
	Contributors []SourceContributor `json:"contributors"`
}

// SourceWalkFile
type SourceWalkFile struct {
	Commit                *SourceCommitComparison `json:"commit,omitempty"`
	FileID                string                  `json:"file_id"`
	RootID                string                  `json:"root_id"`
	Path                  string                  `json:"path"`
	LastAt                *time.Time              `json:"last_at,omitempty"`
	ChangedSincePresented bool                    `json:"changed_since_presented"`
	// Agent effects not yet completed by a file presentation; human edits never clear this count.
	UnpresentedAgentEffects int64     `json:"unpresented_agent_effects"`
	PresentationEffectID    string    `json:"presentation_effect_id,omitempty"`
	PresentationOrdinal     int64     `json:"presentation_ordinal,omitempty"`
	Tip                     SourceTip `json:"tip"`
	// Whether the tip's bytes equal what HEAD holds at this path, by derived git blob object id. Unknown whenever the lens, the repository, or the captured bytes cannot answer.
	HeadMatch SourceHeadMatch    `json:"head_match"`
	Effects   []SourceWalkEffect `json:"effects"`
}

// SourceWalkResponse
type SourceWalkResponse struct {
	// The baseline this page answered, in the request's grammar. A
	// chat-relative `turn:{session_id}` comes back as
	// `turn:{session_id},{n}` naming the turn it read; a chat with no
	// user turn yet answers `turn:{session_id},0` and lists nothing.
	// Send it back for later pages and address per-file comparisons with
	// it, so every read describes the same range.
	Baseline string `json:"baseline"`
	// Opaque cursor for the next page. Omitted on the final page.
	NextCursor  string             `json:"next_cursor,omitempty"`
	CommitRoots []SourceCommitRoot `json:"commit_roots,omitempty"`
	Turns       []SourceWalkTurn   `json:"turns"`
	Files       []SourceWalkFile   `json:"files"`
	// Movements selected by the same baseline and ordinal page as file
	// effects, including commits with no working-file changes, newest first.
	// Also includes movements referenced by this page's effects through
	// git_change_id; those references do not advance the page cursor.
	GitChanges []SourceGitChange `json:"git_changes"`
	// Every command observation window an effect on this page references
	// by command_id, newest first. A window that observed nothing has no
	// row, so there is no bare command to list.
	Commands []SourceCommandWindow `json:"commands"`
	// False when the project root is not a git checkout
	CommitAvailable bool `json:"commit_available"`
}

// SourceWalkSummary
type SourceWalkSummary struct {
	Turns []SourceWalkTurnSummary `json:"turns"`
}

// SourceWalkTurn The opening user message and complete-transcript ordinal for a recorded turn.
type SourceWalkTurn struct {
	SessionID string `json:"session_id"`
	Turn      int    `json:"turn"`
	MessageID string `json:"message_id"`
	// A short excerpt of the opening user message.
	Prompt     string    `json:"prompt"`
	ObservedAt time.Time `json:"observed_at"`
}

// SourceWalkTurnSummary
type SourceWalkTurnSummary struct {
	MessageID string `json:"message_id"`
	// Permanent turn ordinal this message opened, as the turn baseline spells it.
	Turn  int `json:"turn"`
	Steps int `json:"steps"`
	Items int `json:"items"`
}

// SourceWatchCoverage
type SourceWatchCoverage struct {
	State SourceWatchState `json:"state"`
	// One root registration observes every descendant.
	Recursive bool `json:"recursive"`
	// Directories a per-directory watcher could not register.
	UnwatchedDirectories int `json:"unwatched_directories"`
}

// SourceWorkspace
type SourceWorkspace struct {
	// Identity derived from the project's resolved physical roots.
	WorkspaceID string `json:"workspace_id"`
	// True when the requested chat's worktree substitutes the project's roots, so reads and retained views of this workspace must carry that chat's id. False when the project's own checkout answers, and every chat resolving to this workspace_id shares it without one.
	SessionScoped bool `json:"session_scoped"`
	// Ordered physical roots used by every Files read and path action.
	Roots []SourceWorkspaceRoot `json:"roots"`
	// The ledger's pass over the tree. Outside edits to tracked files reach history by path before it completes; untracked files enter history when the pass, or an open command window, admits them.
	Inventory SourceInventoryState `json:"inventory"`
}

// SourceWorkspaceRoot
type SourceWorkspaceRoot struct {
	// Durable source branch for this root; omitted for the primary tree. Roots unaffected by a worktree binding retain their primary branch.
	BranchID string `json:"branch_id,omitempty"`
	// Stable attached-root identity.
	ID string `json:"id"`
	// Absolute physical path for this root in the resolved workspace.
	Path  string              `json:"path"`
	Watch SourceWatchCoverage `json:"watch"`
}

// StartWorkflowRunRequest
type StartWorkflowRunRequest struct {
	// Stable client mutation identity. An exact retry returns the original run; reusing this id with different input is rejected with 409 idempotency_conflict.
	OperationID     string `json:"operation_id"`
	WorkflowID      string `json:"workflow_id"`
	WorkflowVersion string `json:"workflow_version"`
	// Preset declared by the selected workflow version; its parameters apply before explicit parameters. An unknown preset is rejected.
	PresetID string `json:"preset_id,omitempty"`
	// Explicit request attached to this workflow start. When omitted or blank, the manifest default is used or its open question is presented.
	Request string `json:"request,omitempty"`
	// Values for parameters declared by the selected workflow. Unknown names and invalid values are rejected.
	Parameters map[string]string `json:"parameters,omitempty"`
	// Project-relative path under the overlay blueprints/ directory
	BlueprintPath  string `json:"blueprint_path,omitempty"`
	BlueprintTitle string `json:"blueprint_title,omitempty"`
	// Active run the human reviewed when replacing a non-ambient lineage.
	ReplaceRunID string `json:"replace_run_id,omitempty"`
	// Revision displayed with replace_run_id.
	ExpectedRevision int64 `json:"expected_revision,omitempty"`
}

// StorageUsageLane
type StorageUsageLane struct {
	Lane      string `json:"lane"`
	Scope     string `json:"scope"`
	UsedBytes int64  `json:"used_bytes"`
}

// StorageUsageReport Retained-content usage, measured per lane.
type StorageUsageReport struct {
	Lanes []StorageUsageLane `json:"lanes"`
}

// SubmitEditorDocumentUpdateRequest
type SubmitEditorDocumentUpdateRequest struct {
	// Focused chat when this change was authored; omitted when unaffiliated. The host records the chat's turn at acceptance and drops a chat it cannot place instead of refusing the change.
	SessionID   string `json:"session_id,omitempty"`
	ClientID    string `json:"client_id"`
	ReplicaID   uint32 `json:"replica_id"`
	Epoch       int64  `json:"epoch"`
	OperationID string `json:"operation_id"`
	Update      []byte `json:"update"`
	BaseSHA256  string `json:"base_sha256,omitempty"`
	StateVector []byte `json:"state_vector,omitempty"`
}

// SummarizeCoverage Indexed coverage and represented material; warming views have unknown totals
type SummarizeCoverage struct {
	// warming | ready | failed; omitted for file, pattern, or inline summaries
	CatalogState string `json:"catalog_state,omitempty"`
	// Counts describe a completed indexed generation while reconciliation runs
	CatalogRefreshing bool   `json:"catalog_refreshing,omitempty"`
	Complete          bool   `json:"complete"`
	CatalogRevision   uint64 `json:"catalog_revision,omitempty"`
	// Indexed files for directory views
	FilesTotal       int `json:"files_total,omitempty"`
	FilesRepresented int `json:"files_represented,omitempty"`
	DefinitionsTotal int `json:"definitions_total,omitempty"`
	AnchorsReturned  int `json:"anchors_returned,omitempty"`
	// Matching files observed across pattern pages
	MatchingFilesObserved int `json:"matching_files_observed,omitempty"`
	// Matching lines observed so far within source read budgets, including prior pattern pages
	MatchesObserved      int `json:"matches_observed,omitempty"`
	MatchSamplesReturned int `json:"match_samples_returned,omitempty"`
	ChildrenTotal        int `json:"children_total,omitempty"`
	ChildrenReturned     int `json:"children_returned,omitempty"`
	// Cursor used for this view
	Cursor string `json:"cursor,omitempty"`
	// Scope-bound continuation for the next directory or pattern page
	NextCursor string `json:"next_cursor,omitempty"`
}

// SyncEditorDocumentRequest
type SyncEditorDocumentRequest struct {
	ClientID    string `json:"client_id"`
	Incarnation string `json:"incarnation"`
	Epoch       int64  `json:"epoch"`
	BaseSHA256  string `json:"base_sha256,omitempty"`
	StateVector []byte `json:"state_vector,omitempty"`
}

// TaskScope Job metadata describing one worker dispatch. Paths are optional focus
// guidance and never an access, mutation, or promotion boundary.
// BaseOverlayID, when set, declares this dispatch as a stacked overlay
// based on another pending write overlay rather than primary. Stacked
// overlays rebase when their parent is promoted, and are orphaned when
// their parent is rejected.
type TaskScope struct {
	Mode TaskScopeMode `json:"mode,omitempty"`
	// Optional repo-relative focus globs
	Paths []string `json:"paths,omitempty"`
	// Optional. Names the parent write overlay this dispatch stacks on.
	// Empty means "based on primary." Must be a non-terminal overlay
	// (pending, applying, rebasing) on the same parent session, else
	// spawn rejects with OVERLAY_BASE_MISSING or OVERLAY_BASE_NOT_PENDING.
	BaseOverlayID string `json:"base_overlay_id,omitempty"`
}

// TextComparisonSource
type TextComparisonSource struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	// Null means the file is absent; an empty string is a present empty file.
	Before *string `json:"before"`
	// Null means the file is absent; an empty string is a present empty file.
	After *string `json:"after"`
}

// ThinkingBudgetRange
type ThinkingBudgetRange struct {
	Min int `json:"min"`
	Max int `json:"max,omitempty"`
}

// ThinkingCapabilities Native thinking controls resolved by the host from provider configuration, model discovery, and model-family rules, constrained by transport support.
type ThinkingCapabilities struct {
	State         string               `json:"state"`
	Efforts       []string             `json:"efforts,omitempty"`
	CanEnable     bool                 `json:"can_enable,omitempty"`
	CanDisable    bool                 `json:"can_disable,omitempty"`
	Budget        *ThinkingBudgetRange `json:"budget,omitempty"`
	DefaultEffort string               `json:"default_effort,omitempty"`
	Source        string               `json:"source,omitempty"`
}

// ThinkingOverride Fixed requires exactly one native effort, enabled flag, or token budget. Application carries no controls and explicitly restores application behavior.
type ThinkingOverride struct {
	ProviderID   string `json:"provider_id"`
	Model        string `json:"model"`
	Mode         string `json:"mode"`
	Effort       string `json:"effort,omitempty"`
	Enabled      *bool  `json:"enabled,omitempty"`
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
}

// TokenTotals
type TokenTotals struct {
	// Reported cache-read input tokens, included in prompt.
	CacheRead int `json:"cache_read,omitempty"`
	// Reported cache-write input tokens, included in prompt.
	CacheWrite int `json:"cache_write,omitempty"`
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
}

// ToolApprovalPayload
type ToolApprovalPayload struct {
	ToolCallID         string       `json:"tool_call_id,omitempty"`
	Plan               ApprovalPlan `json:"plan"`
	AIRationale        string       `json:"ai_rationale,omitempty"`
	AIRationalePending bool         `json:"ai_rationale_pending,omitempty"`
	JoinedCount        int          `json:"joined_count,omitempty"`
	JoinedToolCallIDs  []string     `json:"joined_tool_call_ids,omitempty"`
	// Live presentation override for an open card. Raised when joiners escalate via MaxConsequence; mint-time plan.presentation is unchanged.
	ConsequenceBand ConsequenceBand `json:"consequence_band,omitempty"`
	// Host-authored consequence line id paired with consequence_band when the live band is high_risk. Prefer over plan.presentation when set.
	ConsequenceCode ConsequenceCode `json:"consequence_code,omitempty"`
	Repeat          *ApprovalRepeat `json:"repeat,omitempty"`
}

// ToolCall
type ToolCall struct {
	Name    string                `json:"name"`
	ID      string                `json:"id"`
	Args    map[string]any        `json:"args"`
	ArgsRef *ChatContentReference `json:"args_ref,omitempty"`
	// Host-prepared card subtitle from the tool presentation catalog.
	DisplayTitle string `json:"display_title,omitempty"`
	// Provider's own tool-call token, retained only for AI providers that key server-side state on it. Never identity; empty when the provider emits no usable token.
	WireID string `json:"wire_id,omitempty"`
	// True when the provider stream ended at the completion token cap mid-arguments — args never parsed.
	ArgsTruncated bool `json:"args_truncated,omitempty"`
	// True when the provider finished normally but emitted invalid JSON arguments — args never parsed and nothing reached the tool.
	ArgsMalformed bool `json:"args_malformed,omitempty"`
	// Provider-specific tool-call metadata required on replay.
	ExtraContent map[string]any `json:"extra_content,omitempty"`
}

// ToolCompletion
type ToolCompletion struct {
	Operation    string `json:"operation"`
	State        string `json:"state"`
	ResourceKind string `json:"resource_kind,omitempty"`
	ResourceID   string `json:"resource_id,omitempty"`
	ResultHandle string `json:"result_handle,omitempty"`
}

// ToolDescriptor
type ToolDescriptor struct {
	DriverID string `json:"driver_id"`
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
}

// ToolFeedback
type ToolFeedback struct {
	Code    string           `json:"code"`
	Details map[string]any   `json:"details,omitempty"`
	Subject *FeedbackSubject `json:"subject,omitempty"`
}

// ToolProcessHandle A background handle the tool returned instead of an exit: a command job, or a read-only call that outlived its foreground wait and keeps running. The calling agent polls a command with command_output and a held call with held_result, and can sleep on either with wait(conditions=[{"kind":"process_done"}]); Den binds its chicklet to the session process mirror through the handle.
type ToolProcessHandle struct {
	// Live background handle, a command job or a held call, to poll, wait on, or stop.
	Handle string `json:"handle"`
	// The host's view when the result was written. It never flips on exit; the session process mirror reports that.
	Running bool `json:"running"`
}

// ToolProgress Measured discovery work. Counts are observations, not a completion estimate.
type ToolProgress struct {
	Phase         string `json:"phase"`
	FilesSearched int    `json:"files_searched"`
	FilesSkipped  int    `json:"files_skipped"`
	Matches       int    `json:"matches"`
}

// ToolResult
type ToolResult struct {
	Content      string                `json:"content"`
	ContentRef   *ChatContentReference `json:"content_ref,omitempty"`
	ToolArgsRef  *ChatContentReference `json:"tool_args_ref,omitempty"`
	DisplayTitle string                `json:"display_title,omitempty"`
	// Host-resolved readable target retained with the result and screened before title formatting. Exact routing identifiers remain in tool_args.
	DisplaySubject string             `json:"display_subject,omitempty"`
	Invocation     *InvocationReceipt `json:"invocation,omitempty"`
	// Name of the tool that produced this result. Carried on the result row so Den renders completed tool cards after the host replaces the assistant tool_calls batch on a reused row.
	Tool string `json:"tool,omitempty"`
	// ID of the tool call this result answers, anchoring search reveal to the originating tool chicklet.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Durable ID of the assistant row that issued this call.
	AssistantMessageID string `json:"assistant_message_id,omitempty"`
	// Immutable argument snapshot for the tool call.
	ToolArgs map[string]any    `json:"tool_args,omitempty"`
	Outcome  ToolResultOutcome `json:"outcome,omitempty"`
	// Guidance codes the host raised against this result, in raise order. A result carries every code raised, and the first is what card selection and quiet chrome bind to. Host-stamped as data by the producer that raised it; never parsed back out of `content`.
	Codes []string `json:"codes,omitempty"`
	// Structured facts for every raised guidance code, in the same order as codes. Agents branch on code and details, never on rendered content.
	Feedback []ToolFeedback `json:"feedback,omitempty"`
	// Quiet-chrome for Den verbose prefs. Host stamps from the policy registry (`guard:` emit, advisory/informational/expected_behavior categories, info|warning severity) or a structured reject `code` with no registry row. `benign` hides unless verbose; `normal` stays visible (worker_dispatch, SPEC_POSTURE, …).
	UiVisibility ToolResultUiVisibility `json:"ui_visibility,omitempty"`
	// Producer-stated worker dispatch identity.
	Dispatch *WorkerDispatch `json:"dispatch,omitempty"`
	// Producer-stated asynchronous or lifecycle completion fact.
	Completion         *ToolCompletion         `json:"completion,omitempty"`
	FileEditPreview    *FileEditPreview        `json:"file_edit_preview,omitempty"`
	PromotionPreviews  []FileEditPreview       `json:"promotion_previews,omitempty"`
	FileEdit           *FileEditSnapshot       `json:"file_edit,omitempty"`
	OverlayPromotion   *OverlayPromotion       `json:"overlay_promotion,omitempty"`
	Visual             *VisualArtifact         `json:"visual,omitempty"`
	CheckpointDecision *CheckpointDecisionMeta `json:"checkpoint_decision,omitempty"`
	// Bounded exceptional external-access detail for this tool result when the host recorded mediated endpoints, local-socket authority, direct IP, or detection citations on the action.
	ExternalAccess *ExternalAccess `json:"external_access,omitempty"`
	// Resolved skill behind a skills_read activation. The host stamps every field from the same catalog entry the activation envelope was rendered from, so the skill chicklet never parses that envelope's prose.
	Skill *SkillActivation `json:"skill,omitempty"`
	// Set when the tool returned while its process was still running; absent means the call finished. Presence is what keeps a chicklet live.
	Process *ToolProcessHandle `json:"process,omitempty"`
	// Set when submit_verdict recorded a review_loop verdict. The card renders these stated facts, never the result prose.
	Verdict *VerdictOutcome `json:"verdict,omitempty"`
}

// TrustFileChange
type TrustFileChange struct {
	ID string `json:"id"`
	// Root identity retained even after the root is detached.
	RootID     string           `json:"root_id"`
	RootLabel  string           `json:"root_label"`
	Path       string           `json:"path"`
	SurfaceIds []TrustSurfaceId `json:"surface_ids"`
	Kind       string           `json:"kind"`
	// Exact retained text when Trust was previously opened; empty for an addition.
	Before string `json:"before"`
	// Exact text captured for this opening; empty for a removal.
	After string `json:"after"`
}

// TrustSettingsResponse Device-wide off switches for what any project may supply. A quiet kill switch, not the primary UX — day-to-day choices are per project.
type TrustSettingsResponse struct {
	Surfaces []TrustSurfaceSetting `json:"surfaces"`
}

// TrustSurfaceItem One concrete thing a surface supplies.
type TrustSurfaceItem struct {
	Name string `json:"name"`
	// Human-readable qualifier — an MCP transport, a pack source.
	Detail string `json:"detail,omitempty"`
	// Attached project root containing this file item.
	RootID string `json:"root_id"`
	// Root-relative path for file items.
	Path string `json:"path"`
	// Prose size hint for instruction files.
	Lines int `json:"lines,omitempty"`
}

// TrustSurfaceSetting One configurable surface with its device-level switch.
type TrustSurfaceSetting struct {
	ID      TrustSurfaceId    `json:"id"`
	Label   string            `json:"label"`
	Group   TrustSurfaceGroup `json:"group"`
	Enabled bool              `json:"enabled"`
}

// TurnClock Active-time clock of one visible user turn in a session tree. A visible turn opens when a person's prompt starts an idle root session and lasts until the next such prompt; host and worker continuations accrue to it. The live clock is published on the edges where it transitions: the first executing prompt in, the last executing prompt out, and a new visible user turn. Clients tick locally between edges as active_ms + (now − running_at), so no event is needed while it merely advances. Transcript pages carry the same shape for each turn they open.
type TurnClock struct {
	// Root session the clock belongs to.
	SessionID string `json:"session_id"`
	// The user message that opened the visible turn. Omitted while the session tree has run without a person's prompt opening one.
	OpeningMessageID string `json:"opening_message_id,omitempty"`
	// Banked active time for the visible user turn in milliseconds. Accrues while at least one prompt in the session tree is executing, including an approval wait inside that execution. Pauses between executions. Resets when a new root user turn begins.
	ActiveMs int64 `json:"active_ms"`
	// The part of active_ms, banked in milliseconds, during which no execution in the session tree was waiting on a person's decision, such as a tool approval or an edit review.
	WorkMs int64 `json:"work_ms"`
	// Whether the clock is currently advancing (a turn is in flight).
	Running bool `json:"running"`
	// RFC 3339 instant the clock resumed; present only while running.
	RunningAt string `json:"running_at,omitempty"`
	// RFC 3339 instant the clock last paused, which is when the visible turn's work last finished. Omitted before the first pause; a continuation that resumes the clock keeps the previous value.
	SettledAt string `json:"settled_at,omitempty"`
}

// TurnComparisonSource What one turn did to one file: the state the turn found through the
// state it left. It starts where the turn baseline's scope comparison
// starts and ends at the turn's own last state, so a later turn's work
// never lands in an earlier turn's range.
type TurnComparisonSource struct {
	Kind          string `json:"kind"`
	FileID        string `json:"file_id"`
	SessionID     string `json:"session_id"`
	Turn          int    `json:"turn"`
	MarkUserEdits *bool  `json:"mark_user_edits,omitempty"`
}

// TurnLoad One decision the local decision engine recorded for a chat: what it offered a turn before the model's first call, or how it matched a request the model made. The transcript page carries the decisions of the turns it shows; the turn_load event carries each one as it lands. An abstained decision is recorded like any other, with its reason.
type TurnLoad struct {
	SessionID string          `json:"session_id"`
	Trigger   TurnLoadTrigger `json:"trigger"`
	// The user message that opened the turn the decision belongs to.
	OpeningMessageID string `json:"opening_message_id"`
	// The model call that asked: the request_tools or skills_read call, or the loadable tool call of a tool_event. Omitted for a turn decision.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Present when an engine answered.
	Engine *TurnLoadEngine `json:"engine,omitempty"`
	// True when no engine answered; reason says why.
	Abstained bool   `json:"abstained"`
	Reason    string `json:"reason,omitempty"`
	// Wall time of the decision.
	ElapsedMs int64         `json:"elapsed_ms"`
	Kind      *TurnLoadKind `json:"kind,omitempty"`
	// Tools the surface offers on every call of the turn, sorted by name; empty outside a turn decision.
	Floor []string `json:"floor"`
	// The standing tools beyond the floor once a turn decision applied, sorted by name; empty outside a turn decision.
	Tools []TurnLoadTool `json:"tools"`
	// Present on a turn decision.
	Boundary *TurnLoadBoundary `json:"boundary,omitempty"`
	// The skill whose body the turn carries: read at the turn decision, or by the tool_event that this receipt records.
	PreloadedSkill *TurnLoadSkill  `json:"preloaded_skill,omitempty"`
	Guides         *TurnLoadGuides `json:"guides,omitempty"`
	Match          *TurnLoadMatch  `json:"match,omitempty"`
}

// TurnLoadBoundary Expected reuse of the standing prefix when the turn opened; history compaction does not expire this tier.
type TurnLoadBoundary struct {
	Cache  TurnLoadCacheState `json:"cache"`
	Reason TurnLoadColdReason `json:"reason,omitempty"`
	// Time since the chat's last model call started, when there was one.
	IdleMs int64 `json:"idle_ms,omitempty"`
	// How long the route keeps an idle prefix; omitted when the provider documents no figure.
	ColdAfterMs int64 `json:"cold_after_ms,omitempty"`
}

// TurnLoadEngine The local decision model that answered.
type TurnLoadEngine struct {
	// Engine name.
	Name string `json:"name"`
	// Backbone model identifier.
	Model string `json:"model,omitempty"`
	// Decision head the answer came from.
	Head string `json:"head,omitempty"`
	// The receipt's one-line engine label, name/model#head.
	Label string `json:"label"`
}

// TurnLoadGuides How many loadable instruction units the turn rendered and left out of the prompt.
type TurnLoadGuides struct {
	Rendered int `json:"rendered"`
	Omitted  int `json:"omitted"`
}

// TurnLoadKind The turn kind the engine chose, with its calibrated confidence.
type TurnLoadKind struct {
	Value      string  `json:"value"`
	Confidence float64 `json:"confidence"`
}

// TurnLoadMatch How a request_tools or skills_read text resolved to names.
type TurnLoadMatch struct {
	// The text the model wrote.
	Need string          `json:"need"`
	By   TurnLoadMatchBy `json:"by"`
	// The tools or skills the text resolved to, best first.
	Names []string `json:"names"`
}

// TurnLoadSkill The skill automatically read for this turn.
type TurnLoadSkill struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// TurnLoadTool One tool the chat's standing set offered beyond the surface floor when the turn opened.
type TurnLoadTool struct {
	Tool   string             `json:"tool"`
	Source TurnLoadToolSource `json:"source"`
	// The engine's P(true) that the request needed the tool, for a predicted tool.
	P float64 `json:"p,omitempty"`
	// The request_tools text that loaded the tool, for a requested tool.
	Need string `json:"need,omitempty"`
	// The predicted tool this one loaded with, for a companion.
	With string `json:"with,omitempty"`
	// True when the tool joined on an earlier turn and stayed because the provider still held the prompt prefix.
	Carried bool `json:"carried"`
}

// UpdateApprovalsSettingsRequest Settings patch for approvals. Omit rules to preserve the global overlay. When rules is present, only deny/ask entries are accepted (any effect:allow → 400); overlay allow grants are kept. Durable allows mutate only via checkpoint resolve + POST …/grants/revoke.
type UpdateApprovalsSettingsRequest struct {
	// Optional. Omit to preserve overlay rules. Present = replace overlay deny/ask only (allows rejected).
	Rules []ApprovalRule `json:"rules,omitempty"`
	// When present, sets posture for the request scope (global or project). Omitted preserves the current overlay value (project inherits global when unset). Null resets to inherit from global (project scope only).
	ApprovalPosture *string `json:"approval_posture,omitempty"`
	// When present, toggles AI rationale on approval cards for the request scope. Omitted preserves the current value (project inherits global when unset). Null resets to inherit from global (project scope only).
	AIRationaleEnabled *bool `json:"ai_rationale_enabled,omitempty"`
	// Global scope: true disables the approval layer outright and false turns it back on. Project scope: false may restore asking for that project; true is rejected because repo content must never be able to disable approvals. Omitted preserves the current overlay value. Default false. Null resets to inherit from global (project scope only).
	NeverAsk *bool `json:"never_ask,omitempty"`
}

// UpdateBlueprintRequest Partial update — supply content and/or title (at least one). Title writes frontmatter. A provisional mint retargets onto the declared title once; `path` on the response is identity after that write.
type UpdateBlueprintRequest struct {
	Content *string `json:"content,omitempty"`
	// Display title written to frontmatter; may retarget a provisional path once
	Title *string `json:"title,omitempty"`
}

// UpdateDetectionPackRequest
type UpdateDetectionPackRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// UpdateEditorDocumentPresenceRequest
type UpdateEditorDocumentPresenceRequest struct {
	ClientID    string                `json:"client_id"`
	Incarnation string                `json:"incarnation"`
	Ranges      []EditorPresenceRange `json:"ranges"`
	Main        int                   `json:"main"`
}

// UpdateExtensionMetaPackRequest expected_revision and at least one change.
type UpdateExtensionMetaPackRequest struct {
	Enabled          *bool  `json:"enabled,omitempty"`
	ExpectedRevision string `json:"expected_revision"`
}

// UpdateExtensionPackRequest expected_revision and at least one change.
type UpdateExtensionPackRequest struct {
	Enabled          *bool  `json:"enabled,omitempty"`
	ExpectedRevision string `json:"expected_revision"`
}

// UpdateExtensionUnitRequest expected_revision and at least one change.
type UpdateExtensionUnitRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
	// Selected winner pack id, or null to clear the explicit own selection
	OwnPackID        *string `json:"own_pack_id,omitempty"`
	ExpectedRevision string  `json:"expected_revision"`
}

// UpdateFileSummariesSettingsRequest
type UpdateFileSummariesSettingsRequest struct {
	Enabled bool `json:"enabled,omitempty"`
}

// UpdateHostResourceRequest
type UpdateHostResourceRequest struct {
	Access HostResourceAccessSetting `json:"access,omitempty"`
}

// UpdateManagedSecretRequest Administers one capability. Omitted fields are left as they stand, and nothing here reaches the stored value. A revoked capability is terminal and accepts no update.
type UpdateManagedSecretRequest struct {
	Name    *string `json:"name,omitempty"`
	Purpose *string `json:"purpose,omitempty"`
	// Deadline after which the agent may no longer substitute this capability; null removes the deadline.
	AgentUseEndsAt *time.Time `json:"agent_use_ends_at,omitempty"`
	// Scope only widens. Promoting a chat capability to `project` lets it outlive the chat that created it, and requires a purpose. Narrowing a project capability has no honest target chat, so it is refused.
	Scope *string `json:"scope,omitempty"`
}

// UpdateMcpProviderRequest Patch enablement and/or mutable connection fields.
type UpdateMcpProviderRequest struct {
	Enabled     *bool           `json:"enabled,omitempty"`
	ToolLoading *McpToolLoading `json:"tool_loading,omitempty"`
	Command     *string         `json:"command,omitempty"`
	Args        *[]string       `json:"args,omitempty"`
	// HTTP MCP URL. A host:port without a scheme is accepted: loopback becomes http://, any other host becomes https://. Remote http:// is refused.
	URL *string `json:"url,omitempty"`
	// Stdio env map (write-only; omit to leave unchanged).
	Env *map[string]string `json:"env,omitempty"`
	// Static HTTP headers (write-only; omit to leave unchanged).
	Headers *map[string]string `json:"headers,omitempty"`
	// Write-only static token. Placement follows credential_wire (default Authorization: Bearer).
	Token          *string            `json:"token,omitempty"`
	CredentialWire *McpCredentialWire `json:"credential_wire,omitempty"`
	// Header name when credential_wire is header.
	CredentialHeader *string `json:"credential_header,omitempty"`
}

// UpdatePowerSettingsRequest
type UpdatePowerSettingsRequest struct {
	KeepAwakeWhileWorking bool `json:"keep_awake_while_working,omitempty"`
}

// UpdateProjectRequest
type UpdateProjectRequest struct {
	Name    *string `json:"name,omitempty"`
	Starred *bool   `json:"starred,omitempty"`
}

// UpdateProjectRootRequest
type UpdateProjectRootRequest struct {
	IsPrimary *bool `json:"is_primary,omitempty"`
	// Rename the display label / @label addressing token; empty rejected, unique per project (case-insensitive).
	Label *string `json:"label,omitempty"`
}

// UpdateProjectScannerRequest
type UpdateProjectScannerRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// UpdateProjectTrustRequest
type UpdateProjectTrustRequest struct {
	// Partial surface id to enabled; unnamed surfaces keep their value.
	Enabled map[string]bool `json:"enabled,omitempty"`
}

// UpdateProviderRequest
type UpdateProviderRequest struct {
	// Null restores the provider kind endpoint. Empty for kinds whose endpoint is derived.
	BaseURL *string `json:"base_url,omitempty"`
	// Human-facing display name. Null restores the instance id.
	Label *string `json:"label,omitempty"`
	// Optional catalog hint naming a conventional environment variable (docs / UI only). Does not configure or authenticate the AI provider — keys must be saved via the credential routes. Null restores the kind hint.
	APIKeyEnv *string `json:"api_key_env,omitempty"`
	// Whether this AI provider needs an API key. Null inherits the provider kind default.
	RequiresAPIKey *bool `json:"requires_api_key,omitempty"`
	// True trusts the destination this update resolves with detected credentials; false withdraws it. Omit to keep the stored decision, which the host withdraws on its own when the update changes the destination. Null withdraws the stored trust decision.
	SecretScreenTrusted *bool `json:"secret_screen_trusted,omitempty"`
	// Configured model overrides. Null clears stored overrides so live discovery supplies available models.
	Models []ProviderModelConfig `json:"models,omitempty"`
}

// UpdateReviewSettingsRequest
type UpdateReviewSettingsRequest struct {
	ReviewPaths []ContentReviewRule `json:"review_paths,omitempty"`
}

// UpdateScannerRequest
type UpdateScannerRequest struct {
	Engine              string             `json:"engine,omitempty"`
	ScopeKind           string             `json:"scope_kind,omitempty"`
	Command             []string           `json:"command,omitempty"`
	OutputParser        string             `json:"output_parser,omitempty"`
	MapperID            string             `json:"mapper_id,omitempty"`
	Categories          []string           `json:"categories,omitempty"`
	Label               string             `json:"label,omitempty"`
	Description         string             `json:"description,omitempty"`
	SkipIfBinaryMissing *bool              `json:"skip_if_binary_missing,omitempty"`
	Runtime             *ScanRuntimePolicy `json:"runtime,omitempty"`
	Env                 []string           `json:"env,omitempty"`
	// When replacing a host row with a full user external, must be external
	Driver string `json:"driver,omitempty"`
}

// UpdateSecurityScannersSettingsRequest
type UpdateSecurityScannersSettingsRequest struct {
	Enabled           bool              `json:"enabled,omitempty"`
	LandedChangeScope LandedChangeScope `json:"landed_change_scope,omitempty"`
	SourceVerify      SourceVerify      `json:"source_verify,omitempty"`
}

// UpdateSessionRequest One session lifecycle command. Exactly one of title, archived, pinned, or pin_position is required; compound commands are rejected without changing the session.
type UpdateSessionRequest struct {
	// User-set display title; empty after trim is rejected
	Title *string `json:"title,omitempty"`
	// true moves the session out of the working set (sidebar, attention, default list); false restores it. History and search are untouched.
	Archived *bool `json:"archived,omitempty"`
	// true adds the session to the end of its project's pinned chats; false unpins it. Archived and worker child sessions cannot be pinned.
	Pinned *bool `json:"pinned,omitempty"`
	// Moves a pinned session to this 1-based position in its project's pinned order; a position past the end moves it last. The project's pinned chats are renumbered 1…n.
	PinPosition *int `json:"pin_position,omitempty"`
}

// UpdateTrustSettingsRequest
type UpdateTrustSettingsRequest struct {
	// Partial surface id to enabled; unnamed surfaces keep their value.
	Enabled map[string]bool `json:"enabled,omitempty"`
}

// UpdateVerifySettingsRequest
type UpdateVerifySettingsRequest struct {
	Test string `json:"test,omitempty"`
}

// UpdateWebResearchProviderRequest
type UpdateWebResearchProviderRequest struct {
	Config map[string]string `json:"config,omitempty"`
}

// UpdateWebResearchSettingsRequest
type UpdateWebResearchSettingsRequest struct {
	Warming       *bool `json:"warming,omitempty"`
	GuessDomains  *bool `json:"guess_domains,omitempty"`
	SearchEnabled *bool `json:"search_enabled,omitempty"`
	// Complete replacement provider set when supplied. An empty array disables every search provider.
	EnabledProviders []string `json:"enabled_providers,omitempty"`
}

// VerdictOutcome A recorded review_loop verdict with its validated citation provenance. Terminal verdicts always carry grounding.
type VerdictOutcome struct {
	// The submitted verdict enum value.
	Verdict string `json:"verdict"`
	// True when the gate was satisfied and the host is advancing.
	Terminal bool `json:"terminal,omitempty"`
	// Review round counter after this submission.
	Attempt      int    `json:"attempt,omitempty"`
	IterationCap int    `json:"iteration_cap,omitempty"`
	EvidenceKey  string `json:"evidence_key,omitempty"`
	Phase        string `json:"phase,omitempty"`
	// Host citation audit of the verdict's cited evidence.
	Grounding *CitationGrounding `json:"grounding,omitempty"`
}

// VerifySettingsResponse
type VerifySettingsResponse struct {
	Scope             SettingsScope         `json:"scope"`
	Test              string                `json:"test"`
	VerifyPath        string                `json:"verify_path"`
	DetectedCommand   string                `json:"detected_command,omitempty"`
	DetectedSource    string                `json:"detected_source,omitempty"`
	BackendConfigured bool                  `json:"backend_configured"`
	SuggestionState   VerifySuggestionState `json:"suggestion_state,omitempty"`
	Dismissed         bool                  `json:"dismissed,omitempty"`
}

// VersionComparisonSource
type VersionComparisonSource struct {
	Kind      string `json:"kind"`
	VersionID string `json:"version_id"`
}

// VisualArtifact
type VisualArtifact struct {
	ID             string               `json:"id"`
	Mime           string               `json:"mime"`
	Bytes          []byte               `json:"bytes,omitempty"`
	StoreRef       bool                 `json:"store_ref,omitempty"`
	Source         VisualArtifactSource `json:"source"`
	Caption        string               `json:"caption,omitempty"`
	EvidenceHandle string               `json:"evidence_handle,omitempty"`
	// Held browser page that produced live-tool media.
	PageID string `json:"page_id,omitempty"`
	// Wall-clock start of a time-based visual recording.
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
	// Duration of a time-based visual recording in milliseconds.
	DurationMS int64 `json:"duration_ms,omitempty"`
	// Raster width in pixels, read from the stored image header. Absent for non-raster media.
	Width int `json:"width,omitempty"`
	// Raster height in pixels, read from the stored image header. Absent for non-raster media.
	Height int `json:"height,omitempty"`
	// When true, host attaches this image on vision-capable models
	Perceive bool `json:"perceive,omitempty"`
	// Originating tool_use id for perceive attach
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Assistant transcript row that originated this artifact.
	OriginMessageID string `json:"origin_message_id,omitempty"`
}

// WebResearchDirectCardContent
type WebResearchDirectCardContent struct {
	ProviderID string `json:"provider_id"`
	Kind       string `json:"kind"`
	Label      string `json:"label"`
}

// WebResearchDirectStatus
type WebResearchDirectStatus struct {
	Configured bool                         `json:"configured"`
	Card       WebResearchDirectCardContent `json:"card"`
}

// WebResearchIndexHealth
type WebResearchIndexHealth struct {
	WritesApplied  int64 `json:"writes_applied"`
	WritesDropped  int64 `json:"writes_dropped"`
	WritesFailed   int64 `json:"writes_failed"`
	QueueHighWater int64 `json:"queue_high_water"`
	LastEvictMs    int64 `json:"last_evict_ms"`
	LastEvictRows  int64 `json:"last_evict_rows"`
	LastEvictBytes int64 `json:"last_evict_bytes"`
	LastSearchMs   int64 `json:"last_search_ms"`
	MaxSearchMs    int64 `json:"max_search_ms"`
}

// WebResearchIndexStatus
type WebResearchIndexStatus struct {
	Available bool                      `json:"available"`
	Docs      int64                     `json:"docs"`
	Hosts     int64                     `json:"hosts"`
	Bytes     int64                     `json:"bytes"`
	Verified  int64                     `json:"verified"`
	Warmed    int64                     `json:"warmed"`
	WarmHits  int64                     `json:"warm_hits"`
	Warming   bool                      `json:"warming"`
	Health    WebResearchIndexHealth    `json:"health"`
	Activity  []WebResearchWarmActivity `json:"activity"`
}

// WebResearchProviderMeta
type WebResearchProviderMeta struct {
	ID                WebSearchProvider         `json:"id"`
	Kind              WebResearchProviderKind   `json:"kind"`
	Label             string                    `json:"label"`
	Roles             []WebResearchProviderRole `json:"roles"`
	DefaultEnabled    bool                      `json:"default_enabled,omitempty"`
	Configured        bool                      `json:"configured"`
	CredentialPresent bool                      `json:"credential_present"`
	CredentialSource  string                    `json:"credential_source,omitempty"`
	CredentialSlot    string                    `json:"credential_slot,omitempty"`
	// Catalog flag — when true, endpoint may use http and private/LAN addresses (still blocking loopback and link-local metadata).
	AllowPrivateEndpoint bool              `json:"allow_private_endpoint,omitempty"`
	Config               map[string]string `json:"config,omitempty"`
}

// WebResearchProvidersResponse
type WebResearchProvidersResponse struct {
	Direct    WebResearchDirectStatus   `json:"direct"`
	Providers []WebResearchProviderMeta `json:"providers"`
}

// WebResearchSettings
type WebResearchSettings struct {
	// Background index warming from explicitly shared URLs and explicit web activity.
	Warming bool `json:"warming"`
	// When results are thin, a model may scout hosts to fill the index.
	GuessDomains  bool `json:"guess_domains"`
	SearchEnabled bool `json:"search_enabled"`
	// Complete enabled provider set. An empty array enables no search provider.
	EnabledProviders []string `json:"enabled_providers"`
}

// WebResearchWarmActivity
type WebResearchWarmActivity struct {
	At         string   `json:"at"`
	Trigger    string   `json:"trigger"`
	Tier       string   `json:"tier,omitempty"`
	Topic      string   `json:"topic,omitempty"`
	Hosts      []string `json:"hosts,omitempty"`
	Pages      int      `json:"pages,omitempty"`
	DurationMs int64    `json:"duration_ms,omitempty"`
	SkipReason string   `json:"skip_reason,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
}

// WorkerBudgetRequest A running worker's request that its coordinator raise its tool-round ceiling.
type WorkerBudgetRequest struct {
	// Additional tool rounds the worker asked for beyond its ceiling at request time
	Rounds int `json:"rounds"`
	// Ceiling the worker asked for, bounded by the host maximum
	RequestedMax int `json:"requested_max"`
	// Worker-authored work the extra rounds would cover; data for the coordinator, not instructions
	RemainingWork []string `json:"remaining_work"`
	// Rounds the worker had completed when it asked
	ToolLoopsUsed int       `json:"tool_loops_used"`
	RequestedAt   time.Time `json:"requested_at"`
}

// WorkerChangeReport
type WorkerChangeReport struct {
	ChangedPaths   []string         `json:"changed_paths,omitempty"`
	WorkspaceDirty bool             `json:"workspace_dirty,omitempty"`
	MutationTools  []string         `json:"mutation_tools,omitempty"`
	SurveyTools    []string         `json:"survey_tools,omitempty"`
	ReceiptCount   int              `json:"receipt_count,omitempty"`
	DiffStat       []WorkerDiffStat `json:"diff_stat,omitempty"`
}

// WorkerCompletionFinding
type WorkerCompletionFinding struct {
	Path         string `json:"path"`
	Evidence     string `json:"evidence,omitempty"`
	Line         int    `json:"line,omitempty"`
	Excerpt      string `json:"excerpt,omitempty"`
	Note         string `json:"note,omitempty"`
	Claim        string `json:"claim,omitempty"`
	Adversary    string `json:"adversary,omitempty"`
	Precondition string `json:"precondition,omitempty"`
	Severity     string `json:"severity,omitempty"`
}

// WorkerCompletionReport
type WorkerCompletionReport struct {
	LegStatus           string                     `json:"leg_status"`
	FilesModified       []string                   `json:"files_modified,omitempty"`
	ObjectivesMet       []string                   `json:"objectives_met,omitempty"`
	RemainingRisk       []string                   `json:"remaining_risk,omitempty"`
	SuggestedNextTask   string                     `json:"suggested_next_task,omitempty"`
	Brief               string                     `json:"brief,omitempty"`
	Findings            []WorkerCompletionFinding  `json:"findings,omitempty"`
	CitedURLs           []string                   `json:"cited_urls,omitempty"`
	EvidenceObligations []WorkerEvidenceObligation `json:"evidence_obligations,omitempty"`
}

// WorkerContextUsage Per-worker context occupancy mirrored to the Den worker-card ring: prompt tokens on the latest turn against the model window, plus the compaction trigger point. Same shape the coordinator context ring uses.
type WorkerContextUsage struct {
	// Prompt tokens on the worker's latest turn — current window occupancy
	PromptTokens int `json:"prompt_tokens,omitempty"`
	// Model context window in tokens; the ring's denominator
	Window int `json:"window,omitempty"`
	// Prompt-token level at which compaction fires for this worker
	CompactionThreshold int `json:"compaction_threshold,omitempty"`
}

// WorkerDecisionRequest
type WorkerDecisionRequest struct {
	WorkerID       string             `json:"worker_id"`
	ChildSessionID string             `json:"child_session_id"`
	Question       string             `json:"question"`
	Options        []string           `json:"options"`
	BlockerClass   WorkerBlockerClass `json:"blocker_class"`
	// Optional session-tree VisualArtifact the worker wants judged
	ArtifactID string `json:"artifact_id,omitempty"`
	// Optional 2–4 session-tree visuals for a compare preference
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
}

// WorkerDependency
type WorkerDependency struct {
	WorkerID string `json:"worker_id"`
	State    string `json:"state"`
}

// WorkerDiffStat
type WorkerDiffStat struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

// WorkerDispatch
type WorkerDispatch struct {
	WorkerID       string `json:"worker_id"`
	ChildSessionID string `json:"child_session_id,omitempty"`
	AgentType      string `json:"agent_type,omitempty"`
	DelegationID   string `json:"delegation_id,omitempty"`
	LegID          string `json:"leg_id,omitempty"`
}

// WorkerEvent
type WorkerEvent struct {
	Dependencies    []WorkerDependency `json:"dependencies,omitempty"`
	WorkerID        string             `json:"worker_id"`
	Status          WorkerStatus       `json:"status"`
	AgentType       string             `json:"agent_type,omitempty"`
	ChildSessionID  string             `json:"child_session_id,omitempty"`
	ParentSessionID string             `json:"parent_session_id,omitempty"`
	// Worker assignment goal used for card titles.
	Brief        string            `json:"brief,omitempty"`
	MergeStatus  WorkerMergeStatus `json:"merge_status,omitempty"`
	MaxToolLoops int               `json:"max_tool_loops,omitempty"`
	// The worker's unanswered request for more tool rounds; absent once granted
	BudgetRequest        *WorkerBudgetRequest  `json:"budget_request,omitempty"`
	ToolLoopsUsed        int                   `json:"tool_loops_used,omitempty"`
	ToolCallsUsed        int                   `json:"tool_calls_used,omitempty"`
	TurnToolCalls        int                   `json:"turn_tool_calls,omitempty"`
	TurnToolsDone        int                   `json:"turn_tools_done,omitempty"`
	ContextUsage         *WorkerContextUsage   `json:"context_usage,omitempty"`
	WorkspacePreparation *WorkspacePreparation `json:"workspace_preparation,omitempty"`
	Result               *WorkerResult         `json:"result,omitempty"`
	Failure              *WorkerFailure        `json:"failure,omitempty"`
	Error                string                `json:"error,omitempty"`
}

// WorkerEvidenceObligation
type WorkerEvidenceObligation struct {
	Kind         string   `json:"kind"`
	Status       string   `json:"status"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
	Reason       string   `json:"reason,omitempty"`
}

// WorkerFailure Structured worker execute failure for Den (SSE worker topic and GET /v1/workers). Copy templates live in config/packs/painted-wolf/platform/host/user-notices.
type WorkerFailure struct {
	Code            string         `json:"code,omitempty"`
	Title           string         `json:"title,omitempty"`
	Message         string         `json:"message,omitempty"`
	SuggestedAction string         `json:"suggested_action,omitempty"`
	Actions         []NoticeAction `json:"actions,omitempty"`
	// Host-declared tier; always `non_catastrophic` on this surface.
	Tier NoticeTier `json:"tier,omitempty"`
	// Host-declared scope. A worker failure belongs to the session that spawned it.
	Scope NoticeScope `json:"scope,omitempty"`
	// Which declared resolution produced `tier`/`scope`.
	Resolution string `json:"resolution,omitempty"`
}

// WorkerJobChangedFile One overlay-modified file, addressed by root.
type WorkerJobChangedFile struct {
	// Attached root the path belongs to; two roots may share a relative path
	RootID string `json:"root_id"`
	// Root-relative path (slash-separated)
	Path string         `json:"path"`
	Op   SourceChangeOp `json:"op"`
}

// WorkerJobChangesResponse Files a worker job has changed on its own overlay. These have not landed
// on the project roots — the coordinator lands them with promote_overlay,
// and only then do they appear on the project's change lens.
type WorkerJobChangesResponse struct {
	Files []WorkerJobChangedFile `json:"files"`
}

// WorkerListResponse
type WorkerListResponse struct {
	NextCursor string       `json:"next_cursor,omitempty"`
	Workers    []WorkerTask `json:"workers"`
}

// WorkerPolicyFeedback Policy feedback frozen when the worker report was evaluated; retained through queue persistence and parent delivery.
type WorkerPolicyFeedback struct {
	Code    string            `json:"code"`
	Effect  string            `json:"effect"`
	Copy    map[string]string `json:"copy"`
	Details map[string]any    `json:"details,omitempty"`
}

// WorkerPromoteJobPathStatus
type WorkerPromoteJobPathStatus struct {
	WorkerID string                    `json:"worker_id"`
	Paths    []WorkerPromotePathStatus `json:"paths,omitempty"`
}

// WorkerPromotePathStatus
type WorkerPromotePathStatus struct {
	Path             string                    `json:"path"`
	Status           WorkerPromotePathOutcome  `json:"status"`
	Reason           string                    `json:"reason,omitempty"`
	HunkCount        int                       `json:"hunk_count,omitempty"`
	PromoteOrder     WorkerPromoteOrderKind    `json:"promote_order,omitempty"`
	PromoteAfter     []string                  `json:"promote_after,omitempty"`
	BlockedBy        []string                  `json:"blocked_by,omitempty"`
	PromoteOrderNote string                    `json:"promote_order_note,omitempty"`
	ConflictTier     WorkerPromoteConflictTier `json:"conflict_tier,omitempty"`
}

// WorkerResult
type WorkerResult struct {
	Summary          string                  `json:"summary,omitempty"`
	CompletionReport *WorkerCompletionReport `json:"completion_report,omitempty"`
	ChangeReport     *WorkerChangeReport     `json:"change_report,omitempty"`
	Grounding        *CitationGrounding      `json:"grounding,omitempty"`
	Response         string                  `json:"response,omitempty"`
	Status           string                  `json:"status,omitempty"`
	HintCode         string                  `json:"hint_code,omitempty"`
	PolicyFeedback   *WorkerPolicyFeedback   `json:"policy_feedback,omitempty"`
	HostAssembled    bool                    `json:"host_assembled,omitempty"`
}

// WorkerSummaryMeta
type WorkerSummaryMeta struct {
	SourceContext   *SourceContext         `json:"source_context,omitempty"`
	DelegationID    string                 `json:"delegation_id,omitempty"`
	LegID           string                 `json:"leg_id,omitempty"`
	WorkerID        string                 `json:"worker_id"`
	ChildSessionID  string                 `json:"child_session_id"`
	AgentType       string                 `json:"agent_type"`
	Status          WorkerSummaryStatus    `json:"status"`
	Grounding       *CitationGrounding     `json:"grounding,omitempty"`
	DecisionRequest *WorkerDecisionRequest `json:"decision_request,omitempty"`
	// Structured worker completion block.
	Envelope string `json:"envelope"`
}

// WorkflowBoundaryMeta
type WorkflowBoundaryMeta struct {
	Event           string `json:"event"`
	WorkflowID      string `json:"workflow_id,omitempty"`
	WorkflowVersion string `json:"workflow_version,omitempty"`
	Phase           string `json:"phase,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

// WorkflowControlRequest
type WorkflowControlRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Reason           string `json:"reason,omitempty"`
}

// WorkflowEvent
type WorkflowEvent struct {
	Event         WorkflowEventKind `json:"event,omitempty"`
	WorkflowID    string            `json:"workflow_id,omitempty"`
	WorkflowRunID string            `json:"workflow_run_id,omitempty"`
	// Authoritative run state at publication, in the same enriched shape the
	// run endpoints return. Present on every run transition, so a client
	// applies the run from the stream rather than a refetch that races the
	// `message` rows naming it. Absent on events that are not a run
	// transition (persisted manifest, gate pending).
	Run               *WorkflowRun          `json:"run,omitempty"`
	PreviousPhase     string                `json:"previous_phase,omitempty"`
	Phase             string                `json:"phase,omitempty"`
	Status            string                `json:"status,omitempty"`
	BranchInstruction string                `json:"branch_instruction,omitempty"`
	Guidance          []ScanGuidanceSummary `json:"guidance,omitempty"`
	Path              string                `json:"path,omitempty"`
	Version           string                `json:"version,omitempty"`
	CreatedBy         string                `json:"created_by,omitempty"`
}

// WorkflowExplainFullPass The full scan pass the phase requested or joined, read from the project's security overview.
type WorkflowExplainFullPass struct {
	AssessmentID string `json:"assessment_id"`
	ProjectID    string `json:"project_id"`
	// The attached folder the pass scans; absent for the project's primary folder.
	RootID string `json:"root_id,omitempty"`
}

// WorkflowExplainMeta A host-written note that says what a phase the host holds is doing. The text is the manifest's; progress names the live record the phase waits on.
type WorkflowExplainMeta struct {
	PhaseID string `json:"phase_id"`
	// One line, shown as the chicklet title.
	Summary  string                   `json:"summary"`
	Body     string                   `json:"body"`
	Progress *WorkflowExplainProgress `json:"progress,omitempty"`
}

// WorkflowExplainProgress The records whose progress the note shows. Absent when the phase waits on nothing with progress to show.
type WorkflowExplainProgress struct {
	FullPass *WorkflowExplainFullPass `json:"full_pass,omitempty"`
	Topology *WorkflowExplainTopology `json:"topology,omitempty"`
}

// WorkflowExplainTopology The topology stages the phase binds; their legs are read from the run's `ui.topology_legs`.
type WorkflowExplainTopology struct {
	Stages []string `json:"stages"`
}

// WorkflowFailure
type WorkflowFailure struct {
	// Stable machine-readable terminal failure code. `REPORT_NOT_ACCEPTED` ends a run whose report still failed the host's document check when its repairs ran out; the report is stored with its defects and stays downloadable.
	Code      string `json:"code"`
	Message   string `json:"message"`
	Phase     string `json:"phase,omitempty"`
	Stage     string `json:"stage,omitempty"`
	Retryable bool   `json:"retryable"`
}

// WorkflowFeedbackMeta
type WorkflowFeedbackMeta struct {
	PhaseID string `json:"phase_id"`
	// Card question shown to the user. ask_user: one decision (prefer choice options); markdown OK for brief context; host-capped at 800 Unicode code points so the prompt fits the 12rem composer-dock prompt area without scrolling.
	Prompt       string               `json:"prompt"`
	ResponseType FeedbackResponseType `json:"response_type"`
	Options      []string             `json:"options,omitempty"`
	AllowOther   bool                 `json:"allow_other,omitempty"`
	Answer       string               `json:"answer,omitempty"`
	// user when a person closes the card
	ResolvedBy string `json:"resolved_by,omitempty"`
	// The person who closed the card; present when resolved_by is user
	ResolvedByPersonID string `json:"resolved_by_person_id,omitempty"`
	// Optional session-tree VisualArtifact shown on the feedback card (single review/clarify)
	ArtifactID string `json:"artifact_id,omitempty"`
	// Compare preference — 2–4 session-tree visuals labeled A… by order
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
	// clarify = scope; review = judge one visual; compare = preference among artifact_ids
	Purpose string           `json:"purpose,omitempty"`
	Secret  *SecretInputMeta `json:"secret,omitempty"`
}

// WorkflowListResponse
type WorkflowListResponse struct {
	Workflows []WorkflowSummary `json:"workflows"`
	// Rejected workflow manifests keyed by their source path, with all validation diagnostics retained.
	Excluded map[string]ExcludedWorkflow `json:"excluded,omitempty"`
}

// WorkflowPresetSummary
type WorkflowPresetSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
}

// WorkflowRequestSpec
type WorkflowRequestSpec struct {
	Cadence  string `json:"cadence"`
	Question string `json:"question"`
	Default  string `json:"default,omitempty"`
}

// WorkflowRequestState
type WorkflowRequestState struct {
	Cadence  string `json:"cadence"`
	Status   string `json:"status"`
	Text     string `json:"text,omitempty"`
	Source   string `json:"source,omitempty"`
	Sequence int    `json:"sequence,omitempty"`
}

// WorkflowRun
type WorkflowRun struct {
	ID              string  `json:"id"`
	SessionID       string  `json:"session_id"`
	ProjectID       string  `json:"project_id,omitempty"`
	ParentRunID     *string `json:"parent_run_id,omitempty"`
	WorkflowID      string  `json:"workflow_id"`
	WorkflowVersion string  `json:"workflow_version"`
	// Catalog attach policy for this run's manifest, persisted with the run.
	// `session_create` marks the ambient-capable recipe; ambient root also requires no parent_run_id.
	AttachPolicy string            `json:"attach_policy,omitempty"`
	Status       WorkflowRunStatus `json:"status"`
	// Monotonic mutable-state revision for stale-command rejection.
	Revision     int64  `json:"revision"`
	CurrentPhase string `json:"current_phase"`
	// Project-relative path under the overlay blueprints/ directory
	BlueprintPath  string           `json:"blueprint_path,omitempty"`
	PauseReason    string           `json:"pause_reason,omitempty"`
	Failure        *WorkflowFailure `json:"failure,omitempty"`
	StartMessageID string           `json:"start_message_id,omitempty"`
	EndMessageID   string           `json:"end_message_id,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	PausedAt       *time.Time       `json:"paused_at,omitempty"`
	CompletedAt    *time.Time       `json:"completed_at,omitempty"`
	UI             *WorkflowRunUi   `json:"ui,omitempty"`
}

// WorkflowRunObligation
type WorkflowRunObligation struct {
	// Registered obligation kind, e.g. "scan".
	Kind string `json:"kind"`
	// off, complete, and failed settle the gate; empty means no tracked work.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// Kind-specific progress counters.
	Detail map[string]any `json:"detail,omitempty"`
}

// WorkflowRunPage Stable newest-first keyset page of workflow-run history.
type WorkflowRunPage struct {
	Runs []WorkflowRun `json:"runs"`
	// Opaque cursor for the next older page, bound to this session and status filter.
	NextCursor string `json:"next_cursor,omitempty"`
}

// WorkflowRunUi
type WorkflowRunUi struct {
	// Host-projected activity label from the active workflow manifest phase.
	CurrentPhaseLabel     string                `json:"current_phase_label"`
	HumanApprovalAwaiting bool                  `json:"human_approval_awaiting,omitempty"`
	Request               *WorkflowRequestSpec  `json:"request,omitempty"`
	RequestState          *WorkflowRequestState `json:"request_state,omitempty"`
	// Host-authorized document download after the workflow stored its enabled report and reached a terminal state: an accepted report completes the run, and one the host did not accept fails it with `REPORT_NOT_ACCEPTED`.
	ReportAvailable bool `json:"report_available,omitempty"`
	// The current phase is the workflow's last. A run rests here with status still "running" until the next advance finds no target, so a client that paints activity from the phase label must stop when this is true.
	PhaseTerminal   bool             `json:"phase_terminal,omitempty"`
	PlanRevisionAt  *time.Time       `json:"plan_revision_at,omitempty"`
	PendingFeedback *PendingFeedback `json:"pending_feedback,omitempty"`
	// Human-actor choice leaves on the current phase (manifest order). Coordinator-only edges are omitted. armed reflects optional when: readiness.
	ChoiceTransitions []ChoiceTransitionUi `json:"choice_transitions,omitempty"`
	// Run-scoped status for the current phase's host obligations.
	PhaseObligations []WorkflowRunObligation `json:"phase_obligations,omitempty"`
	// Every leg the run's topology plans for its bound phases, in topology order, with the status of its dispatched worker; a leg not yet dispatched is pending.
	TopologyLegs []WorkflowTopologyLeg `json:"topology_legs,omitempty"`
}

// WorkflowSummary
type WorkflowSummary struct {
	ID             string               `json:"id"`
	Version        string               `json:"version"`
	Name           string               `json:"name"`
	Description    string               `json:"description,omitempty"`
	Trigger        string               `json:"trigger,omitempty"`
	Request        *WorkflowRequestSpec `json:"request,omitempty"`
	InitialPosture string               `json:"initial_posture,omitempty"`
	// Optional topology id under config/topologies/ for orchestrated pipeline runs
	Topology string `json:"topology,omitempty"`
	// Semantic glyph name for launcher tiles (e.g. route, radar, shield)
	Icon string `json:"icon,omitempty"`
	// Surfaces this workflow as a quick-start tile in the new-session launcher
	Featured bool `json:"featured,omitempty"`
	// Workflow operates on an attached repo; launcher disables it for rootless draft sessions
	RequiresRepo bool `json:"requires_repo,omitempty"`
	// Manifest declares a blueprint block; surfaces in the Blueprints zone workflow launcher
	SupportsBlueprints bool `json:"supports_blueprints,omitempty"`
	// Opted-in workflow yields a downloadable grounded PDF report for completed runs
	ReportEnabled bool                    `json:"report_enabled,omitempty"`
	Phases        []string                `json:"phases,omitempty"`
	Presets       []WorkflowPresetSummary `json:"presets,omitempty"`
	Scope         WorkflowScope           `json:"scope,omitempty"`
}

// WorkflowTemplateListResponse
type WorkflowTemplateListResponse struct {
	Templates []WorkflowTemplateSummary `json:"templates"`
}

// WorkflowTemplateParameter
type WorkflowTemplateParameter struct {
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Default  any    `json:"default,omitempty"`
}

// WorkflowTemplateSummary
type WorkflowTemplateSummary struct {
	ID          string                               `json:"id"`
	Description string                               `json:"description"`
	Parameters  map[string]WorkflowTemplateParameter `json:"parameters"`
}

// WorkflowTopologyLeg
type WorkflowTopologyLeg struct {
	// Stable within the run; a pipeline stage's name, or a fan-out subtask's position as `subtask-<n>`.
	ID string `json:"id"`
	// The topology stage a phase binds; every fan-out subtask belongs to stage `fan_out`.
	Stage   string `json:"stage"`
	PhaseID string `json:"phase_id"`
	// The topology's label for the leg.
	Label  string    `json:"label"`
	Status LegStatus `json:"status"`
	// Ids of legs whose output this leg takes as input.
	WaitsFor []string `json:"waits_for,omitempty"`
}

// WorkspaceCacheClearResult
type WorkspaceCacheClearResult struct {
	ID   string       `json:"id"`
	OK   bool         `json:"ok"`
	Code ApiErrorCode `json:"code,omitempty"`
	// Host copy when ok is false; branch on code.
	Message string `json:"message,omitempty"`
}

// WorkspaceCacheStatus
type WorkspaceCacheStatus struct {
	ID           string `json:"id"`
	SourceRoot   string `json:"source_root"`
	LogicalBytes int64  `json:"logical_bytes"`
	// Best-effort allocated bytes retained by the immutable bridge cache
	AllocatedBytes int64     `json:"allocated_bytes"`
	LastUsedAt     time.Time `json:"last_used_at"`
}

// WorkspacePreparation
type WorkspacePreparation struct {
	Strategy WorkspaceProvisionStrategy `json:"strategy,omitempty"`
	Stage    string                     `json:"stage"`
	Files    int64                      `json:"files"`
	Bytes    int64                      `json:"bytes"`
	// Logical regular-file bytes when a source survey has completed
	TotalBytes int64 `json:"total_bytes,omitempty"`
}
