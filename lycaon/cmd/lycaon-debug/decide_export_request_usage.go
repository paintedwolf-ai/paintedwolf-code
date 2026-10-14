package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// exportRequestUsage reports actual conversation usage separately from source estimates.
func (e *rowExporter) exportRequestUsage(ctx context.Context, r db.ListTurnReceiptContextsRow, root string, msgs []db.ListSessionMessagesRow, start, end int) error {
	var state turnload.State
	if err := json.Unmarshal([]byte(r.StateJson), &state); err != nil {
		return err
	}
	row := map[string]any{"schema": "pw-request-row/1", "receipt": r.ID, "session": r.SessionID, "root_session": root, "opening_message_id": r.OpeningMessageID.String,
		"host": state.Host, "surface": r.SurfaceID, "project": r.ProjectName, "model": r.Model, "partial": r.TurnStatus != "complete",
		"calls": 0, "tool_calls": 0, "recall_calls": 0, "repeated_calls": 0, "request_index": 0,
		"retention": map[string]any{"recorded": false, "asked": false, "dropped": 0}}
	index, calls, tools, recalls, repeats := 0, 0, 0, 0, 0
	seen := map[string]bool{}
	for i, m := range msgs[:end] {
		if i < start && visibleUserRow(m) {
			index++
		}
		if i >= start && m.Role == string(api.MessageRoleAssistant) {
			calls++
		}
		for _, call := range toolCallsOf(m) {
			raw, err := json.Marshal(struct {
				Name string
				Args map[string]any
			}{call.Name, call.Args})
			if err != nil {
				return err
			}
			key := string(raw)
			if i >= start {
				tools++
				if call.Name == "recall" {
					recalls++
				}
				if seen[key] {
					repeats++
				}
			}
			seen[key] = true
		}
	}
	row["calls"], row["tool_calls"], row["recall_calls"], row["repeated_calls"], row["request_index"] = calls, tools, recalls, repeats, index
	providerCalls, err := e.queries.ListSessionProviderCalls(ctx, r.SessionID)
	if err != nil {
		return err
	}
	from, err := time.Parse(time.RFC3339Nano, msgs[start].Ts)
	if err != nil {
		return err
	}
	var until time.Time
	if end < len(msgs) {
		until, err = time.Parse(time.RFC3339Nano, msgs[end].Ts)
		if err != nil {
			return err
		}
	}
	var reported int
	var prompt, completion, cache int64
	for _, call := range providerCalls {
		at, err := time.Parse(time.RFC3339Nano, call.StartedAt)
		if err != nil {
			return err
		}
		if at.Before(from) || (!until.IsZero() && !at.Before(until)) {
			continue
		}
		reported++
		prompt += call.PromptTokens
		completion += call.CompletionTokens
		cache += call.CacheReadTokens
	}
	row["provider_calls"], row["prompt_tokens"], row["completion_tokens"], row["cache_read_tokens"] = reported, prompt, completion, cache
	row["usage_available"] = reported > 0
	return e.requests.Encode(row)
}
