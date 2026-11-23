package apitest

import "github.com/lycaon/lycaon/pkg/api"

// AllMessageKinds returns every declared MessageKind.
func AllMessageKinds() []api.MessageKind {
	return []api.MessageKind{
		api.MessageKindWorkflowBoundary,
		api.MessageKindProgressComplete,
		api.MessageKindProgressUpdate,
		api.MessageKindWorkflowFeedback,
		api.MessageKindWorkflowExplain,
		api.MessageKindDraft,
		api.MessageKindIndexWarming,
		api.MessageKindSuperseded,
		api.MessageKindBlueprint,
		api.MessageKindAgentNote,
		api.MessageKindCompletionReport,
		api.MessageKindIterationCapCloseout,
		api.MessageKindHostKick,
		api.MessageKindCoordinatorGuidance,
		api.MessageKindHostNudge,
		api.MessageKindHostLoopWake,
		api.MessageKindHostSecretRedactionNotice,
		api.MessageKindUserContinuation,
	}
}

// AllCompletionReportScopes returns every declared CompletionReportScope.
func AllCompletionReportScopes() []api.CompletionReportScope {
	return []api.CompletionReportScope{
		api.CompletionReportScopeSession,
		api.CompletionReportScopePhase,
		api.CompletionReportScopeRun,
	}
}

// AllDraftStatuses returns every declared DraftStatus.
func AllDraftStatuses() []api.DraftStatus {
	return []api.DraftStatus{
		api.DraftStatusLive,
		api.DraftStatusCommitted,
		api.DraftStatusWithdrawn,
		api.DraftStatusRejected,
	}
}

// AllMessageVisibilities returns every declared MessageVisibility.
func AllMessageVisibilities() []api.MessageVisibility {
	return []api.MessageVisibility{
		api.MessageVisibilityTranscript,
		api.MessageVisibilityInternal,
	}
}

// AllMessageLiveStatuses returns the wire values for live messages.
func AllMessageLiveStatuses() []api.MessageLiveStatus {
	return []api.MessageLiveStatus{
		api.MessageLiveStatusStreaming,
		api.MessageLiveStatusComplete,
	}
}

// AllNavigationEntryKinds returns every durable project-path target kind.
func AllNavigationEntryKinds() []api.NavigationEntryKind {
	return []api.NavigationEntryKind{
		api.NavigationEntryKindFile,
		api.NavigationEntryKindFolder,
	}
}

// AllWorkerStatuses returns every declared WorkerStatus.
func AllWorkerStatuses() []api.WorkerStatus {
	return []api.WorkerStatus{
		api.WorkerStatusPending,
		api.WorkerStatusRunning,
		api.WorkerStatusWaiting,
		api.WorkerStatusComplete,
		api.WorkerStatusFailed,
		api.WorkerStatusCanceled,
		api.WorkerStatusHeld,
	}
}

// AllWorkerSummaryStatuses returns the worker summary status vocabulary.
func AllWorkerSummaryStatuses() []api.WorkerSummaryStatus {
	return []api.WorkerSummaryStatus{
		api.WorkerSummaryStatusComplete,
		api.WorkerSummaryStatusPartial,
		api.WorkerSummaryStatusFailed,
		api.WorkerSummaryStatusCanceled,
		api.WorkerSummaryStatusHeld,
		api.WorkerSummaryStatusOpen,
		api.WorkerSummaryStatusNeedsDecision,
	}
}

// AllCheckpointStatuses returns every declared CheckpointStatus.
func AllCheckpointStatuses() []api.CheckpointStatus {
	return []api.CheckpointStatus{
		api.CheckpointStatusPending,
		api.CheckpointStatusApproved,
		api.CheckpointStatusRejected,
		api.CheckpointStatusExpired,
		api.CheckpointStatusCanceled,
	}
}

// AllWorkflowBoundaryKinds returns every workflow_boundary message event kind.
func AllWorkflowBoundaryKinds() []api.WorkflowBoundaryKind {
	return []api.WorkflowBoundaryKind{
		api.WorkflowBoundaryKindStarted,
		api.WorkflowBoundaryKindExited,
		api.WorkflowBoundaryKindCanceled,
		api.WorkflowBoundaryKindCompleted,
		api.WorkflowBoundaryKindFailed,
		api.WorkflowBoundaryKindPaused,
		api.WorkflowBoundaryKindResumed,
		api.WorkflowBoundaryKindPausedOnChild,
		api.WorkflowBoundaryKindInterrupted,
	}
}
