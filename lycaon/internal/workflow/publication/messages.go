package publication

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/workflow/runstate"

	"github.com/lycaon/lycaon/pkg/api"
)

type TranscriptStore interface {
	Get(context.Context, string) (*api.Session, error)
	AppendMessages(context.Context, string, ...api.Message) error
	MutationEventsOutboxed() bool
}

// Messages admits workflow-stamped transcript rows and publishes committed changes.
type Messages struct {
	Sessions TranscriptStore
	Events   *events.Publisher
	Runs     runstate.RunsRepository
}

func (m *Messages) Append(ctx context.Context, sessionID string, msgs ...api.Message) error {
	if m == nil || m.Sessions == nil {
		return fmt.Errorf("session store not configured")
	}
	if err := m.Sessions.AppendMessages(ctx, sessionID, msgs...); err != nil {
		return err
	}
	m.PublishAppends(ctx, sessionID, msgs...)
	return nil
}

func (m *Messages) PublishAppends(ctx context.Context, sessionID string, msgs ...api.Message) {
	if m == nil || m.Events == nil || len(msgs) == 0 {
		return
	}
	if m.Sessions != nil && m.Sessions.MutationEventsOutboxed() {
		return
	}
	key := ""
	if sess, err := m.Sessions.Get(ctx, sessionID); err == nil && sess != nil {
		key = sess.ProjectID
	}
	if key == "" {
		return
	}
	for _, msg := range msgs {
		if msg.ID == "" {
			continue
		}
		m.Events.PublishMessageAppend(ctx, key, sessionID, msg)
	}
}

// PublishPatch emits an in-place transcript update.
func (m *Messages) PublishPatch(ctx context.Context, sessionID string, msg api.Message) {
	if m == nil || m.Events == nil || msg.ID == "" {
		return
	}
	if m.Sessions != nil && m.Sessions.MutationEventsOutboxed() {
		return
	}
	key := ""
	if sess, err := m.Sessions.Get(ctx, sessionID); err == nil && sess != nil {
		key = sess.ProjectID
	}
	if key == "" {
		return
	}
	m.Events.PublishMessagePatch(ctx, key, sessionID, msg)
}

// StampAndAppendMessages sets workflow_run_id on messages while a run is active.
func (m *Messages) StampAndAppendMessages(ctx context.Context, sessionID string, msgs ...api.Message) error {
	if m == nil || m.Sessions == nil {
		return fmt.Errorf("session store not configured")
	}
	active, err := m.Runs.ActiveBySession(ctx, sessionID)
	if err != nil {
		return err
	}
	for i := range msgs {
		if msgs[i].WorkflowBoundary != nil || msgs[i].Kind == api.MessageKindWorkflowBoundary {
			continue
		}
		if active != nil && strings.TrimSpace(msgs[i].WorkflowRunID) == "" {
			msgs[i].WorkflowRunID = active.ID
		}
	}
	return m.Sessions.AppendMessages(ctx, sessionID, msgs...)
}
