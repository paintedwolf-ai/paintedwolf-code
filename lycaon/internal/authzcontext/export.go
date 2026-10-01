package authzcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const DefaultListEventsLimit = 500

// SessionReader loads authorization contexts and events for a session.
type SessionReader interface {
	ListContexts(ctx context.Context, sessionID string) ([]Context, error)
	ListEvents(ctx context.Context, sessionID string) ([]Event, error)
}

// ListEvents returns newest-first authz_events for a session, capped at limit.
// limit <= 0 uses DefaultListEventsLimit.
func ListEvents(ctx context.Context, reader SessionReader, sessionID string, limit int) ([]Event, error) {
	if reader == nil {
		return nil, fmt.Errorf("authzcontext: nil reader")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, fmt.Errorf("authzcontext: empty session_id")
	}
	if limit <= 0 {
		limit = DefaultListEventsLimit
	}
	all, err := reader.ListEvents(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if len(all) <= limit {
		return newestFirst(all), nil
	}
	tail := all[len(all)-limit:]
	return newestFirst(tail), nil
}

func newestFirst(events []Event) []Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]Event, len(events))
	for i := range events {
		out[i] = events[len(events)-1-i]
	}
	return out
}

type exportHead struct {
	Kind            string `json:"kind"`
	SessionID       string `json:"session_id"`
	ContextHeadHash string `json:"context_head_hash,omitempty"`
	EventHeadHash   string `json:"event_head_hash,omitempty"`
	ContextSeq      int    `json:"context_seq,omitempty"`
	EventSeq        int    `json:"event_seq,omitempty"`
}

type exportContext struct {
	Kind            string `json:"kind"`
	SessionID       string `json:"session_id"`
	ContextSeq      int    `json:"context_seq"`
	RowHash         string `json:"row_hash"`
	HashVersion     int    `json:"hash_version"`
	SealedAt        string `json:"ts"`
	WorkerJobID     string `json:"worker_job_id,omitempty"`
	ProjectID       string `json:"project_id,omitempty"`
	AgentType       string `json:"agent_type,omitempty"`
	ToolProfile     string `json:"tool_profile,omitempty"`
	Posture         string `json:"posture,omitempty"`
	ApprovalPosture string `json:"approval_posture,omitempty"`
	ConfigHash      string `json:"config_hash"`
	MaxToolLoops    int    `json:"max_tool_loops,omitempty"`
}

type exportEvent struct {
	Kind             string `json:"kind"`
	SessionID        string `json:"session_id"`
	EventSeq         int    `json:"event_seq"`
	RowHash          string `json:"row_hash"`
	HashVersion      int    `json:"hash_version"`
	RecordedAt       string `json:"ts"`
	ContextSeq       int    `json:"context_seq,omitempty"`
	Action           string `json:"action"`
	Outcome          string `json:"outcome"`
	ResolvedBy       string `json:"resolved_by"`
	ResolverPersonID string `json:"resolver_person_id,omitempty"`
	ToolName         string `json:"tool_name,omitempty"`
	RejectCode       string `json:"reject_code,omitempty"`
	DetailJSON       string `json:"detail_json"`
	ConfigHash       string `json:"config_hash,omitempty"`
}

// ExportSessionAuthz writes JSONL: contexts (asc), events (asc), then a head record
// with per-session chain tips for off-box anchoring.
func ExportSessionAuthz(ctx context.Context, w io.Writer, reader SessionReader, sessionID string) error {
	if w == nil {
		return fmt.Errorf("authzcontext export: nil writer")
	}
	if reader == nil {
		return fmt.Errorf("authzcontext export: nil reader")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("authzcontext export: empty session_id")
	}
	contexts, err := reader.ListContexts(ctx, sessionID)
	if err != nil {
		return err
	}
	events, err := reader.ListEvents(ctx, sessionID)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	for _, c := range contexts {
		row := exportContext{
			Kind:            "context",
			SessionID:       c.SessionID,
			ContextSeq:      c.ContextSeq,
			RowHash:         c.RowHash,
			HashVersion:     c.HashVersion,
			SealedAt:        chainTimestamp(c),
			WorkerJobID:     c.WorkerJobID,
			ProjectID:       c.ProjectID,
			AgentType:       c.AgentType,
			ToolProfile:     c.ToolProfile,
			Posture:         c.Posture,
			ApprovalPosture: string(c.ApprovalPosture),
			ConfigHash:      c.ConfigHash,
			MaxToolLoops:    c.MaxToolLoops,
		}
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	for _, e := range events {
		row := exportEvent{
			Kind:             "event",
			SessionID:        e.SessionID,
			EventSeq:         e.EventSeq,
			RowHash:          e.RowHash,
			HashVersion:      e.HashVersion,
			RecordedAt:       eventChainTimestamp(e),
			ContextSeq:       e.ContextSeq,
			Action:           string(e.Action),
			Outcome:          string(e.Outcome),
			ResolvedBy:       string(e.ResolvedBy),
			ResolverPersonID: e.ResolverPersonID,
			ToolName:         e.ToolName,
			RejectCode:       e.RejectCode,
			DetailJSON:       e.DetailJSON,
			ConfigHash:       e.ConfigHash,
		}
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	head := exportHead{Kind: "head", SessionID: sessionID}
	if n := len(contexts); n > 0 {
		last := contexts[n-1]
		head.ContextHeadHash = last.RowHash
		head.ContextSeq = last.ContextSeq
	}
	if n := len(events); n > 0 {
		last := events[n-1]
		head.EventHeadHash = last.RowHash
		head.EventSeq = last.EventSeq
	}
	return enc.Encode(head)
}
