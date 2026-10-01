package store

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestRejectDuplicateAppendIDs(t *testing.T) {
	t.Parallel()
	if err := rejectDuplicateAppendIDs([]api.Message{
		{ID: "a"}, {ID: "b"}, {ID: ""},
	}); err != nil {
		t.Fatalf("distinct ids: %v", err)
	}
	err := rejectDuplicateAppendIDs([]api.Message{
		{ID: "same"}, {ID: "same"},
	})
	if !errors.Is(err, ErrDuplicateMessageID) {
		t.Fatalf("duplicate ids: %v want ErrDuplicateMessageID", err)
	}
}

func TestRejectExistingMessageIDs(t *testing.T) {
	t.Parallel()
	existing := []api.Message{{ID: "live"}}
	if err := rejectExistingMessageIDs(existing, []api.Message{{ID: "new"}}); err != nil {
		t.Fatalf("new id: %v", err)
	}
	err := rejectExistingMessageIDs(existing, []api.Message{{ID: "live"}})
	if !errors.Is(err, ErrDuplicateMessageID) {
		t.Fatalf("existing id: %v want ErrDuplicateMessageID", err)
	}
}
