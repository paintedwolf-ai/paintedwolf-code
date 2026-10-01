package logview

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
)

// Tailer yields JSONL records appended to a growing capture file across polls. It
// tracks a byte offset and only advances past complete (newline-terminated) lines,
// so a half-written trailing line is re-read whole on the next poll rather than
// being decoded truncated.
type Tailer[T any] struct {
	path   string
	offset int64
}

// NewTailer starts a tailer at the end of the current file, so Poll returns only
// records written after construction. Pass fromStart to replay existing content
// first.
func NewTailer[T any](path string, fromStart bool) *Tailer[T] {
	t := &Tailer[T]{path: path}
	if !fromStart {
		if info, err := os.Stat(path); err == nil {
			t.offset = info.Size()
		}
	}
	return t
}

// Poll returns records appended since the previous Poll. A missing file yields no
// records and no error; a truncated/rotated file resets to the new start.
func (t *Tailer[T]) Poll() ([]T, error) {
	f, err := os.Open(t.path) // #nosec G304 -- capture file path resolved from the debug session dir
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() < t.offset {
		t.offset = 0 // file was truncated or rotated
	}
	if info.Size() == t.offset {
		return nil, nil
	}

	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil, err
	}
	chunk, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	// Only consume through the last newline; keep any partial tail for next poll.
	lastNL := bytes.LastIndexByte(chunk, '\n')
	if lastNL < 0 {
		return nil, nil
	}
	complete := chunk[:lastNL+1]
	t.offset += int64(len(complete))

	var out []T
	for _, line := range bytes.Split(complete, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec T
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}
