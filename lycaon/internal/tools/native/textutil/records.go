package textutil

import (
	"bufio"
	"errors"
	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
	"io"
)

// records reads delimiter-terminated records under record_bytes; a final
// record without its delimiter is still a record. The returned slice is
// valid until the next call.
type records struct {
	inv           *invocation
	reader        *bufio.Reader
	delimiter     byte
	buffer        []byte
	maximum       int64
	releaseReader func()
	releaseBuffer func()
}

func (inv *invocation) records(r io.Reader, delimiter byte) (*records, error) {
	release, err := inv.ports.Memory.Reserve(inv.ctx, 64<<10)
	if err != nil {
		return nil, err
	}
	records := &records{inv: inv, reader: bufio.NewReaderSize(r, 64<<10), delimiter: delimiter, maximum: inv.limit("record_bytes"), releaseReader: release}
	inv.releases = append(inv.releases, records.Close)
	return records, nil
}
func (r *records) Close() {
	if r.releaseReader != nil {
		r.releaseReader()
		r.releaseReader = nil
	}
	if r.releaseBuffer != nil {
		r.releaseBuffer()
		r.releaseBuffer = nil
	}
	r.buffer = nil
}

// next returns the record without its delimiter, or io.EOF.
func (r *records) next() ([]byte, error) {
	record, _, err := r.nextTerminated()
	return record, err
}

// nextTerminated also reports whether the record ended with its delimiter.
func (r *records) nextTerminated() ([]byte, bool, error) {
	r.buffer = r.buffer[:0]
	for {
		if err := r.inv.ctx.Err(); err != nil {
			return nil, false, err
		}
		chunk, err := r.reader.ReadSlice(r.delimiter)
		if int64(len(r.buffer)+len(chunk)) > r.maximum+1 {
			return nil, false, nativeutility.LimitExceeded("record_bytes", r.maximum, int64(len(r.buffer)+len(chunk)))
		}
		if cap(r.buffer) < len(r.buffer)+len(chunk) {
			capacity := min(int(r.maximum+1), max(len(r.buffer)+len(chunk), 2*cap(r.buffer)))
			release, err := r.inv.ports.Memory.Reserve(r.inv.ctx, int64(capacity))
			if err != nil {
				return nil, false, err
			}
			buffer := make([]byte, len(r.buffer), capacity)
			copy(buffer, r.buffer)
			if r.releaseBuffer != nil {
				r.releaseBuffer()
			}
			r.buffer, r.releaseBuffer = buffer, release
		}
		r.buffer = append(r.buffer, chunk...)
		switch {
		case err == nil:
			record := r.buffer[:len(r.buffer)-1]
			if int64(len(record)) > r.maximum {
				return nil, false, nativeutility.LimitExceeded("record_bytes", r.maximum, int64(len(record)))
			}
			return record, true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(r.buffer) == 0 {
				r.Close()
				return nil, false, io.EOF
			}
			if int64(len(r.buffer)) > r.maximum {
				return nil, false, nativeutility.LimitExceeded("record_bytes", r.maximum, int64(len(r.buffer)))
			}
			return r.buffer, false, nil
		default:
			return nil, false, err
		}
	}
}

// writeRecord emits a record with its delimiter.
func writeRecord(out io.Writer, record []byte, delimiter byte) error {
	if _, err := out.Write(record); err != nil {
		return err
	}
	_, err := out.Write([]byte{delimiter})
	return err
}
