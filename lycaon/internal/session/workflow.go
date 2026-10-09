package session

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
)

// workflowfacts.ActiveWorkflowManifest holds runtime fields from the active workflow manifest.

func (m *Manager) assertWorkflowRunnable(ctx context.Context, sessionID string) error {
	if m == nil || m.workflows == nil {
		return nil
	}
	return m.workflows.Policy.AssertSessionRunnable(ctx, sessionID)
}

func (m *Manager) hasActiveWorkflowRun(ctx context.Context, sessionID string) bool {
	if m == nil || m.workflows == nil {
		return false
	}
	run, err := m.workflows.Runs.ActiveBySession(ctx, sessionID)
	return err == nil && run != nil
}

func (m *Manager) appendMessages(ctx context.Context, sessionID string, msgs ...api.Message) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	if len(msgs) == 0 {
		return nil
	}
	stamped, err := m.prepareAppendForStore(ctx, sessionID, msgs)
	if err != nil {
		return err
	}
	if m.workflows != nil {
		err = m.workflows.Transcript.StampAndAppendMessages(ctx, sessionID, stamped...)
	} else {
		err = m.store.AppendMessages(ctx, sessionID, stamped...)
	}
	if err != nil {
		return err
	}
	m.publishMessageAppends(ctx, sessionID, stamped...)
	return nil
}

func (m *Manager) updateMessage(ctx context.Context, sessionID, messageID string, msg api.Message) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	if msg.ID == "" {
		msg.ID = messageID
	}
	existing, err := m.store.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	merged := api.MergeMessagePatch(existing, msg)
	if existing.Content != merged.Content || (existing.Visibility != api.MessageVisibilityTranscript && merged.Visibility == api.MessageVisibilityTranscript) {
		if msg.NavigationRefs == nil {
			merged.NavigationRefs = nil
		}
		rows := []api.Message{merged}
		m.attachMessageNavigationRefs(ctx, sessionID, rows)
		merged = rows[0]
	}
	merged = m.screenForStore(m.messageSecretContext(ctx, sessionID), merged)
	m.Streams().Flush(ctx, sessionID)
	if m.events != nil {
		m.events.FlushMessagePatches(ctx, sessionID)
	}
	stored, err := m.store.UpdateMessage(ctx, sessionID, messageID, merged)
	if err != nil {
		return err
	}
	m.publishMessagePatch(ctx, sessionID, stored)
	return nil
}

// SupersedeWorkerReport retains rejected reports for search and collapsed history.
func (m *Manager) SupersedeWorkerReport(ctx context.Context, sessionID, messageID string) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return fmt.Errorf("message id required")
	}
	existing, err := m.store.GetMessage(ctx, sessionID, messageID)
	if err != nil {
		return err
	}
	existing.Kind = api.MessageKindSuperseded
	return m.updateMessage(ctx, sessionID, messageID, existing)
}
