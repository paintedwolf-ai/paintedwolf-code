package closeoutassembly

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/runeclamp"
	sessiontree "github.com/lycaon/lycaon/internal/session/tree"
	"github.com/lycaon/lycaon/internal/session/verification"
	"github.com/lycaon/lycaon/pkg/api"
)

type executionObservations struct {
	ModelChecklist string                                `json:"model_authored_checklist,omitempty"`
	SourceChecks   *verification.SourceCheckObservations `json:"source_checks"`
}

type SourceVerification interface {
	SourceObservations(context.Context, *api.Session, []api.Message) *verification.SourceCheckObservations
}

func (m *Service) SetSourceVerification(source SourceVerification) { m.sourceVerification = source }

func (m *Service) executionObservations(ctx context.Context, sessionID string) string {
	if m.sessionStore == nil || m.transcriptStore == nil || m.evidenceStore == nil {
		return ""
	}
	messages, err := m.transcriptStore.GetMessages(ctx, sessionID)
	if err != nil {
		return ""
	}
	var checks *verification.SourceCheckObservations
	if m.sourceVerification != nil {
		sess, err := m.sessionStore.Get(ctx, sessionID)
		if err == nil && sess != nil {
			checks = m.sourceVerification.SourceObservations(ctx, sess, messages)
		}
	}
	var snapshot *progress.Snapshot
	if m.progress != nil {
		root := sessiontree.RootID(ctx, m.sessionStore, sessionID)
		if loaded, err := m.progress.Snapshot(ctx, root); err == nil {
			snapshot = &loaded
		}
	}
	return executionObservationJSON(messages, checks, snapshot)
}

// Checklist rows remain model-authored and cannot establish source freshness.
func executionObservationJSON(messages []api.Message, checks *verification.SourceCheckObservations, snapshot *progress.Snapshot) string {
	if checks == nil {
		return ""
	}
	out := executionObservations{SourceChecks: checks}
	updatedInTurn := false
	var fallbackRows []progress.Row
	for _, message := range messages[api.UserIntentBoundary(messages):] {
		result := message.ToolResult
		if result == nil || result.Tool != "update_progress" || string(result.Outcome) != "completed" {
			continue
		}
		updatedInTurn = true
		if rows := extractToolResultRows(message.Content); len(rows) > 0 {
			fallbackRows = rows
		}
	}
	if updatedInTurn {
		var rendered string
		if snapshot != nil && !snapshot.Missing() {
			rendered = snapshot.RenderMarkdown()
		} else if len(fallbackRows) > 0 {
			rendered = progress.RenderMarkdown(fallbackRows)
		}
		out.ModelChecklist = runeclamp.ClampBytes(strings.TrimSpace(rendered), 800)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(raw)
}

func extractToolResultRows(output string) []progress.Row {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil
	}
	var payload struct {
		Snapshot struct {
			Rows []progress.Row `json:"rows"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return nil
	}
	return payload.Snapshot.Rows
}
