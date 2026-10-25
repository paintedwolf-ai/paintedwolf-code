package textutil

import (
	"bufio"
	"errors"
	"io"

	"github.com/lycaon/lycaon/internal/tools"
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
	offset        int64
	nextOffset    int64
	releaseReader func()
	releaseBuffer func()
}

func (inv *invocation) records(r io.Reader, delimiter byte) (*records, error) {
	release, err := inv.ports.Memory.Reserve(inv.ctx, 64<<10)
	if err != nil {
		return nil, err
	}
	records := &records{inv: inv, reader: bufio.NewReaderSize(r, 64<<10), delimiter: delimiter, maximum: inv.limit("record_bytes"), releaseReader: release}
	return records, nil
}
func (r *records) Close() {
	r.reader = nil
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
	if r.reader == nil {
		return nil, false, io.EOF
	}
	r.buffer = r.buffer[:0]
	r.offset = r.nextOffset
	for {
		if err := r.inv.ctx.Err(); err != nil {
			return nil, false, err
		}
		chunk, err := r.reader.ReadSlice(r.delimiter)
		r.nextOffset += int64(len(chunk))
		observed := int64(len(r.buffer) + len(chunk))
		if err == nil {
			observed--
		}
		if observed > r.maximum {
			return nil, false, tools.CommandLimitExceeded("record_bytes", r.maximum, observed)
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
			return record, true, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(r.buffer) == 0 {
				r.Close()
				return nil, false, io.EOF
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

// consume owns framing storage until the selected source is finished.
func (r *records) consume(visit func([]byte, bool) error) error {
	defer r.Close()
	for {
		record, terminated, err := r.nextTerminated()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := visit(record, terminated); err != nil {
			return err
		}
	}
}

func closeRecords(readers []*records) {
	for _, reader := range readers {
		if reader != nil {
			reader.Close()
		}
	}
}
