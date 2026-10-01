package sourcecatalog

import (
	"errors"
	"io"
)

// One lookahead entry lets a short directory publish complete membership once.
type directoryBatch struct {
	file interface {
		ReadDir(int) ([]directoryEntry, error)
	}
	next directoryEntry
	done bool
}

func (r *directoryBatch) read(limit int) ([]directoryEntry, bool, error) {
	entries := make([]directoryEntry, 0, limit+1)
	if r.next != nil {
		entries = append(entries, r.next)
		r.next = nil
	}
	for !r.done && len(entries) <= limit {
		batch, err := r.file.ReadDir(limit + 1 - len(entries))
		entries = append(entries, batch...)
		if errors.Is(err, io.EOF) {
			r.done = true
			break
		}
		if err != nil {
			return nil, false, err
		}
	}
	if len(entries) <= limit {
		return entries, r.done, nil
	}
	r.next = entries[limit]
	return entries[:limit], false, nil
}
