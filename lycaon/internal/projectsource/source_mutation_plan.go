package projectsource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/pkg/api"
)

type sourceMutationStatus string

const (
	sourceMutationPrepared    sourceMutationStatus = "prepared"
	sourceMutationFileApplied sourceMutationStatus = "file_applied"
	sourceMutationCommitted   sourceMutationStatus = "committed"
	sourceMutationDiverged    sourceMutationStatus = "diverged"
	sourceMutationFailed      sourceMutationStatus = "failed"
)

// Disposal selects the filesystem effect; recovery is described by the plan.
type sourceDisposal string

const (
	sourceDisposalTrash   sourceDisposal = "trash"
	sourceDisposalDiscard sourceDisposal = "discard"
)

// sourceMutationAgent identifies the tool call responsible for an operation.
type sourceMutationAgent struct {
	JobID         string                  `json:"job_id,omitempty"`
	ToolCallID    string                  `json:"tool_call_id,omitempty"`
	ToolName      string                  `json:"tool_name,omitempty"`
	WorkspaceKind api.SourceWorkspaceKind `json:"workspace_kind,omitempty"`
}

// Admission identity stays flat in durable mutation plans.
type sourceMutationAttribution struct {
	Agent       *sourceMutationAgent `json:"agent,omitempty"`
	ProjectID   string               `json:"project_id"`
	BranchID    sourcebranch.ID      `json:"branch_id,omitempty"`
	WorkspaceID string               `json:"workspace_id"`
	SessionID   string               `json:"session_id,omitempty"`
	Turn        int                  `json:"turn,omitempty"`
	PersonID    string               `json:"person_id,omitempty"`
	BatchID     string               `json:"batch_id,omitempty"`
	Cause       string               `json:"cause,omitempty"`
}

type sourceMutationContent struct {
	Encoding             string `json:"encoding,omitempty"`
	BaseSHA256           string `json:"base_sha256,omitempty"`
	AfterSHA             string `json:"after_sha256,omitempty"`
	Before               []byte `json:"before,omitempty"`
	After                []byte `json:"after,omitempty"`
	BeforeSize           int64  `json:"before_size,omitempty"`
	AfterSize            int64  `json:"after_size,omitempty"`
	FileID               string `json:"file_id,omitempty"`
	DerivedFromVersionID string `json:"derived_from_version_id,omitempty"`
}

type sourceMutationPublication struct {
	CrossVolume         bool   `json:"cross_volume,omitempty"`
	HoldAbs             string `json:"hold_abs,omitempty"`
	MoveCleanupStarted  bool   `json:"move_cleanup_started,omitempty"`
	HoldStarted         bool   `json:"hold_started,omitempty"`
	DestinationIdentity string `json:"destination_identity,omitempty"`
	EffectStarted       bool   `json:"effect_started,omitempty"`
	StageIdentity       string `json:"stage_identity,omitempty"`
	PublicationMode     uint32 `json:"publication_mode,omitempty"`
	StageAbs            string `json:"stage_abs,omitempty"`
	DeleteIdentity      string `json:"delete_identity,omitempty"`
	DeleteStarted       bool   `json:"delete_started,omitempty"`
	EntryIdentity       string `json:"entry_identity,omitempty"`
}

type sourceMutationRecovery struct {
 NativeTrash *sourceTrashRecovery `json:"native_trash,omitempty"`
	RecoveryID    string         `json:"recovery_id,omitempty"`
	RecoveryCount int64          `json:"recovery_count,omitempty"`
	Disposal      sourceDisposal `json:"disposal,omitempty"`
	TreeSHA       string         `json:"tree_sha256,omitempty"`
}

type sourceMutationPlan struct {
	sourceMutationContent
	sourceMutationPublication
	sourceMutationRecovery
	sourceMutationAttribution
	AgentEffect       *sourceeffect.Plan   `json:"agent_effect,omitempty"`
	Kind              string               `json:"kind"`
	RootID            string               `json:"root_id"`
	RootPath          string               `json:"root_path"`
	Path              string               `json:"path,omitempty"`
	FromPath          string               `json:"from_path,omitempty"`
	ToPath            string               `json:"to_path,omitempty"`
	AbsPath           string               `json:"abs_path,omitempty"`
	FromAbs           string               `json:"from_abs,omitempty"`
	ToAbs             string               `json:"to_abs,omitempty"`
	EntryKind         SourceEntryKind      `json:"entry_kind,omitempty"`
	Recursive         bool                 `json:"recursive,omitempty"`
	Changed           bool                 `json:"changed"`
	HistoryEntryID    string               `json:"history_entry_id,omitempty"`
	HistoryTransition string               `json:"history_transition,omitempty"`
	CreateParents     bool                 `json:"create_parents,omitempty"`
	Writes            []sourceMutationPlan `json:"writes,omitempty"`
	Response          json.RawMessage      `json:"response"`
}

type sourceMutationRow struct {
	ID, ProjectID, Kind, InputDigest string
	Plan                             sourceMutationPlan
	Status                           sourceMutationStatus
	Response                         json.RawMessage
	Error                            string
	CreatedAt, UpdatedAt             time.Time
}

func sourceMutationDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
