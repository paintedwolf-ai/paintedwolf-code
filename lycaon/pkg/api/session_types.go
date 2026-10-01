package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type SessionPosture string

const (
	SessionPostureSpec        SessionPosture = "spec"
	SessionPostureBuild       SessionPosture = "build"
	SessionPostureOrchestrate SessionPosture = "orchestrate"
	SessionPostureVet         SessionPosture = "vet"
)

// IsWorkerChild reports an isolated worker session spawned from a coordinator parent.
func (s *Session) IsWorkerChild() bool {
	return s != nil && strings.TrimSpace(s.ParentSessionID) != ""
}

// SessionListSort is the sort key for GET /v1/projects/{id}/sessions.
type SessionListSort string

const (
	SessionListSortActivity SessionListSort = "activity"
	SessionListSortCreated  SessionListSort = "created"
	SessionListSortTitle    SessionListSort = "title"
	// SessionListSortPin orders pinned chats by pin_rank; it requires pinned=true.
	SessionListSortPin SessionListSort = "pin"
)

// SessionListOrder is the sort direction for GET /v1/projects/{id}/sessions.
type SessionListOrder string

const (
	SessionListOrderAsc  SessionListOrder = "asc"
	SessionListOrderDesc SessionListOrder = "desc"
)

// DefaultSessionListLimit is the page size when GET .../sessions omits limit.
const DefaultSessionListLimit = 50

// MaxSessionListLimit caps a client-supplied session list page size.
const MaxSessionListLimit = 200

// SessionBackgroundDirectStatus is the liveness of a background direct-IP job.
type SessionBackgroundDirectStatus string

const (
	SessionBackgroundDirectStatusActive  SessionBackgroundDirectStatus = "active"
	SessionBackgroundDirectStatusUnknown SessionBackgroundDirectStatus = "unknown"
)

// AttachmentKind is the routing family the host detected for a stored body.
type AttachmentKind string

const (
	AttachmentKindImage    AttachmentKind = "image"
	AttachmentKindText     AttachmentKind = "text"
	AttachmentKindDocument AttachmentKind = "document"
	AttachmentKindVideo    AttachmentKind = "video"
)

// PromptReferenceKind is the wire enum for PromptReferencePart.kind.
type PromptReferenceKind string

const (
	PromptReferencePathFile   PromptReferenceKind = "path-file"
	PromptReferencePathFolder PromptReferenceKind = "path-folder"
	PromptReferenceArtifact   PromptReferenceKind = "artifact"
	PromptReferenceSearchHit  PromptReferenceKind = "search-hit"
)

// PromptReferencePart is the exact discriminated reference union used by
// prompt intake. Exactly one variant must be set.
type PromptReferencePart struct {
	PathFile   *PromptReferencePathFilePart   `json:"-"`
	PathFolder *PromptReferencePathFolderPart `json:"-"`
	Artifact   *PromptReferenceArtifactPart   `json:"-"`
	SearchHit  *PromptReferenceSearchHitPart  `json:"-"`
}

// PromptReferencePathFilePart identifies a whole file or an inclusive line
// range under one attached project root.
type PromptReferencePathFilePart struct {
	Kind      PromptReferenceKind `json:"kind"`
	ProjectID string              `json:"project_id"`
	RootID    string              `json:"root_id,omitempty"`
	Path      string              `json:"path"`
	StartLine int                 `json:"start_line,omitempty"`
	EndLine   int                 `json:"end_line,omitempty"`
}

// PromptReferencePathFolderPart identifies a folder under one attached root.
type PromptReferencePathFolderPart struct {
	Kind      PromptReferenceKind `json:"kind"`
	ProjectID string              `json:"project_id"`
	RootID    string              `json:"root_id,omitempty"`
	Path      string              `json:"path"`
}

// PromptReferenceArtifactPart re-links an existing visual artifact.
type PromptReferenceArtifactPart struct {
	Kind       PromptReferenceKind `json:"kind"`
	ProjectID  string              `json:"project_id"`
	ArtifactID string              `json:"artifact_id"`
}

// PromptReferenceSearchHitPart re-serves a stored evidence-index hit.
type PromptReferenceSearchHitPart struct {
	Kind      PromptReferenceKind `json:"kind"`
	ProjectID string              `json:"project_id"`
	SessionID string              `json:"session_id,omitempty"`
	SourceRef string              `json:"source_ref"`
	HitKind   string              `json:"hit_kind,omitempty"`
}

func NewPromptReferencePathFile(part PromptReferencePathFilePart) PromptReferencePart {
	part.Kind = PromptReferencePathFile
	return PromptReferencePart{PathFile: &part}
}

func NewPromptReferencePathFolder(part PromptReferencePathFolderPart) PromptReferencePart {
	part.Kind = PromptReferencePathFolder
	return PromptReferencePart{PathFolder: &part}
}

func NewPromptReferenceArtifact(part PromptReferenceArtifactPart) PromptReferencePart {
	part.Kind = PromptReferenceArtifact
	return PromptReferencePart{Artifact: &part}
}

func NewPromptReferenceSearchHit(part PromptReferenceSearchHitPart) PromptReferencePart {
	part.Kind = PromptReferenceSearchHit
	return PromptReferencePart{SearchHit: &part}
}

// ProjectID reports the project the active variant belongs to.
func (p PromptReferencePart) ProjectID() string {
	switch {
	case p.PathFile != nil:
		return p.PathFile.ProjectID
	case p.PathFolder != nil:
		return p.PathFolder.ProjectID
	case p.Artifact != nil:
		return p.Artifact.ProjectID
	case p.SearchHit != nil:
		return p.SearchHit.ProjectID
	default:
		return ""
	}
}

// Validate rejects an incomplete or multiply-populated union before use.
func (p PromptReferencePart) Validate() error {
	variants := 0
	if p.PathFile != nil {
		variants++
		if p.PathFile.Kind != PromptReferencePathFile || strings.TrimSpace(p.PathFile.ProjectID) == "" || strings.TrimSpace(p.PathFile.Path) == "" {
			return fmt.Errorf("invalid path-file reference")
		}
	}
	if p.PathFolder != nil {
		variants++
		if p.PathFolder.Kind != PromptReferencePathFolder || strings.TrimSpace(p.PathFolder.ProjectID) == "" || strings.TrimSpace(p.PathFolder.Path) == "" {
			return fmt.Errorf("invalid path-folder reference")
		}
	}
	if p.Artifact != nil {
		variants++
		if p.Artifact.Kind != PromptReferenceArtifact || strings.TrimSpace(p.Artifact.ProjectID) == "" || strings.TrimSpace(p.Artifact.ArtifactID) == "" {
			return fmt.Errorf("invalid artifact reference")
		}
	}
	if p.SearchHit != nil {
		variants++
		if p.SearchHit.Kind != PromptReferenceSearchHit || strings.TrimSpace(p.SearchHit.ProjectID) == "" || strings.TrimSpace(p.SearchHit.SourceRef) == "" {
			return fmt.Errorf("invalid search-hit reference")
		}
	}
	if variants != 1 {
		return fmt.Errorf("prompt reference must contain exactly one variant")
	}
	return nil
}

// MarshalJSON writes only the selected wire variant.
func (p PromptReferencePart) MarshalJSON() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	switch {
	case p.PathFile != nil:
		return json.Marshal(p.PathFile)
	case p.PathFolder != nil:
		return json.Marshal(p.PathFolder)
	case p.Artifact != nil:
		return json.Marshal(p.Artifact)
	default:
		return json.Marshal(p.SearchHit)
	}
}

// UnmarshalJSON decodes exactly one kind-specific wire variant. The local
// strict decoder rejects unknown cross-variant fields inside the union.
func (p *PromptReferencePart) UnmarshalJSON(data []byte) error {
	var tag struct {
		Kind PromptReferenceKind `json:"kind"`
	}
	if err := json.Unmarshal(data, &tag); err != nil {
		return err
	}
	decode := func(dst any) error {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		return dec.Decode(dst)
	}
	var out PromptReferencePart
	switch tag.Kind {
	case PromptReferencePathFile:
		var part PromptReferencePathFilePart
		if err := decode(&part); err != nil {
			return err
		}
		out.PathFile = &part
	case PromptReferencePathFolder:
		var part PromptReferencePathFolderPart
		if err := decode(&part); err != nil {
			return err
		}
		out.PathFolder = &part
	case PromptReferenceArtifact:
		var part PromptReferenceArtifactPart
		if err := decode(&part); err != nil {
			return err
		}
		out.Artifact = &part
	case PromptReferenceSearchHit:
		var part PromptReferenceSearchHitPart
		if err := decode(&part); err != nil {
			return err
		}
		out.SearchHit = &part
	default:
		return fmt.Errorf("unknown prompt reference kind %q", tag.Kind)
	}
	if err := out.Validate(); err != nil {
		return err
	}
	*p = out
	return nil
}

// RewindMode selects which point in a turn a rewind restores to.
type RewindMode string

// RewindModeBeforeTurn restores state before the anchor ask.
const RewindModeBeforeTurn RewindMode = "before_turn"

type MessageRole string

const (
	MessageRoleUser      MessageRole = "user"
	MessageRoleAssistant MessageRole = "assistant"
	MessageRoleTool      MessageRole = "tool"
	MessageRoleSystem    MessageRole = "system"
)

// MessageKind tags special message rows (e.g. workflow span boundaries).
type MessageKind string

const (
	MessageKindWorkflowBoundary MessageKind = "workflow_boundary"
	MessageKindProgressComplete MessageKind = "progress_complete"
	MessageKindProgressUpdate   MessageKind = "progress_update"
	MessageKindWorkflowFeedback MessageKind = "workflow_feedback"
	// MessageKindWorkflowExplain tags the host's note for a phase it holds.
	MessageKindWorkflowExplain MessageKind = "workflow_explain"
	// MessageKindDraft tags coordinator orchestration prose, rendered apart from the answer.
	MessageKindDraft MessageKind = "draft"
	// MessageKindIndexWarming tags a host-initiated warming row.
	MessageKindIndexWarming MessageKind = "index_warming"
	// MessageKindSuperseded tags a replaced worker completion report.
	MessageKindSuperseded MessageKind = "superseded"
	// MessageKindBlueprint tags a blueprint proposal card.
	MessageKindBlueprint MessageKind = "blueprint"
	// MessageKindAgentNote tags a user-visible grounded note.
	MessageKindAgentNote MessageKind = "agent_note"
	// MessageKindCompletionReport tags an accepted grounded report.
	MessageKindCompletionReport MessageKind = "completion_report"
	// MessageKindIterationCapCloseout tags an exhausted tool-loop budget.
	MessageKindIterationCapCloseout MessageKind = "iteration_cap_closeout"
	// MessageKindHostKick tags a rendered coordinator kick. HostSignalID identifies
	// the catalog anchor that produced the text.
	MessageKindHostKick MessageKind = "host_kick"
	// MessageKindCoordinatorGuidance tags a structured guidance retry. HostSignalID
	// carries the registered guidance Code.
	MessageKindCoordinatorGuidance MessageKind = "coordinator_guidance"
	// MessageKindHostNudge tags host-authored steering without a catalog anchor.
	MessageKindHostNudge MessageKind = "host_nudge"
	// MessageKindHostLoopWake tags the exact host loop re-entry row.
	MessageKindHostLoopWake MessageKind = "host_loop_wake"
	// MessageKindHostSecretRedactionNotice tags the transient model instruction
	// projected from durable host-secret-redaction provenance.
	MessageKindHostSecretRedactionNotice MessageKind = "host_secret_redaction_notice"
	// MessageKindUserContinuation keeps mid-turn direction in the visible turn.
	MessageKindUserContinuation MessageKind = "user_continuation"
)

// BlueprintTranscriptStatus is the lifecycle state of a blueprint transcript row.
type BlueprintTranscriptStatus string

const (
	BlueprintTranscriptStatusProposed         BlueprintTranscriptStatus = "proposed"
	BlueprintTranscriptStatusAwaitingApproval BlueprintTranscriptStatus = "awaiting_approval"
	BlueprintTranscriptStatusApproved         BlueprintTranscriptStatus = "approved"
	BlueprintTranscriptStatusRevised          BlueprintTranscriptStatus = "revised"
	BlueprintTranscriptStatusSuperseded       BlueprintTranscriptStatus = "superseded"
	BlueprintTranscriptStatusRejected         BlueprintTranscriptStatus = "rejected"
)

// BlueprintCardPhase mirrors Den inline-card phase chrome for a blueprint transcript row.
type BlueprintCardPhase string

const (
	BlueprintCardPhaseDrafting BlueprintCardPhase = "drafting"
	BlueprintCardPhaseReady    BlueprintCardPhase = "ready"
	BlueprintCardPhaseChanged  BlueprintCardPhase = "changed"
	BlueprintCardPhaseApproved BlueprintCardPhase = "approved"
	BlueprintCardPhaseBuilding BlueprintCardPhase = "building"
	// BlueprintCardPhaseRejected is the terminal record for a run that ended with the
	// blueprint still a draft. BlueprintTranscriptStatus says whether that was the
	// human answering no or the run being superseded.
	BlueprintCardPhaseRejected BlueprintCardPhase = "rejected"
)

// CompletionReportScope is the report boundary stored with a message.
type CompletionReportScope string

const (
	// CompletionReportScopeSession covers one chat session.
	CompletionReportScopeSession CompletionReportScope = "session"
	// CompletionReportScopePhase covers one workflow phase.
	CompletionReportScopePhase CompletionReportScope = "phase"
	// CompletionReportScopeRun covers one workflow run.
	CompletionReportScopeRun CompletionReportScope = "run"
)

// CompletionReportFindingDisposition is what a report finding asks for.
type CompletionReportFindingDisposition string

const (
	// CompletionReportFindingDispositionAct needs work.
	CompletionReportFindingDispositionAct CompletionReportFindingDisposition = "act"
	// CompletionReportFindingDispositionAccept is an accepted risk.
	CompletionReportFindingDispositionAccept CompletionReportFindingDisposition = "accept"
	// CompletionReportFindingDispositionHeld is a surface found sound.
	CompletionReportFindingDispositionHeld CompletionReportFindingDisposition = "held"
)

// CompletionReportAskEffort is how much work a report's ask commits to.
type CompletionReportAskEffort string

const (
	CompletionReportAskEffortSmall  CompletionReportAskEffort = "small"
	CompletionReportAskEffortMedium CompletionReportAskEffort = "medium"
	CompletionReportAskEffortLarge  CompletionReportAskEffort = "large"
)

// CompletionReportDefectCode is the rejection code of a document requirement
// a stored run report failed.
type CompletionReportDefectCode string

const (
	CompletionReportDefectCodeFenceUnreadable      CompletionReportDefectCode = "REPORT_FENCE_UNREADABLE"
	CompletionReportDefectCodeDocumentInvalid      CompletionReportDefectCode = "REPORT_DOCUMENT_INVALID"
	CompletionReportDefectCodeClaimUnreported      CompletionReportDefectCode = "REPORT_CLAIM_UNREPORTED"
	CompletionReportDefectCodeInventoryUnaccounted CompletionReportDefectCode = "REPORT_INVENTORY_UNACCOUNTED"
)

// DraftStatus is the terminal or live state of a coordinator draft slot.
type DraftStatus string

const (
	DraftStatusLive      DraftStatus = "live"
	DraftStatusCommitted DraftStatus = "committed"
	DraftStatusWithdrawn DraftStatus = "withdrawn"
	// DraftStatusRejected retains rejected coordinator attempts with internal visibility.
	DraftStatusRejected DraftStatus = "rejected"
)

// MessageLiveStatus is derived from the active turn at query or SSE time.
type MessageLiveStatus string

const (
	MessageLiveStatusStreaming MessageLiveStatus = "streaming"
	MessageLiveStatusComplete  MessageLiveStatus = "complete"
)

// MessageVisibility controls whether a message row appears in Den chat transcript.
type MessageVisibility string

const (
	MessageVisibilityTranscript MessageVisibility = "transcript"
	MessageVisibilityInternal   MessageVisibility = "internal"
)

// PromptCacheTier names the stable boundary a prompt-cache breakpoint closes.
type PromptCacheTier string

const (
	// PromptCacheTierNone marks no boundary.
	PromptCacheTierNone PromptCacheTier = ""
	// PromptCacheTierStanding closes the standing prefix: the system prompt
	// and the tool schemas, which change only when the host re-decides them.
	PromptCacheTierStanding PromptCacheTier = "standing"
	// PromptCacheTierHistory closes stable history, which grows every call.
	PromptCacheTierHistory PromptCacheTier = "history"
)

// WorkflowBoundaryKind is the event discriminator on workflow_boundary messages.
type WorkflowBoundaryKind string

const (
	WorkflowBoundaryKindStarted       WorkflowBoundaryKind = "started"
	WorkflowBoundaryKindExited        WorkflowBoundaryKind = "exited"
	WorkflowBoundaryKindCanceled      WorkflowBoundaryKind = "canceled"
	WorkflowBoundaryKindCompleted     WorkflowBoundaryKind = "completed"
	WorkflowBoundaryKindFailed        WorkflowBoundaryKind = "failed"
	WorkflowBoundaryKindPaused        WorkflowBoundaryKind = "paused"
	WorkflowBoundaryKindResumed       WorkflowBoundaryKind = "resumed"
	WorkflowBoundaryKindPausedOnChild WorkflowBoundaryKind = "paused_on_child"
	WorkflowBoundaryKindInterrupted   WorkflowBoundaryKind = "interrupted"
)

// FeedbackResponseType selects the workflow feedback input shape.
type FeedbackResponseType string

const (
	FeedbackResponseText         FeedbackResponseType = "text"
	FeedbackResponseSingleChoice FeedbackResponseType = "single_choice"
	FeedbackResponseMultiChoice  FeedbackResponseType = "multi_choice"
	FeedbackResponseSecret       FeedbackResponseType = "secret"
)

// NavigationEntryKind identifies the host-validated project entry type.
type NavigationEntryKind string

const (
	NavigationEntryKindFile   NavigationEntryKind = "file"
	NavigationEntryKindFolder NavigationEntryKind = "folder"
)

// MaxMessageNavigationRefs bounds durable interaction metadata per message.
const MaxMessageNavigationRefs = 128

// NavigationStatus describes resolution, independently of citation grounding.
type NavigationStatus string

const (
	NavigationPending     NavigationStatus = "pending"
	NavigationResolved    NavigationStatus = "resolved"
	NavigationAmbiguous   NavigationStatus = "ambiguous"
	NavigationMissing     NavigationStatus = "missing"
	NavigationUnavailable NavigationStatus = "unavailable"
)

// MessageOrigin identifies the producer of model-visible content.
type MessageOrigin string

const (
	MessageOriginHost       MessageOrigin = "host"
	MessageOriginUser       MessageOrigin = "user"
	MessageOriginModel      MessageOrigin = "model"
	MessageOriginTool       MessageOrigin = "tool"
	MessageOriginPeerAgent  MessageOrigin = "peer_agent"
	MessageOriginProject    MessageOrigin = "project"
	MessageOriginAttachment MessageOrigin = "attachment"
	MessageOriginRetrieval  MessageOrigin = "retrieval"
	MessageOriginUnknown    MessageOrigin = "unknown"
)

// ContentAuthority is the instruction authority assigned by the host. It is
// independent of both provider role and whether the content source is trusted.
type ContentAuthority string

const (
	ContentAuthoritySystem    ContentAuthority = "system"
	ContentAuthorityDeveloper ContentAuthority = "developer"
	ContentAuthorityUser      ContentAuthority = "user"
	ContentAuthorityNone      ContentAuthority = "none"
	ContentAuthorityUnknown   ContentAuthority = "unknown"
)

// ContentTrustTier records whether the host trusts the content channel. Trust
// never grants instruction authority.
type ContentTrustTier string

const (
	ContentTrustTierTrusted   ContentTrustTier = "trusted"
	ContentTrustTierUntrusted ContentTrustTier = "untrusted"
	ContentTrustTierUnknown   ContentTrustTier = "unknown"
)

// MessageReferenceKind identifies a retrieval target.
type MessageReferenceKind string

const (
	MessageReferenceKindPathFile   MessageReferenceKind = "path_file"
	MessageReferenceKindPathFolder MessageReferenceKind = "path_folder"
	MessageReferenceKindSearchHit  MessageReferenceKind = "search_hit"
)

type Message struct {
	ID        string           `json:"id"`
	Role      MessageRole      `json:"role"`
	Content   string           `json:"content"`
	Origin    MessageOrigin    `json:"origin"`
	Authority ContentAuthority `json:"authority"`
	TrustTier ContentTrustTier `json:"trust_tier"`
	// AuthorPersonID names the person who wrote a prompt message.
	AuthorPersonID string `json:"author_person_id,omitempty"`
	// ContentParts preserves instruction and data boundaries.
	ContentParts []MessageContentPart `json:"content_parts,omitempty"`
	// HostSecretRedaction records host-replaced secret spans.
	HostSecretRedaction *HostSecretRedactionMeta `json:"host_secret_redaction,omitempty"`
	Kind                MessageKind              `json:"kind,omitempty"`
	HostSignalID        string                   `json:"host_signal_id,omitempty"`
	// WorkerID identifies the worker run associated with this row.
	WorkerID         string                `json:"worker_id,omitempty"`
	WorkflowRunID    string                `json:"workflow_run_id,omitempty"`
	WorkflowBoundary *WorkflowBoundaryMeta `json:"workflow_boundary,omitempty"`
	ProgressComplete *ProgressCompleteMeta `json:"progress_complete,omitempty"`
	ProgressUpdate   *ProgressUpdateMeta   `json:"progress_update,omitempty"`
	WorkflowFeedback *WorkflowFeedbackMeta `json:"workflow_feedback,omitempty"`
	WorkflowExplain  *WorkflowExplainMeta  `json:"workflow_explain,omitempty"`
	IndexWarming     *IndexWarmingMeta     `json:"index_warming,omitempty"`
	Blueprint        *BlueprintMeta        `json:"blueprint,omitempty"`
	CompletionReport *CompletionReportMeta `json:"completion_report,omitempty"`
	// An empty ToolCalls slice clears the client batch; nil preserves it.
	ToolCalls            []ToolCall            `json:"tool_calls,omitempty"`
	ToolResult           *ToolResult           `json:"tool_result,omitempty"`
	WorkerSummary        *WorkerSummaryMeta    `json:"worker_summary,omitempty"`
	Grounding            *CitationGrounding    `json:"grounding,omitempty"`
	NavigationRefs       []NavigationReference `json:"navigation_refs,omitempty"`
	SourceContext        *SourceContext        `json:"source_context,omitempty"`
	CompactedChunk       *CompactedChunkMeta   `json:"compacted_chunk,omitempty"`
	CompactionCheckpoint bool                  `json:"compaction_checkpoint,omitempty"`
	Visibility           MessageVisibility     `json:"visibility,omitempty"`
	// PromptCacheBreakpoint marks a stable boundary a provider may cache up
	// to, and which tier it closes.
	PromptCacheBreakpoint PromptCacheTier `json:"prompt_cache_breakpoint,omitempty"`
	// ContextPinned protects session identity rows during prompt fitting.
	ContextPinned bool `json:"context_pinned,omitempty"`
	// Seq is the durable transcript mutation clock.
	Seq int64 `json:"seq,omitempty"`
	// ArtifactIDs references visual artifacts for coordinator "present" projection.
	ArtifactIDs []string `json:"artifact_ids,omitempty"`
	// EvidenceHandles lists handles minted with this tool result.
	EvidenceHandles []string `json:"evidence_handles,omitempty"`
	// DietStamp is an optional host strategy override (e.g. preserve_structure).
	DietStamp string `json:"diet_stamp,omitempty"`
	// DietStampSource disambiguates preserve_structure producers.
	DietStampSource string `json:"diet_stamp_source,omitempty"`
	// Ord is the immutable per-session creation ordinal.
	Ord               int64       `json:"ord,omitempty"`
	DraftVersionCount int         `json:"draft_version_count,omitempty"`
	DraftStatus       DraftStatus `json:"draft_status,omitempty"`
	// Status is the runtime-derived message state.
	Status MessageLiveStatus `json:"status,omitempty"`
	// GeneratingTokens estimates tokens for the active stream.
	GeneratingTokens int       `json:"generating_tokens,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	// ModelProjected prevents repeated data marking within one request.
	ModelProjected bool `json:"-"`
	// ModelReasoning holds provider-specific replay state outside HTTP and SSE.
	ModelReasoning *ModelReasoning `json:"-"`
}

// ModelReasoning stores provider-specific replay state for one assistant turn.
type ModelReasoning struct {
	ProviderID string `json:"provider_id,omitempty"`
	Model      string `json:"model,omitempty"`
	// Text is the flattened plaintext trace.
	Text string `json:"text,omitempty"`
	// Details preserves structured reasoning blocks byte-for-byte.
	Details []json.RawMessage `json:"details,omitempty"`
}

// RedactionKind is what one replacement claims.
type RedactionKind string

const (
	// RedactionKindSecret marks a detected secret that was removed.
	RedactionKindSecret RedactionKind = "secret"
	// RedactionKindManagedReference marks a protected value written as its
	// capability reference. The source still holds the value.
	RedactionKindManagedReference RedactionKind = "managed_reference"
	// RedactionKindObserverMask marks content a standing policy never shows to
	// observers. Nothing was detected, and nothing is claimed about the value.
	RedactionKindObserverMask RedactionKind = "observer_mask"
)

// RedactionSource is the evidence lens that justified one replacement.
type RedactionSource string

const (
	RedactionSourceShapeRule        RedactionSource = "shape_rule"
	RedactionSourceContainerHarvest RedactionSource = "container_harvest"
	RedactionSourceRememberedMatch  RedactionSource = "remembered_match"
	// RedactionSourcePolicy accompanies an observer mask, where the host decided
	// in advance rather than detecting anything.
	RedactionSourcePolicy RedactionSource = "policy"
)

// Occurrences is the number of replacements in this copy.
func (m *HostSecretRedactionMeta) Occurrences() int {
	if m == nil {
		return 0
	}
	return len(m.Spans)
}

// References is the number of protected values written as their reference.
func (m *HostSecretRedactionMeta) References() int {
	references := 0
	for _, span := range m.SpanList() {
		if span.Kind == RedactionKindManagedReference {
			references++
		}
	}
	return references
}

// Redactions is the number of replacements that are not references: removed
// secrets and observer masks.
func (m *HostSecretRedactionMeta) Redactions() int {
	return m.Occurrences() - m.References()
}

// SpanList is the replacements in this copy, empty when nothing was replaced.
func (m *HostSecretRedactionMeta) SpanList() []RedactedSpan {
	if m == nil {
		return nil
	}
	return m.Spans
}

// NewHostSecretRedactionMeta states the provenance of one screened copy,
// returning nil when nothing was replaced so presence stays authoritative.
func NewHostSecretRedactionMeta(spans []RedactedSpan) *HostSecretRedactionMeta {
	if len(spans) == 0 {
		return nil
	}
	return &HostSecretRedactionMeta{Spans: spans}
}

// DefaultTranscriptPageLimit is the page size when GET .../messages omits
// limit; Den mirrors the value.
const DefaultTranscriptPageLimit = 100

// MaxTranscriptPageLimit caps a client-supplied limit.
const MaxTranscriptPageLimit = 500

// TranscriptPageQuery selects a transcript window by exclusive ordinal bound.
type TranscriptPageQuery struct {
	Limit    int    `json:"limit"`
	Before   *int64 `json:"before,omitempty"`
	After    *int64 `json:"after,omitempty"`
	WorkerID string `json:"worker_id,omitempty"`
}

// EffectiveLimit returns the clamped page size.
func (q TranscriptPageQuery) EffectiveLimit() int {
	if q.Limit <= 0 {
		return DefaultTranscriptPageLimit
	}
	if q.Limit > MaxTranscriptPageLimit {
		return MaxTranscriptPageLimit
	}
	return q.Limit
}

// WorkerSummaryStatus is the worker state on parent messages.
type WorkerSummaryStatus string

const (
	WorkerSummaryStatusComplete      WorkerSummaryStatus = "complete"
	WorkerSummaryStatusPartial       WorkerSummaryStatus = "partial"
	WorkerSummaryStatusFailed        WorkerSummaryStatus = "failed"
	WorkerSummaryStatusCanceled      WorkerSummaryStatus = "canceled"
	WorkerSummaryStatusHeld          WorkerSummaryStatus = "held"
	WorkerSummaryStatusOpen          WorkerSummaryStatus = "open"
	WorkerSummaryStatusNeedsDecision WorkerSummaryStatus = "needs_decision"
)

// WorkerSummaryLegSucceeded reports output eligible for synthesis or merge.
func WorkerSummaryLegSucceeded(status WorkerSummaryStatus) bool {
	switch status {
	case WorkerSummaryStatusComplete, WorkerSummaryStatusOpen:
		return true
	default:
		return false
	}
}

// CitationVerdict is the three-tier host resolution outcome on citation wire fields.
type CitationVerdict string

const (
	CitationVerdictMatched      CitationVerdict = "matched"
	CitationVerdictTraced       CitationVerdict = "traced"
	CitationVerdictUnverifiable CitationVerdict = "unverifiable"
)

// CitationGroundingCheckStatus is the host-authored semantic outcome of a grounding check.
type CitationGroundingCheckStatus string

const (
	CitationGroundingCheckStatusPassed   CitationGroundingCheckStatus = "passed"
	CitationGroundingCheckStatusFailed   CitationGroundingCheckStatus = "failed"
	CitationGroundingCheckStatusAdvisory CitationGroundingCheckStatus = "advisory"
)

// CitationGroundingCheckKind classifies a grounding check for UI grouping.
const (
	CitationGroundingCheckKindCitation  = "citation"
	CitationGroundingCheckKindLifecycle = "lifecycle"
)

// SpawnChildRequest creates an isolated worker child session.
type SpawnChildRequest struct {
	AgentType    string   `json:"agent_type"`
	Prompt       string   `json:"prompt"`
	Files        []string `json:"files,omitempty"`
	MaxToolLoops int      `json:"max_tool_loops,omitempty"`
	// WorkerJobID identifies the worker run associated with the initial assignment row.
	WorkerJobID string `json:"-"`
}

type ToolResultOutcome string

const (
	ToolResultOutcomeCompleted ToolResultOutcome = "completed"
	ToolResultOutcomeRejected  ToolResultOutcome = "rejected"
	ToolResultOutcomeError     ToolResultOutcome = "error"
)

type ToolResultUiVisibility string

const (
	ToolResultUiVisibilityNormal ToolResultUiVisibility = "normal"
	ToolResultUiVisibilityBenign ToolResultUiVisibility = "benign"
)

// InvocationStatus is the durable state of one registered tool call.
type InvocationStatus string

const (
	InvocationStatusRunning     InvocationStatus = "running"
	InvocationStatusCompleted   InvocationStatus = "completed"
	InvocationStatusRejected    InvocationStatus = "rejected"
	InvocationStatusError       InvocationStatus = "error"
	InvocationStatusInterrupted InvocationStatus = "interrupted"
)

// Failure classes say whose fault an invocation outcome was.
const (
	// FailureClassOwnerError reports failure during subsystem execution.
	FailureClassOwnerError = "owner_error"
	// FailureClassHostRejection is a host rule declining a call as formed.
	FailureClassHostRejection = "host_rejection"
	// FailureClassPolicyRejection is a policy unit declining a call as formed.
	FailureClassPolicyRejection = "policy_rejection"
	// FailureClassContentPolicyRejection is a content rule declining a call.
	FailureClassContentPolicyRejection = "content_policy_rejection"
	// FailureClassIsolationRejection is a typed generic-boundary stop. The
	// selected operation owner remains unchanged and may not have been invoked.
	FailureClassIsolationRejection = "isolation_rejection"
	// FailureClassHostFault is the host failing to record an outcome its
	// caller stated; neither the caller nor the subsystem owner caused it.
	FailureClassHostFault = "host_fault"
)

// CallerFault reports whether this outcome describes something the caller did.
// Only these belong in repetition accounting. An outcome with no failure
// recorded is a plain rejection and counts.
func (f *InvocationFailure) CallerFault() bool {
	return f == nil || (f.Class != FailureClassOwnerError && f.Class != FailureClassHostFault)
}

// SourceVerdict enumerates the terminal readings of a source run.
const (
	SourceVerdictPassed       = "passed"
	SourceVerdictFailed       = "failed"
	SourceVerdictUnverifiable = "unverifiable"
)

// VisualArtifactSource names the producer of a VisualArtifact.
type VisualArtifactSource string

const (
	VisualArtifactSourceRender    VisualArtifactSource = "render"
	VisualArtifactSourceCapture   VisualArtifactSource = "capture"
	VisualArtifactSourceFetch     VisualArtifactSource = "fetch"
	VisualArtifactSourceUser      VisualArtifactSource = "user"
	VisualArtifactSourceWorkspace VisualArtifactSource = "workspace"
)

// PresentableStillVisualMime reports whether closeout may present the artifact.
func PresentableStillVisualMime(mime string) bool {
	mime = strings.TrimSpace(mime)
	if mime == "" {
		return false
	}
	lower := strings.ToLower(mime)
	if strings.HasPrefix(lower, "video/") {
		return false
	}
	if strings.EqualFold(mime, "application/vnd.lycaon.filmstrip+zip") {
		return true
	}
	return strings.HasPrefix(lower, "image/")
}

// PresentableStill reports whether this artifact should ride closeout artifact_ids.
func (v VisualArtifact) PresentableStill() bool {
	return PresentableStillVisualMime(v.Mime)
}

// ArtifactReferenceKind names a durable claim on an artifact.
type ArtifactReferenceKind string

const (
	// ArtifactReferenceKindMessagePresent is a coordinator present row.
	ArtifactReferenceKindMessagePresent ArtifactReferenceKind = "message_present"
	// ArtifactReferenceKindMessageAttachment is a human attachment on a prompt.
	ArtifactReferenceKindMessageAttachment ArtifactReferenceKind = "message_attachment"
	// ArtifactReferenceKindToolResult is the visual a tool result carries.
	ArtifactReferenceKindToolResult ArtifactReferenceKind = "tool_result"
	// ArtifactReferenceKindProjectCover is the project's home-card cover.
	ArtifactReferenceKindProjectCover ArtifactReferenceKind = "project_cover"
)

// PrimaryCode is the first code raised against this result, or empty. Card
// selection and quiet chrome bind to one code; the rest stay addressable.
func (t *ToolResult) PrimaryCode() string {
	if t == nil || len(t.Codes) == 0 {
		return ""
	}
	return t.Codes[0]
}
