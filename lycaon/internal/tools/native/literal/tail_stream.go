package literal

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"io"
)

// tailInput seeks regular pinned files and retains only a bounded trailing
// window for non-seekable input. Neither path gathers the complete source.
func tailInput(inv *invocation, in *input, w window) error {
	if memory := inv.owners.Revisions.Memory; memory != nil {
		release, err := memory.Reserve(inv.ctx, 96<<10)
		if err != nil {
			return err
		}
		defer release()
	}
	if w.fromStart {
		return tailFromStart(inv, in, w)
	}
	if w.count == 0 {
		_, err := io.Copy(io.Discard, in.reader())
		return err
	}
	if reader, ok := in.reader().(io.ReadSeeker); ok {
		return tailSeekable(inv, in, reader, w)
	}
	retained := tailWindow{maximum: inv.owners.limit("stream_bytes"), memory: inv.owners.Revisions.Memory, ctx: inv.ctx}
	defer retained.close()
	buffer := make([]byte, 32<<10)
	for {
		if err := inv.ctx.Err(); err != nil {
			return err
		}
		n, err := in.reader().Read(buffer)
		if n > 0 {
			if err := retained.append(buffer[:n], w); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	start := tailStart(retained.data, w)
	return inv.out.literal(in, retained.data[start:], retained.base+start)
}

func tailSeekable(inv *invocation, in *input, reader io.ReadSeeker, w window) error {
	size, err := reader.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	start := int64(0)
	if w.bytes {
		start = max(0, size-w.count)
	} else if w.count == 0 {
		start = size
	} else {
		buffer := make([]byte, 64<<10)
		remaining := w.count
		for end := size; end > 0 && remaining > 0; {
			if err := inv.ctx.Err(); err != nil {
				return err
			}
			begin := max(0, end-int64(len(buffer)))
			if _, err = reader.Seek(begin, io.SeekStart); err != nil {
				return err
			}
			block := buffer[:end-begin]
			if _, err = io.ReadFull(reader, block); err != nil {
				return err
			}
			for i := len(block) - 1; i >= 0; i-- {
				if begin+int64(i) == size-1 && block[i] == '\n' {
					continue
				}
				if block[i] == '\n' {
					remaining--
					if remaining == 0 {
						start = begin + int64(i) + 1
						break
					}
				}
			}
			end = begin
		}
	}
	if _, err = reader.Seek(start, io.SeekStart); err != nil {
		return err
	}
	return copyLiteral(inv, in, reader, start)
}

func copyLiteral(inv *invocation, in *input, reader io.Reader, offset int64) error {
	buffer := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if writeErr := inv.out.literal(in, buffer[:n], offset); writeErr != nil {
				return writeErr
			}
			offset += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func tailFromStart(inv *invocation, in *input, w window) error {
	if w.bytes {
		skipped, err := io.CopyN(io.Discard, in.reader(), max(0, w.count-1))
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		return copyLiteral(inv, in, in.reader(), skipped)
	}
	line := int64(1)
	return eachLine(in, func(f fragment) error {
		var err error
		if line >= w.count {
			err = inv.out.literal(in, f.data, f.offset)
		}
		if f.ends {
			line++
		}
		return err
	})
}

type tailWindow struct {
	memory                     *toolcontract.ProgramMemory
	ctx                        context.Context
	releaseData, releaseBreaks func()
	data                       []byte
	breaks                     []int64
	firstBreak                 int
	base, total, maximum       int64
}

func (t *tailWindow) append(p []byte, w window) error {
	t.total += int64(len(p))
	if w.bytes {
		if w.count == 0 {
			t.base = t.total
			return nil
		}
		if int64(len(p)) >= w.count {
			p = p[int64(len(p))-w.count:]
			t.data = t.data[:0]
		}
		drop := max(0, int64(len(t.data)+len(p))-w.count)
		t.data = t.data[drop:]
	} else {
		for i, c := range p {
			if c == '\n' {
				if int64(len(t.breaks)-t.firstBreak) == min(w.count, t.maximum)+1 {
					t.firstBreak++
				}
				if t.firstBreak > 0 && t.firstBreak >= cap(t.breaks)/2 {
					copy(t.breaks, t.breaks[t.firstBreak:])
					t.breaks = t.breaks[:len(t.breaks)-t.firstBreak]
					t.firstBreak = 0
				}
				if int64(len(t.data)+len(p))+8*int64(len(t.breaks)+1) > t.maximum {
					return tools.CommandLimitExceeded("stream_bytes", t.maximum, int64(len(t.data)+len(p))+8*int64(len(t.breaks)+1))
				}
				if len(t.breaks) == cap(t.breaks) {
					capacity := max(16, 2*cap(t.breaks))
					release, err := t.reserve(int64(capacity) * 8)
					if err != nil {
						return err
					}
					breaks := make([]int64, len(t.breaks), capacity)
					copy(breaks, t.breaks)
					if t.releaseBreaks != nil {
						t.releaseBreaks()
					}
					t.breaks, t.releaseBreaks = breaks, release
				}
				t.breaks = append(t.breaks, t.total-int64(len(p))+int64(i))
			}
		}
	}
	if int64(len(t.data)+len(p)) > t.maximum {
		return tools.CommandLimitExceeded("stream_bytes", t.maximum, int64(len(t.data)+len(p)))
	}
	if len(t.data)+len(p) > cap(t.data) {
		capacity := min(t.maximum, max(int64(len(t.data)+len(p)), 2*int64(cap(t.data))))
		release, err := t.reserve(capacity)
		if err != nil {
			return err
		}
		data := make([]byte, len(t.data), int(capacity))
		copy(data, t.data)
		if t.releaseData != nil {
			t.releaseData()
		}
		t.data, t.releaseData = data, release
	}
	t.data = append(t.data, p...)
	t.base = t.total - int64(len(t.data))
	if !w.bytes && int64(len(t.breaks)-t.firstBreak) == min(w.count, t.maximum)+1 {
		drop := t.breaks[t.firstBreak] + 1 - t.base
		if drop > 0 {
			t.data = t.data[drop:]
			t.base += drop
		}
	}
	return nil
}

func (t *tailWindow) reserve(bytes int64) (func(), error) {
	if t.memory == nil {
		return func() {}, nil
	}
	return t.memory.Reserve(t.ctx, bytes)
}
func (t *tailWindow) close() {
	if t.releaseData != nil {
		t.releaseData()
		t.releaseData = nil
	}
	if t.releaseBreaks != nil {
		t.releaseBreaks()
		t.releaseBreaks = nil
	}
	t.data, t.breaks = nil, nil
}
