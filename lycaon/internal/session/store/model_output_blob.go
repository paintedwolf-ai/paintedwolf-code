package store

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/timelayout"
	"time"

	"github.com/lycaon/lycaon/internal/contentblob"
	"github.com/lycaon/lycaon/internal/db"
)

// modelOutputBody is the content-addressed envelope for one settled model
// output's content/tool_calls/reasoning triple, referenced by
// model_outputs.content_blob_sha256.
type modelOutputBody struct {
	Content       string          `json:"content"`
	ToolCallsJSON json.RawMessage `json:"tool_calls_json,omitempty"`
	ReasoningJSON json.RawMessage `json:"reasoning_json,omitempty"`
}

// writeModelOutputBlob publishes content references with the model-output row.
// An empty body leaves the content hash unset.
func writeModelOutputBlob(ctx context.Context, s *sqlCore, q *db.Queries, projectID, content, toolCallsJSON, reasoningJSON string) (string, error) {
	if content == "" && toolCallsJSON == "" && reasoningJSON == "" {
		return "", nil
	}
	body := modelOutputBody{Content: content}
	if toolCallsJSON != "" {
		body.ToolCallsJSON = json.RawMessage(toolCallsJSON)
	}
	if reasoningJSON != "" {
		body.ReasoningJSON = json.RawMessage(reasoningJSON)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("marshal model output body: %w", err)
	}
	blobStore := contentblob.StoreFor(s.dataDir, projectID)
	sha, byteSize, storedSize, err := contentblob.Write(ctx, blobStore, raw)
	if err != nil {
		return "", fmt.Errorf("write model output blob: %w", err)
	}
	if err := q.UpsertContentBlobObject(ctx, db.UpsertContentBlobObjectParams{
		ProjectID: projectID, Sha256: sha, ByteSize: byteSize, StoredSize: storedSize,
		CreatedAt: timelayout.Format(time.Now().UTC()),
	}); err != nil {
		return "", fmt.Errorf("upsert content blob object: %w", err)
	}
	return sha, nil
}

// readModelOutputBlob reverses writeModelOutputBlob. An empty sha means the
// output carried no body — all three fields are zero values.
func readModelOutputBlob(dataDir, projectID, sha string) (content, toolCallsJSON, reasoningJSON string, err error) {
	if sha == "" {
		return "", "", "", nil
	}
	blobStore := contentblob.StoreFor(dataDir, projectID)
	raw, err := contentblob.Read(blobStore, sha)
	if err != nil {
		return "", "", "", fmt.Errorf("read model output blob: %w", err)
	}
	var body modelOutputBody
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", "", "", fmt.Errorf("decode model output blob: %w", err)
	}
	return body.Content, string(body.ToolCallsJSON), string(body.ReasoningJSON), nil
}

func modelOutputFromDB(dataDir string, row db.ModelOutputs) (ModelOutput, error) {
	content, toolCallsJSON, reasoningJSON, err := readModelOutputBlob(dataDir, row.ProjectID, row.ContentBlobSha256)
	if err != nil {
		return ModelOutput{}, err
	}
	out := ModelOutput{
		ID: row.ID, TurnAttemptID: row.TurnAttemptID, SessionID: row.SessionID,
		Iteration: int(row.Iteration), MessageID: row.MessageID, ProviderID: row.ProviderID,
		Model: row.Model, Content: content, ToolCallsJSON: toolCallsJSON,
		ReasoningJSON: reasoningJSON, FinishReason: row.FinishReason, Scripted: row.Scripted != 0,
	}
	out.CreatedAt, err = db.ParseTime(row.CreatedAt)
	if err != nil {
		return ModelOutput{}, err
	}
	out.SettledAt, err = db.ParseTime(row.SettledAt)
	return out, err
}

// ReadModelOutputs returns a session's settled outputs with their bodies, read
// from a store whose data directory is dataDir.
func ReadModelOutputs(ctx context.Context, q *db.Queries, dataDir, sessionID string) ([]ModelOutput, error) {
	rows, err := q.ListSessionModelOutputs(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]ModelOutput, 0, len(rows))
	for _, row := range rows {
		output, err := modelOutputFromDB(dataDir, row)
		if err != nil {
			return nil, fmt.Errorf("model output %s: %w", row.ID, err)
		}
		out = append(out, output)
	}
	return out, nil
}

// sameSettledModelOutputRow compares by content digest, which covers text, tool
// calls, and reasoning without reading blobs.
func sameSettledModelOutputRow(committed db.ModelOutputs, proposed ModelOutput, proposedSha string) bool {
	return committed.ID == proposed.ID &&
		committed.TurnAttemptID == proposed.TurnAttemptID &&
		committed.SessionID == proposed.SessionID &&
		committed.Iteration == int64(proposed.Iteration) &&
		committed.MessageID == proposed.MessageID &&
		committed.ProviderID == proposed.ProviderID &&
		committed.Model == proposed.Model &&
		committed.ContentBlobSha256 == proposedSha &&
		committed.FinishReason == proposed.FinishReason && (committed.Scripted != 0) == proposed.Scripted
}

// sameModelOutput compares the in-memory output values directly.
func sameModelOutput(committed, proposed ModelOutput) bool {
	return committed.ID == proposed.ID &&
		committed.TurnAttemptID == proposed.TurnAttemptID &&
		committed.SessionID == proposed.SessionID &&
		committed.Iteration == proposed.Iteration &&
		committed.MessageID == proposed.MessageID &&
		committed.ProviderID == proposed.ProviderID &&
		committed.Model == proposed.Model &&
		committed.Content == proposed.Content &&
		committed.ToolCallsJSON == proposed.ToolCallsJSON &&
		committed.ReasoningJSON == proposed.ReasoningJSON &&
		committed.FinishReason == proposed.FinishReason && committed.Scripted == proposed.Scripted
}
