package textutil

import (
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/commandspill"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

type mergeCursor struct {
	reader commandspill.RunReader
	record []byte
	index  int
}

func (c *mergeCursor) advance() (bool, error) {
	record, err := c.reader.Next()
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	var limit *commandspill.RecordLimitError
	if errors.As(err, &limit) {
		return false, nativeutility.LimitExceeded("record_bytes", limit.Maximum, limit.Observed)
	}
	c.record = record
	return err == nil, err
}

// mergeHeap orders cursors by record and then by run index, which keeps the
// merge stable across runs spilled in input order.
type mergeHeap struct {
	order   sortOrder
	cursors []*mergeCursor
}

func (h *mergeHeap) Len() int { return len(h.cursors) }
func (h *mergeHeap) Less(i, j int) bool {
	if c := h.order.compare(h.cursors[i].record, h.cursors[j].record); c != 0 {
		return c < 0
	}
	return h.cursors[i].index < h.cursors[j].index
}
func (h *mergeHeap) Swap(i, j int) { h.cursors[i], h.cursors[j] = h.cursors[j], h.cursors[i] }
func (h *mergeHeap) Push(x any)    { h.cursors = append(h.cursors, x.(*mergeCursor)) }
func (h *mergeHeap) Pop() any {
	last := h.cursors[len(h.cursors)-1]
	h.cursors = h.cursors[:len(h.cursors)-1]
	return last
}
