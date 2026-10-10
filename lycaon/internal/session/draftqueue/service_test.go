package draftqueue

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/queue"
	"github.com/lycaon/lycaon/internal/session/promptinput"
	"github.com/lycaon/lycaon/internal/session/store"
	"testing"
	"time"
)

type editedReceipt struct {
	Store
	row      *store.PromptSubmission
	err      error
	editable bool
}

func (s editedReceipt) GetPromptSubmission(context.Context, string) (*store.PromptSubmission, error) {
	return s.row, s.err
}
func (s editedReceipt) UpdateQueuedPromptSubmissionInputs(context.Context, []store.PromptSubmissionInputUpdate) (bool, error) {
	return s.editable, nil
}

func TestInlineEditRefusalLeavesDraftAndRevisionUnchanged(t *testing.T) {
	raw, err := json.Marshal(promptinput.Input{Text: "original"})
	if err != nil {
		t.Fatalf("encode input: %v", err)
	}
	for _, tc := range []struct {
		name, input string
		err         error
		editable    bool
	}{{"corrupt receipt", "{", nil, true}, {"receipt read canceled", string(raw), context.Canceled, true}, {"receipt no longer queued", string(raw), nil, false}} {
		t.Run(tc.name, func(t *testing.T) {
			q := queue.New()
			draft := q.AppendOrdered("session", "submission", "person", "original", 0, time.Time{})
			service := New(editedReceipt{row: &store.PromptSubmission{InputJSON: tc.input}, err: tc.err, editable: tc.editable}, q, func(context.Context, string) { t.Fatal("refused edit published state") })
			_, err := service.UpdateText(t.Context(), "session", draft.Revision, "submission", "replacement")
			if err == nil {
				t.Fatal("invalid receipt admitted an inline edit")
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatalf("receipt failure lost: %v", err)
			}
			got := service.Snapshot("session")
			if got.Revision != draft.Revision || len(got.QueueItems) != 1 || got.QueueItems[0].Text != "original" {
				t.Fatalf("refused edit changed draft: %+v", got)
			}
		})
	}
}
