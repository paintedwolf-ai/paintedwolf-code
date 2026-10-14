package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

const retentionRowSchema = "pw-retention-row/1"
const retentionCandidateLimit = 16

// retentionCandidate describes an observation without declaring it expendable.
type retentionCandidate struct {
	ID      string   `json:"id"`
	Ref     string   `json:"ref"`
	Tool    string   `json:"tool"`
	Ord     int64    `json:"ord"`
	Seq     int64    `json:"seq"`
	Tokens  int      `json:"tokens"`
	Age     int      `json:"age"`
	Handles []string `json:"handles"`
	Option  string   `json:"option"`
}

type retentionLabels struct {
	Needed     map[string]*bool    `json:"needed"`
	Later      map[string]*bool    `json:"later"`
	By         map[string][]string `json:"by"`
	Provenance map[string]string   `json:"provenance"`
	CallsAfter int                 `json:"calls_after"`
}

type retentionRow struct {
	Schema           string               `json:"schema"`
	Receipt          int64                `json:"receipt"`
	Session          string               `json:"session"`
	RootSession      string               `json:"root_session"`
	OpeningMessageID string               `json:"opening_message_id"`
	Host             string               `json:"host"`
	Surface          string               `json:"surface"`
	CatalogRevision  string               `json:"catalog_revision"`
	Project          string               `json:"project"`
	Model            string               `json:"model"`
	Partial          bool                 `json:"partial"`
	Lang             string               `json:"lang"`
	Watermark        bool                 `json:"watermark"`
	Tokens           int                  `json:"tokens"`
	Reconstructed    bool                 `json:"reconstructed"`
	Engine           map[string]any       `json:"engine"`
	State            json.RawMessage      `json:"state"`
	Candidates       []retentionCandidate `json:"candidates"`
	Labels           retentionLabels      `json:"labels"`
}

func (e *rowExporter) exportCompactionObservations(ctx context.Context, r db.ListTurnReceiptContextsRow, root string, msgs []db.ListSessionMessagesRow) error {
	start := openingIndex(msgs, r.OpeningMessageID.String)
	if start < 0 {
		return nil
	}
	end := len(msgs)
	for i := start + 1; i < len(msgs); i++ {
		if visibleUserRow(msgs[i]) {
			end = i
			break
		}
	}
	if e.retention != nil {
		candidates, tokens := compactionCandidates(msgs[:start])
		var state turnload.State
		if err := json.Unmarshal([]byte(r.StateJson), &state); err != nil {
			return err
		}
		row := retentionRow{Schema: retentionRowSchema, Receipt: r.ID, Session: r.SessionID, RootSession: root, OpeningMessageID: r.OpeningMessageID.String,
			Host: state.Host, Surface: r.SurfaceID, CatalogRevision: r.CatalogRevision, Project: r.ProjectName, Model: r.Model, Partial: r.TurnStatus != "complete", Lang: guessLang(state.User),
			Tokens: tokens, Reconstructed: true, Engine: map[string]any{"state": "abstained", "reason": "observations_only", "dropped": []string{}}, State: json.RawMessage(r.StateJson), Candidates: candidates,
			Labels: canonicalRecoveryLabels(msgs, start, end, candidates)}
		if err := e.retention.Encode(row); err != nil {
			return err
		}
	}
	if e.requests != nil {
		return e.exportRequestUsage(ctx, r, root, msgs, start, end)
	}
	return nil
}

func openingIndex(msgs []db.ListSessionMessagesRow, id string) int {
	for i, m := range msgs {
		if m.ID == id {
			return i
		}
	}
	return -1
}

func compactionCandidates(msgs []db.ListSessionMessagesRow) ([]retentionCandidate, int) {
	candidates := []retentionCandidate{}
	tokens := 0
	for i, m := range msgs {
		tokens += tokenest.EstimateDefault(m.Content)
		if m.Role != string(api.MessageRoleTool) {
			continue
		}
		var result api.ToolResult
		if err := json.Unmarshal([]byte(m.ToolResultJson.String), &result); err != nil {
			continue
		}
		var handles []string
		_ = json.Unmarshal([]byte(m.EvidenceHandlesJson.String), &handles)
		content := m.Content
		if content == "" {
			content = result.Content
		}
		candidates = append(candidates, retentionCandidate{ID: m.ID, Tool: result.Tool, Ord: m.Ord, Seq: m.Seq, Tokens: tokenest.EstimateDefault(content), Age: len(msgs) - i - 1, Handles: orEmpty(handles), Option: result.Tool + " observation: " + boundedObservation(content)})
	}
	candidates = candidates[max(0, len(candidates)-retentionCandidateLimit):]
	for i := range candidates {
		candidates[i].Ref = fmt.Sprintf("r%d", i)
	}
	return candidates, tokens
}

func boundedObservation(content string) string {
	runes := []rune(strings.TrimSpace(content))
	return string(runes[:min(256, len(runes))])
}
