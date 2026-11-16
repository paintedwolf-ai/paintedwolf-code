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

// AllCompletionReportFindingDispositions returns every declared disposition.
func AllCompletionReportFindingDispositions() []CompletionReportFindingDisposition {
	return []CompletionReportFindingDisposition{
		CompletionReportFindingDispositionAct,
		CompletionReportFindingDispositionAccept,
		CompletionReportFindingDispositionHeld,
		CompletionReportFindingDispositionUnresolved,
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
