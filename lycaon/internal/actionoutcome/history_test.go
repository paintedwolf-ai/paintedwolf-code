package actionoutcome

import (
	"errors"
	"testing"
)

func TestHistoryRetainsPendingAndRejectsTerminalChanges(t *testing.T) {
	h := NewHistory[int](1)
	for _, id := range []string{"pending", "first", "last"} {
		if !h.Open(id, 0) || h.Open(id, 1) {
			t.Fatal("duplicate registration", id)
		}
	}
	for _, id := range []string{"first", "last"} {
		if !h.Change(id, func(int) (int, bool) { return 2, true }) ||
			h.Change(id, func(int) (int, bool) { return 3, true }) {
			t.Fatal("terminal transition", id)
		}
	}
	if _, err := h.Await(t.Context(), "first"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("evicted record returned %v", err)
	}
	if got, err := h.Await(t.Context(), "last"); got != 2 || err != nil {
		t.Fatalf("retained outcome = %d, %v", got, err)
	}
	if len(h.Snapshot()) != 2 {
		t.Fatal("lost pending record")
	}
}
