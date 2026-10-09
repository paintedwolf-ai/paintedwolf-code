package transcript

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Service) Append(ctx context.Context, sessionID string, msgs ...api.Message) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("session store not configured")
	}
	if len(msgs) == 0 {
		return nil
	}
	stamped, err := m.Prepare(ctx, sessionID, msgs)
	if err != nil {
		return err
	}
	if m.workflows != nil {
		err = m.workflows.StampAndAppendMessages(ctx, sessionID, stamped...)
	} else {
		err = m.store.AppendMessages(ctx, sessionID, stamped...)
	}
	if err != nil {
		return err
	}
	m.PublishAppends(ctx, sessionID, stamped...)
	return nil
}

func (m *Service) Update(ctx context.Context, sessionID, messageID string, msg api.Message) error {
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
		m.NavigationRefs(ctx, sessionID, rows)
		merged = rows[0]
	}
	merged = m.Screen(m.SecretContext(ctx, sessionID), merged)
	m.Streams.Flush(ctx, sessionID)
	if m.events != nil {
		m.events.FlushMessagePatches(ctx, sessionID)
	}
	stored, err := m.store.UpdateMessage(ctx, sessionID, messageID, merged)
	if err != nil {
		return err
	}
	m.PublishPatch(ctx, sessionID, stored)
	return nil
}

// SupersedeWorkerReport retains rejected reports for search and collapsed history.
func (m *Service) SupersedeWorkerReport(ctx context.Context, sessionID, messageID string) error {
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
	return m.Update(ctx, sessionID, messageID, existing)
}
