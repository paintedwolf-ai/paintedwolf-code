package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

func duplicateMessageID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrDuplicateMessageID
	}
	return fmt.Errorf("%w: %s", ErrDuplicateMessageID, id)
}

func rejectDuplicateAppendIDs(msgs []api.Message) error {
	seen := make(map[string]struct{}, len(msgs))
	for _, msg := range msgs {
		id := strings.TrimSpace(msg.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			return duplicateMessageID(id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func rejectExistingMessageIDs(existing []api.Message, msgs []api.Message) error {
	if len(existing) == 0 {
		return nil
	}
	have := make(map[string]struct{}, len(existing))
	for _, msg := range existing {
		id := strings.TrimSpace(msg.ID)
		if id == "" {
			continue
		}
		have[id] = struct{}{}
	}
	for _, msg := range msgs {
		id := strings.TrimSpace(msg.ID)
		if id == "" {
			continue
		}
		if _, ok := have[id]; ok {
			return duplicateMessageID(id)
		}
	}
	return nil
}

// appendsActivity reports whether an append shows in the chat's transcript,
// which is what advances activity_at. Hidden host rows and span bookkeeping,
// including a phase's explain note, change only the record.
func appendsActivity(msgs []api.Message) bool {
	for _, msg := range msgs {
		if !api.IsInternalTranscriptMessage(msg) && !api.IsWorkflowBoundaryMessage(msg) && !api.IsWorkflowExplainMessage(msg) {
			return true
		}
	}
	return false
}

// ErrWorkflowRunStampRequired is returned when a progress row lacks workflow_run_id.
var ErrWorkflowRunStampRequired = errors.New("workflow_run_id required for progress transcript rows")

func requireWorkflowRunStamp(msg api.Message) error {
	if !api.IsProgressUpdateMessage(msg) && !api.IsProgressCompleteMessage(msg) {
		return nil
	}
	if strings.TrimSpace(msg.WorkflowRunID) != "" {
		return nil
	}
	kind := strings.TrimSpace(string(msg.Kind))
	if kind == "" {
		kind = "progress"
	}
	return fmt.Errorf("%w (%s)", ErrWorkflowRunStampRequired, kind)
}
