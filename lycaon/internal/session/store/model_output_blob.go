package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
func readModelOutputBlob(s *sqlCore, projectID, sha string) (content, toolCallsJSON, reasoningJSON string, err error) {
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

func modelOutputFromDB(s *sqlCore, row db.ModelOutputs) (ModelOutput, error) {
	content, toolCallsJSON, reasoningJSON, err := readModelOutputBlob(s, row.ProjectID, row.ContentBlobSha256)
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

func (t *memoryTurns) ProjectLiveModelOutput(ctx context.Context, out LiveModelOutput) error {
	s := t.core
	if err := ctx.Err(); err != nil {
		return err
	}
	if out.ID == "" || out.TurnAttemptID == "" || out.SessionID == "" || out.MessageID == "" || out.Iteration <= 0 {
		return fmt.Errorf("live model output identity required")
	}
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.liveModelOutputs[out.ID]; ok {
		out.CreatedAt = existing.CreatedAt
	} else if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	out.UpdatedAt = now
	s.liveModelOutputs[out.ID] = out
	return nil
}

func (t *memoryTurns) SettleModelOutput(ctx context.Context, out ModelOutput) (ModelOutput, error) {
	return settleModelOutputMemory(ctx, t.core, out)
}

func settleModelOutputMemory(ctx context.Context, s *memoryCore, out ModelOutput) (ModelOutput, error) {
	if err := ctx.Err(); err != nil {
		return ModelOutput{}, err
	}
	if out.ID == "" || out.TurnAttemptID == "" || out.SessionID == "" || out.MessageID == "" || out.Iteration <= 0 {
		return ModelOutput{}, fmt.Errorf("model output identity required")
	}
	now := time.Now().UTC()
	if out.CreatedAt.IsZero() {
		out.CreatedAt = now
	}
	if out.SettledAt.IsZero() {
		out.SettledAt = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.modelOutputs[out.ID]; ok {
		if !sameModelOutput(existing, out) {
			return ModelOutput{}, fmt.Errorf("model output id %s reused with different content or identity", out.ID)
		}
		return existing, nil
	}
	active := false
	for _, turn := range s.turns {
		active = active || (turn.SessionID == out.SessionID && turn.ActiveAttemptID == out.TurnAttemptID && turn.Status == TurnStatusRunning)
	}
	if !active {
		return ModelOutput{}, fmt.Errorf("model output attempt claim lost")
	}
	for _, existing := range s.modelOutputs {
		if existing.TurnAttemptID == out.TurnAttemptID && existing.Iteration == out.Iteration {
			return ModelOutput{}, fmt.Errorf("model iteration already settled")
		}
	}
	s.modelOutputs[out.ID] = out
	delete(s.liveModelOutputs, out.ID)
	return out, nil
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

func (t *memoryTurns) ListPendingModelOutputProjections(ctx context.Context) ([]PendingModelOutputProjection, error) {
	return listPendingModelOutputProjectionsMemory(ctx, t.core)
}

func listPendingModelOutputProjectionsMemory(ctx context.Context, s *memoryCore) ([]PendingModelOutputProjection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PendingModelOutputProjection
	for id, output := range s.modelOutputs {
		if _, ok := s.projectedModelOutputs[id]; !ok {
			workerJobID := ""
			for jobID, turnIDs := range s.workerTurns {
				for _, turnID := range turnIDs {
					for _, attempt := range s.turnAttempts[turnID] {
						if attempt.ID == output.TurnAttemptID {
							workerJobID = jobID
							break
						}
					}
					if workerJobID != "" {
						break
					}
				}
				if workerJobID != "" {
					break
				}
			}
			out = append(out, PendingModelOutputProjection{Output: output, WorkerJobID: workerJobID})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Output.SettledAt.Equal(out[j].Output.SettledAt) {
			return out[i].Output.ID < out[j].Output.ID
		}
		return out[i].Output.SettledAt.Before(out[j].Output.SettledAt)
	})
	if len(out) > 256 {
		out = out[:256]
	}
	return out, nil
}

func (t *memoryTurns) MarkModelOutputProjected(ctx context.Context, outputID string) error {
	s := t.core
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(outputID) == "" {
		return fmt.Errorf("model output projection identity required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	output, ok := s.modelOutputs[outputID]
	if !ok {
		return fmt.Errorf("model output not found: %s", outputID)
	}
	for _, message := range s.messages[output.SessionID] {
		if message.ID == output.MessageID && message.Seq > 0 {
			s.projectedModelOutputs[outputID] = message.Seq
			return nil
		}
	}
	return fmt.Errorf("model output projection not found: %s", outputID)
}
