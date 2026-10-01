package api

// AllMessageKinds returns every declared MessageKind.
func AllMessageKinds() []MessageKind {
	return []MessageKind{
		MessageKindWorkflowBoundary,
		MessageKindProgressComplete,
		MessageKindProgressUpdate,
		MessageKindWorkflowFeedback,
		MessageKindWorkflowExplain,
		MessageKindDraft,
		MessageKindIndexWarming,
		MessageKindSuperseded,
		MessageKindBlueprint,
		MessageKindAgentNote,
		MessageKindCompletionReport,
		MessageKindIterationCapCloseout,
		MessageKindHostKick,
		MessageKindCoordinatorGuidance,
		MessageKindHostNudge,
		MessageKindHostLoopWake,
		MessageKindHostSecretRedactionNotice,
		MessageKindUserContinuation,
	}
}

// AllCompletionReportScopes returns every declared CompletionReportScope.
func AllCompletionReportScopes() []CompletionReportScope {
	return []CompletionReportScope{
		CompletionReportScopeSession,
		CompletionReportScopePhase,
		CompletionReportScopeRun,
	}
}

// AllCompletionReportFindingDispositions returns every declared disposition.
func AllCompletionReportFindingDispositions() []CompletionReportFindingDisposition {
	return []CompletionReportFindingDisposition{
		CompletionReportFindingDispositionAct,
		CompletionReportFindingDispositionAccept,
		CompletionReportFindingDispositionHeld,
	}
}

// AllCompletionReportAskEfforts returns every declared ask effort.
func AllCompletionReportAskEfforts() []CompletionReportAskEffort {
	return []CompletionReportAskEffort{
		CompletionReportAskEffortSmall,
		CompletionReportAskEffortMedium,
		CompletionReportAskEffortLarge,
	}
}

// AllDraftStatuses returns every declared DraftStatus.
func AllDraftStatuses() []DraftStatus {
	return []DraftStatus{
		DraftStatusLive,
		DraftStatusCommitted,
		DraftStatusWithdrawn,
		DraftStatusRejected,
	}
}

// AllMessageVisibilities returns every declared MessageVisibility.
func AllMessageVisibilities() []MessageVisibility {
	return []MessageVisibility{
		MessageVisibilityTranscript,
		MessageVisibilityInternal,
	}
}

// AllMessageLiveStatuses returns every declared MessageLiveStatus (runtime-derived
// wire field — never a persisted messages column).
func AllMessageLiveStatuses() []MessageLiveStatus {
	return []MessageLiveStatus{
		MessageLiveStatusStreaming,
		MessageLiveStatusComplete,
	}
}

// AllNavigationEntryKinds returns every durable project-path target kind.
func AllNavigationEntryKinds() []NavigationEntryKind {
	return []NavigationEntryKind{
		NavigationEntryKindFile,
		NavigationEntryKindFolder,
	}
}

// AllWorkerStatuses returns every declared WorkerStatus.
func AllWorkerStatuses() []WorkerStatus {
	return []WorkerStatus{
		WorkerStatusPending,
		WorkerStatusRunning,
		WorkerStatusWaiting,
		WorkerStatusComplete,
		WorkerStatusFailed,
		WorkerStatusCanceled,
		WorkerStatusHeld,
	}
}

// AllWorkerSummaryStatuses returns every declared WorkerSummaryStatus (canonical
// task-row patch tag on parent messages).
func AllWorkerSummaryStatuses() []WorkerSummaryStatus {
	return []WorkerSummaryStatus{
		WorkerSummaryStatusComplete,
		WorkerSummaryStatusPartial,
		WorkerSummaryStatusFailed,
		WorkerSummaryStatusCanceled,
		WorkerSummaryStatusHeld,
		WorkerSummaryStatusOpen,
		WorkerSummaryStatusNeedsDecision,
	}
}

// AllCheckpointStatuses returns every declared CheckpointStatus.
func AllCheckpointStatuses() []CheckpointStatus {
	return []CheckpointStatus{
		CheckpointStatusPending,
		CheckpointStatusApproved,
		CheckpointStatusRejected,
		CheckpointStatusExpired,
		CheckpointStatusCanceled,
	}
}

// AllWorkflowRunStatuses returns every declared WorkflowRunStatus.
func AllWorkflowRunStatuses() []WorkflowRunStatus {
	return []WorkflowRunStatus{
		WorkflowRunStatusRunning,
		WorkflowRunStatusPaused,
		WorkflowRunStatusPausedOnChild,
		WorkflowRunStatusComplete,
		WorkflowRunStatusFailed,
		WorkflowRunStatusCanceled,
		WorkflowRunStatusInterrupted,
	}
}

// AllWorkflowBoundaryKinds returns every workflow_boundary message event kind.
func AllWorkflowBoundaryKinds() []WorkflowBoundaryKind {
	return []WorkflowBoundaryKind{
		WorkflowBoundaryKindStarted,
		WorkflowBoundaryKindExited,
		WorkflowBoundaryKindCanceled,
		WorkflowBoundaryKindCompleted,
		WorkflowBoundaryKindFailed,
		WorkflowBoundaryKindPaused,
		WorkflowBoundaryKindResumed,
		WorkflowBoundaryKindPausedOnChild,
		WorkflowBoundaryKindInterrupted,
	}
}
