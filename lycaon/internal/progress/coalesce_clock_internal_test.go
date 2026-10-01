package progress

import (
	"testing"
	"time"
)

// ChangedAt is the write instant that produced Latest, not the later flush instant.
func TestCoalescerAnchorsChangedAtToWriteInstant(t *testing.T) {
	writeAt := time.Date(2026, 7, 7, 18, 4, 18, 0, time.UTC)
	var got FlushPayload
	c := NewCoalescer(time.Hour, func(p FlushPayload) { got = p })
	c.now = func() time.Time { return writeAt }

	c.Record("s1", "- [ ] a\n", "- [x] a\n")

	// Flush happens later than the write — the payload must still carry the write instant.
	c.now = func() time.Time { return writeAt.Add(2 * time.Second) }
	c.FlushNow("s1")

	if !got.ChangedAt.Equal(writeAt) {
		t.Fatalf("ChangedAt = %s, want the write instant %s", got.ChangedAt, writeAt)
	}
}

// A burst of writes anchors to the last write in the window, since that is when the plan reached the
// Latest state the delta row describes.
func TestCoalescerAnchorsChangedAtToLastWrite(t *testing.T) {
	base := time.Date(2026, 7, 7, 18, 4, 0, 0, time.UTC)
	var got FlushPayload
	c := NewCoalescer(time.Hour, func(p FlushPayload) { got = p })

	c.now = func() time.Time { return base }
	c.Record("s1", "- [ ] a\n", "- [ ] a\n- [ ] b\n")
	last := base.Add(500 * time.Millisecond)
	c.now = func() time.Time { return last }
	c.Record("s1", "- [ ] a\n- [ ] b\n", "- [x] a\n- [ ] b\n")
	c.FlushNow("s1")

	if !got.ChangedAt.Equal(last) {
		t.Fatalf("ChangedAt = %s, want the last write instant %s", got.ChangedAt, last)
	}
}
