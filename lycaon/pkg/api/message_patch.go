package api

// MergeMessagePatch applies live fields while preserving stable metadata.
func MergeMessagePatch(existing, patch Message) Message {
	merged := existing
	merged.ID = patch.ID
	if patch.Role != "" {
		merged.Role = patch.Role
	}
	// Content and its structured parts replace together.
	merged.Content = patch.Content
	merged.ContentParts = patch.ContentParts
	// Live patches do not clear compaction stamps.
	if patch.DietStamp != "" {
		merged.DietStamp = patch.DietStamp
	}
	if patch.DietStampSource != "" {
		merged.DietStampSource = patch.DietStampSource
	}
	if patch.ToolCalls != nil {
		merged.ToolCalls = patch.ToolCalls
	}
	if patch.ToolResult != nil {
		merged.ToolResult = patch.ToolResult
	}
	if patch.WorkerSummary != nil {
		merged.WorkerSummary = patch.WorkerSummary
	}
	if patch.Grounding != nil {
		merged.Grounding = patch.Grounding
	}
	if patch.SourceContext != nil {
		merged.SourceContext = patch.SourceContext
	}
	if patch.NavigationRefs != nil {
		merged.NavigationRefs = patch.NavigationRefs
	}
	if patch.CompactedChunk != nil {
		merged.CompactedChunk = patch.CompactedChunk
	}
	merged.CompactionCheckpoint = patch.CompactionCheckpoint
	if patch.Visibility != "" {
		merged.Visibility = patch.Visibility
	}
	if !patch.CreatedAt.IsZero() {
		merged.CreatedAt = patch.CreatedAt
	}
	if patch.Kind != "" {
		merged.Kind = patch.Kind
	}
	if patch.DraftStatus != "" {
		merged.DraftStatus = patch.DraftStatus
	}
	if patch.DraftVersionCount > 0 {
		merged.DraftVersionCount = patch.DraftVersionCount
	}
	if patch.WorkflowRunID != "" {
		merged.WorkflowRunID = patch.WorkflowRunID
	}
	if patch.WorkflowBoundary != nil {
		merged.WorkflowBoundary = patch.WorkflowBoundary
	}
	if patch.WorkflowFeedback != nil {
		merged.WorkflowFeedback = patch.WorkflowFeedback
	}
	if patch.Blueprint != nil {
		merged.Blueprint = patch.Blueprint
	}
	if patch.ProgressUpdate != nil {
		merged.ProgressUpdate = patch.ProgressUpdate
	}
	if patch.ProgressComplete != nil {
		merged.ProgressComplete = patch.ProgressComplete
	}
	if patch.CompletionReport != nil {
		merged.CompletionReport = patch.CompletionReport
	}
	if patch.IndexWarming != nil {
		merged.IndexWarming = patch.IndexWarming
	}
	if patch.ArtifactIDs != nil {
		merged.ArtifactIDs = patch.ArtifactIDs
	}
	if patch.EvidenceHandles != nil {
		merged.EvidenceHandles = patch.EvidenceHandles
	}
	return merged
}
