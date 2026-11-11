package claim

import (
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

// WorkspaceNotice carries a structured preparation or recovery status for Host and Den.
type WorkspaceNotice struct {
	Severity      NoticeSeverity     `json:"severity"`
	ReasonCode    adapter.ReasonCode `json:"reason_code,omitempty"`
	Diagnostic    string             `json:"diagnostic,omitempty"`
	CopiedEntries int64              `json:"copied_entries,omitempty"`
	CopiedBytes   int64              `json:"copied_bytes,omitempty"`
}

// CheckpointStatus tracks checkpoint publication independently of workspace readiness and commands.
type CheckpointStatus struct {
	State               CheckpointState    `json:"state"`
	LatestCheckpointID  string             `json:"latest_checkpoint_id,omitempty"`
	PendingCheckpointID string             `json:"pending_checkpoint_id,omitempty"`
	FailedReasonCode    adapter.ReasonCode `json:"failed_reason_code,omitempty"`
	Diagnostic          string             `json:"diagnostic,omitempty"`
}

// WorkspaceStateSnapshot is the typed Host/Den workspace state surface.
// Command completion, workspace readiness, and checkpoint persistence are strictly distinct.
type WorkspaceStateSnapshot struct {
	Identity         ClaimIdentity        `json:"identity"`
	Adapter          AdapterMetadataFacts `json:"adapter"`
	SelectedRoots    []RootMappingRecord  `json:"selected_roots"`
	FocusPathHints   []string             `json:"focus_path_hints,omitempty"`
	Readiness        ReadinessState       `json:"readiness"`
	Command          CommandState         `json:"command"`
	GitInit          GitInitState         `json:"git_init"`
	Checkpoint       CheckpointStatus     `json:"checkpoint"`
	Notice           WorkspaceNotice      `json:"notice"`
	SupportedActions []RecoveryAction     `json:"supported_actions,omitempty"`
}

// DispatchWorkspaceReceipt is the wire-level receipt attached to worker dispatch and turn results.
type DispatchWorkspaceReceipt struct {
	WorkerID         string            `json:"worker_id"`
	ViewID           string            `json:"view_id"`
	AdapterID        adapter.AdapterID `json:"adapter_id"`
	Readiness        ReadinessState    `json:"readiness"`
	Command          CommandState      `json:"command"`
	GitInit          GitInitState      `json:"git_init"`
	Checkpoint       CheckpointStatus  `json:"checkpoint"`
	CopiedEntries    int64             `json:"copied_entries"`
	CopiedBytes      int64             `json:"copied_bytes"`
	Notice           WorkspaceNotice   `json:"notice"`
	SupportedActions []RecoveryAction  `json:"supported_actions,omitempty"`
}

// BuildReceipt projects a WorkspaceStateSnapshot into a DispatchWorkspaceReceipt without prose parsing.
func BuildReceipt(snap WorkspaceStateSnapshot) DispatchWorkspaceReceipt {
	return DispatchWorkspaceReceipt{
		WorkerID:         snap.Identity.WorkerID,
		ViewID:           snap.Identity.ViewID,
		AdapterID:        snap.Adapter.AdapterID,
		Readiness:        snap.Readiness,
		Command:          snap.Command,
		GitInit:          snap.GitInit,
		Checkpoint:       snap.Checkpoint,
		CopiedEntries:    snap.Adapter.CopiedEntries,
		CopiedBytes:      snap.Adapter.CopiedBytes,
		Notice:           snap.Notice,
		SupportedActions: append([]RecoveryAction(nil), snap.SupportedActions...),
	}
}

func buildReadySnapshot(meta ViewMetadataV2) WorkspaceStateSnapshot {
	gitState := GitInitInitialized
	if meta.Adapter.PendingGitInit {
		gitState = GitInitLazyPending
	}
	cpState := CheckpointNone
	if meta.Checkpoints.LastPublishedCheckpointID != "" {
		cpState = CheckpointPublished
	}
	snap := WorkspaceStateSnapshot{
		Identity:       meta.Identity,
		Adapter:        meta.Adapter,
		SelectedRoots:  append([]RootMappingRecord(nil), meta.SelectedRoots...),
		FocusPathHints: append([]string(nil), meta.FocusPathHints...),
		Readiness:      StateReady,
		Command:        CommandIdle,
		GitInit:        gitState,
		Checkpoint: CheckpointStatus{
			State:              cpState,
			LatestCheckpointID: meta.Checkpoints.LastPublishedCheckpointID,
		},
	}
	snap.SupportedActions = deriveSupportedActions(snap)
	return snap
}

func buildFailedPreparationSnapshot(
	req ClaimRequest,
	adpID adapter.AdapterID,
	reason adapter.ReasonCode,
	diagnostic string,
	observedEntries, observedBytes int64,
) WorkspaceStateSnapshot {
	prepKind := adapter.PreparationMounted
	vis := adapter.VisibilityLive
	if adpID == adapter.AdapterEagerCopy {
		prepKind = adapter.PreparationMaterialized
		vis = adapter.VisibilityFixed
	}
	sev := SeverityError
	if reason == adapter.ReasonWorkspaceDoesNotFit || reason == adapter.ReasonContextCanceled {
		sev = SeverityWarning
	}
	snap := WorkspaceStateSnapshot{
		Identity: req.Identity,
		Adapter: AdapterMetadataFacts{
			AdapterID:        adpID,
			PreparationKind:  prepKind,
			SourceVisibility: vis,
			CopiedEntries:    observedEntries,
			CopiedBytes:      observedBytes,
		},
		FocusPathHints: append([]string(nil), req.FocusPathHints...),
		Readiness:      StateFailedPreparation,
		Command:        CommandIdle,
		GitInit:        GitInitLazyPending,
		Checkpoint:     CheckpointStatus{State: CheckpointNone},
		Notice: WorkspaceNotice{
			Severity:      sev,
			ReasonCode:    reason,
			Diagnostic:    diagnostic,
			CopiedEntries: observedEntries,
			CopiedBytes:   observedBytes,
		},
	}
	snap.SupportedActions = deriveSupportedActions(snap)
	return snap
}

func deriveSupportedActions(snap WorkspaceStateSnapshot) []RecoveryAction {
	var actions []RecoveryAction
	switch snap.Readiness {
	case StatePreparing:
		actions = append(actions, ActionCancelClaim)
	case StateFailedPreparation:
		if snap.Notice.ReasonCode == adapter.ReasonWorkspaceDoesNotFit {
			actions = append(actions, ActionSelectSmallerWorkspace)
		}
		actions = append(actions, ActionRetryClaim, ActionCancelClaim)
	case StateRecoveryRequired:
		actions = append(actions, ActionRecoverView, ActionCancelClaim)
	default:
	}
	if snap.Checkpoint.State == CheckpointFailedPersistence {
		actions = append(actions, ActionRetryCheckpoint)
	}
	return actions
}
