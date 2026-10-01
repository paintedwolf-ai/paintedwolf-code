package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/pkg/api"
)

// messageFromRow maps a generated messages row onto the wire type.
func messageFromRow(r db.ListSessionMessagesRow) (api.Message, error) {
	msg := api.Message{
		ID:                   r.ID,
		Role:                 api.MessageRole(r.Role),
		Content:              r.Content,
		Origin:               api.MessageOrigin(r.Origin),
		Authority:            api.ContentAuthority(r.Authority),
		TrustTier:            api.ContentTrustTier(r.TrustTier),
		AuthorPersonID:       db.StringFromNull(r.AuthorPersonID),
		Seq:                  r.Seq,
		Ord:                  r.Ord,
		DraftVersionCount:    int(r.DraftVersionCount),
		DraftStatus:          api.DraftStatus(strings.TrimSpace(r.DraftStatus)),
		Visibility:           api.MessageVisibility(r.Visibility),
		Kind:                 api.MessageKind(r.Kind),
		HostSignalID:         r.HostSignalID,
		WorkerID:             db.StringFromNull(r.WorkerJobID),
		WorkflowRunID:        db.StringFromNull(r.WorkflowRunID),
		CompactionCheckpoint: r.CompactionCheckpoint != 0,
	}
	_ = db.UnmarshalJSON(r.HostSecretRedactionJson, &msg.HostSecretRedaction)
	_ = db.UnmarshalJSON(r.ContentPartsJson, &msg.ContentParts)
	if r.WorkflowBoundaryJson.Valid && strings.TrimSpace(r.WorkflowBoundaryJson.String) != "" {
		var meta api.WorkflowBoundaryMeta
		if err := db.UnmarshalJSON(r.WorkflowBoundaryJson, &meta); err == nil {
			msg.WorkflowBoundary = &meta
		}
	}
	if r.ProgressCompleteJson.Valid && strings.TrimSpace(r.ProgressCompleteJson.String) != "" {
		var meta api.ProgressCompleteMeta
		if err := db.UnmarshalJSON(r.ProgressCompleteJson, &meta); err == nil {
			msg.ProgressComplete = &meta
		}
	}
	if r.ProgressUpdateJson.Valid && strings.TrimSpace(r.ProgressUpdateJson.String) != "" {
		var meta api.ProgressUpdateMeta
		if err := db.UnmarshalJSON(r.ProgressUpdateJson, &meta); err == nil {
			msg.ProgressUpdate = &meta
		}
	}
	if r.WorkflowFeedbackJson.Valid && strings.TrimSpace(r.WorkflowFeedbackJson.String) != "" {
		var meta api.WorkflowFeedbackMeta
		if err := db.UnmarshalJSON(r.WorkflowFeedbackJson, &meta); err == nil {
			msg.WorkflowFeedback = &meta
		}
	}
	if r.WorkflowExplainJson.Valid && strings.TrimSpace(r.WorkflowExplainJson.String) != "" {
		var meta api.WorkflowExplainMeta
		if err := db.UnmarshalJSON(r.WorkflowExplainJson, &meta); err == nil {
			msg.WorkflowExplain = &meta
		}
	}
	if r.IndexWarmingJson.Valid && strings.TrimSpace(r.IndexWarmingJson.String) != "" {
		var meta api.IndexWarmingMeta
		if err := db.UnmarshalJSON(r.IndexWarmingJson, &meta); err == nil {
			msg.IndexWarming = &meta
		}
	}
	if r.BlueprintJson.Valid && strings.TrimSpace(r.BlueprintJson.String) != "" {
		var meta api.BlueprintMeta
		if err := db.UnmarshalJSON(r.BlueprintJson, &meta); err == nil {
			msg.Blueprint = &meta
		}
	}
	if r.CompletionReportJson.Valid && strings.TrimSpace(r.CompletionReportJson.String) != "" {
		var meta api.CompletionReportMeta
		if err := db.UnmarshalJSON(r.CompletionReportJson, &meta); err == nil {
			msg.CompletionReport = &meta
		}
	}
	_ = db.UnmarshalJSON(r.ArtifactIdsJson, &msg.ArtifactIDs)
	_ = db.UnmarshalJSON(r.EvidenceHandlesJson, &msg.EvidenceHandles)
	_ = db.UnmarshalJSON(r.ReasoningJson, &msg.ModelReasoning)
	_ = db.UnmarshalJSON(r.ToolCallsJson, &msg.ToolCalls)
	_ = db.UnmarshalJSON(r.ToolResultJson, &msg.ToolResult)
	_ = db.UnmarshalJSON(r.WorkerSummaryJson, &msg.WorkerSummary)
	_ = db.UnmarshalJSON(r.GroundingJson, &msg.Grounding)
	navigationRefs, navigationErr := sourceref.DecodeMetadata(r.NavigationRefsJson)
	if navigationErr != nil {
		return api.Message{}, navigationErr
	}
	msg.NavigationRefs = navigationRefs.Refs
	if r.NavigationRefsJson.Valid {
		msg.SourceContext = &navigationRefs.Context
	}
	_ = db.UnmarshalJSON(r.CompactedChunkJson, &msg.CompactedChunk)
	var err error
	msg.CreatedAt, err = db.ParseTime(r.Ts)
	return msg, err
}

func insertMessage(ctx context.Context, q *db.Queries, sessionID string, msg api.Message) error {
	msg = api.NormalizeMessageProvenance(msg)
	encoded, err := encodeMessageJSONFields(msg)
	if err != nil {
		return err
	}
	return q.InsertMessage(ctx, db.InsertMessageParams{
		ID:                      msg.ID,
		EntryID:                 msg.ID,
		SessionID:               sessionID,
		Role:                    string(msg.Role),
		Content:                 msg.Content,
		Origin:                  string(msg.Origin),
		Authority:               string(msg.Authority),
		TrustTier:               string(msg.TrustTier),
		AuthorPersonID:          db.NullString(msg.AuthorPersonID),
		ContentPartsJson:        encoded.contentParts,
		HostSecretRedactionJson: encoded.hostSecretRedaction,
		Kind:                    string(msg.Kind),
		HostSignalID:            msg.HostSignalID,
		WorkerJobID:             db.NullString(msg.WorkerID),
		WorkflowRunID:           db.NullString(msg.WorkflowRunID),
		WorkflowBoundaryJson:    encoded.workflowBoundary,
		ProgressCompleteJson:    encoded.progressComplete,
		ProgressUpdateJson:      encoded.progressUpdate,
		WorkflowFeedbackJson:    encoded.workflowFeedback,
		WorkflowExplainJson:     encoded.workflowExplain,
		IndexWarmingJson:        encoded.indexWarming,
		BlueprintJson:           encoded.blueprint,
		CompletionReportJson:    encoded.completionReport,
		ArtifactIdsJson:         encoded.artifactIDs,
		EvidenceHandlesJson:     encoded.evidenceHandles,
		ToolCallsJson:           encoded.toolCalls,
		ToolResultJson:          encoded.toolResult,
		WorkerSummaryJson:       encoded.workerSummary,
		GroundingJson:           encoded.grounding,
		NavigationRefsJson:      encoded.navigationRefs,
		CompactedChunkJson:      encoded.compactedChunk,
		CompactionCheckpoint:    int64(boolToInt(msg.CompactionCheckpoint)),
		Visibility:              messageVisibilityColumn(msg.Visibility),
		DraftVersionCount:       int64(msg.DraftVersionCount),
		DraftStatus:             string(msg.DraftStatus),
		Seq:                     msg.Seq,
		Ord:                     msg.Ord,
		Ts:                      db.FormatTime(msg.CreatedAt),
		ReasoningJson:           encoded.reasoning,
	})
}

type messageJSONFields struct {
	toolCalls           sql.NullString
	hostSecretRedaction sql.NullString
	contentParts        sql.NullString
	toolResult          sql.NullString
	workerSummary       sql.NullString
	grounding           sql.NullString
	navigationRefs      sql.NullString
	compactedChunk      sql.NullString
	workflowBoundary    sql.NullString
	progressComplete    sql.NullString
	progressUpdate      sql.NullString
	workflowFeedback    sql.NullString
	workflowExplain     sql.NullString
	indexWarming        sql.NullString
	completionReport    sql.NullString
	blueprint           sql.NullString
	artifactIDs         sql.NullString
	evidenceHandles     sql.NullString
	reasoning           sql.NullString
}

func encodeMessageJSONFields(msg api.Message) (messageJSONFields, error) {
	var encoded messageJSONFields
	fields := []struct {
		name   string
		target *sql.NullString
		value  any
	}{
		{"tool_calls", &encoded.toolCalls, msg.ToolCalls},
		{"host_secret_redaction", &encoded.hostSecretRedaction, msg.HostSecretRedaction},
		{"content_parts", &encoded.contentParts, msg.ContentParts},
		{"tool_result", &encoded.toolResult, msg.ToolResult},
		{"worker_summary", &encoded.workerSummary, msg.WorkerSummary},
		{"grounding", &encoded.grounding, msg.Grounding},
		{"compacted_chunk", &encoded.compactedChunk, msg.CompactedChunk},
		{"workflow_boundary", &encoded.workflowBoundary, msg.WorkflowBoundary},
		{"progress_complete", &encoded.progressComplete, msg.ProgressComplete},
		{"progress_update", &encoded.progressUpdate, msg.ProgressUpdate},
		{"workflow_feedback", &encoded.workflowFeedback, msg.WorkflowFeedback},
		{"workflow_explain", &encoded.workflowExplain, msg.WorkflowExplain},
		{"index_warming", &encoded.indexWarming, msg.IndexWarming},
		{"completion_report", &encoded.completionReport, msg.CompletionReport},
		{"blueprint", &encoded.blueprint, msg.Blueprint},
		{"artifact_ids", &encoded.artifactIDs, msg.ArtifactIDs},
		{"evidence_handles", &encoded.evidenceHandles, msg.EvidenceHandles},
		{"reasoning", &encoded.reasoning, msg.ModelReasoning},
	}
	for _, field := range fields {
		value, err := db.MarshalJSON(field.value)
		if err != nil {
			return messageJSONFields{}, fmt.Errorf("encode message %s: %w", field.name, err)
		}
		*field.target = value
	}
	refs, err := sourceref.EncodeMetadata(msg.NavigationRefs, msg.SourceContext)
	if err != nil {
		return messageJSONFields{}, fmt.Errorf("encode message navigation_refs: %w", err)
	}
	encoded.navigationRefs = refs
	return encoded, nil
}

func sessionEntryResource(msg api.Message) (kind, id string) {
	id = msg.ID
	if msg.WorkflowBoundary != nil || msg.WorkflowRunID != "" || msg.Kind == api.MessageKindWorkflowBoundary {
		return "workflow_event", id
	}
	if msg.WorkerSummary != nil {
		if jobID := strings.TrimSpace(msg.WorkerSummary.WorkerID); jobID != "" {
			return "worker_job", jobID
		}
	}
	if msg.ToolResult != nil {
		if msg.ToolResult.Dispatch != nil {
			if jobID := strings.TrimSpace(msg.ToolResult.Dispatch.WorkerID); jobID != "" {
				return "worker_job", jobID
			}
		}
		return "tool_receipt", id
	}
	if msg.Role == api.MessageRoleAssistant && msg.Origin == api.MessageOriginModel {
		return "model_output", id
	}
	if msg.Role == api.MessageRoleUser && msg.Origin == api.MessageOriginUser {
		return "utterance", id
	}
	return "host_event", id
}

// nextTranscriptSeq advances the transcript mutation clock.
func nextTranscriptSeq(ctx context.Context, q *db.Queries, sessionID string) (int64, error) {
	if err := q.BumpSessionTranscriptSeq(ctx, sessionID); err != nil {
		return 0, err
	}
	return q.GetSessionTranscriptSeq(ctx, sessionID)
}

// nextTranscriptOrd advances the immutable creation order.
func nextTranscriptOrd(ctx context.Context, q *db.Queries, sessionID string) (int64, error) {
	if err := q.BumpSessionTranscriptOrd(ctx, sessionID); err != nil {
		return 0, err
	}
	return q.GetSessionTranscriptOrd(ctx, sessionID)
}

func messageVisibilityColumn(v api.MessageVisibility) string {
	switch strings.TrimSpace(string(v)) {
	case string(api.MessageVisibilityInternal):
		return string(api.MessageVisibilityInternal)
	default:
		return string(api.MessageVisibilityTranscript)
	}
}
