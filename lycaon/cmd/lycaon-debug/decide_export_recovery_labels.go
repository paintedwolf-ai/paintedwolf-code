package main

import (
	"encoding/json"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// canonicalRecoveryLabels leaves absence unknown and records successful body recovery.
func canonicalRecoveryLabels(msgs []db.ListSessionMessagesRow, start, end int, candidates []retentionCandidate) retentionLabels {
	labels := retentionLabels{Needed: map[string]*bool{}, Later: map[string]*bool{}, By: map[string][]string{}, Provenance: map[string]string{}}
	handles := map[string]string{}
	candidateIDs := map[string]bool{}
	for _, candidate := range candidates {
		labels.Needed[candidate.ID] = nil
		labels.Later[candidate.ID] = nil
		candidateIDs[candidate.ID] = true
		for _, handle := range candidate.Handles {
			handles[handle] = candidate.ID
		}
	}
	calls := map[string]api.ToolCall{}
	for i, m := range msgs {
		for _, call := range toolCallsOf(m) {
			calls[call.ID] = call
		}
		if i < start {
			continue
		}
		if i < end && m.Role == string(api.MessageRoleAssistant) {
			labels.CallsAfter++
		}
		var result api.ToolResult
		if m.Role != string(api.MessageRoleTool) || json.Unmarshal([]byte(m.ToolResultJson.String), &result) != nil || result.Tool != "recall" {
			continue
		}
		var recovered struct {
			Resolution string `json:"resolution"`
			Hits       []struct {
				Handle string   `json:"handle"`
				Body   []string `json:"body"`
			} `json:"hits"`
			History *struct {
				Entries []struct {
					ID      string `json:"id"`
					Content string `json:"content"`
				} `json:"entries"`
			} `json:"history"`
		}
		body := result.Content
		if body == "" {
			body = m.Content
		}
		if json.Unmarshal([]byte(body), &recovered) != nil || recovered.Resolution != "matched" {
			continue
		}
		ids := map[string]bool{}
		for _, hit := range recovered.Hits {
			if id := handles[hit.Handle]; id != "" && len(hit.Body) > 0 {
				ids[id] = true
			}
		}
		if recovered.History != nil {
			call := calls[result.ToolCallID]
			history, _ := call.Args["history"].(map[string]any)
			bodyID, _ := history["message_id"].(string)
			for _, entry := range recovered.History.Entries {
				if entry.ID == bodyID && candidateIDs[entry.ID] && entry.Content != "" {
					ids[entry.ID] = true
				}
			}
		}
		for id := range ids {
			yes := true
			labels.Later[id] = &yes
			if i < end {
				labels.Needed[id] = &yes
			}
			labels.By[id] = []string{"recall"}
			labels.Provenance[id] = "canonical_recovery"
		}
	}
	return labels
}

func toolCallsOf(m db.ListSessionMessagesRow) []api.ToolCall {
	var calls []api.ToolCall
	_ = json.Unmarshal([]byte(m.ToolCallsJson.String), &calls)
	return calls
}
