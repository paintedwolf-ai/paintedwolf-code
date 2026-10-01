package store

import (
	"context"
	"encoding/json"
	"fmt"
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
func (s *SQL) writeModelOutputBlob(ctx context.Context, q *db.Queries, projectID, content, toolCallsJSON, reasoningJSON string) (string, error) {
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
	sha, byteSize, storedSize, err := contentblob.Write(blobStore, raw)
	if err != nil {
		return "", fmt.Errorf("write model output blob: %w", err)
	}
	if err := q.UpsertContentBlobObject(ctx, db.UpsertContentBlobObjectParams{
		ProjectID: projectID, Sha256: sha, ByteSize: byteSize, StoredSize: storedSize,
		CreatedAt: db.FormatTime(time.Now().UTC()),
	}); err != nil {
		return "", fmt.Errorf("upsert content blob object: %w", err)
	}
	return sha, nil
}

// readModelOutputBlob reverses writeModelOutputBlob. An empty sha means the
// output carried no body — all three fields are zero values.
func (s *SQL) readModelOutputBlob(projectID, sha string) (content, toolCallsJSON, reasoningJSON string, err error) {
	if sha == "" {
		return "", "", "", nil
	}
	blobStore := contentblob.StoreFor(s.dataDir, projectID)
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
