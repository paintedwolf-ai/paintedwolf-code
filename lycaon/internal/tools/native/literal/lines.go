package literal

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

const lineBufferBytes = 64 * 1024

// fragment is part of one LF-delimited line at offset in its input. A line
// longer than the stream buffer arrives as several fragments; starts and
// ends mark its first and last. The last line may end without LF.
type fragment struct {
	data         []byte
	offset       int64
	starts, ends bool
}

// terminated reports whether the fragment carries its line's LF.
func (f fragment) terminated() bool { return len(f.data) > 0 && f.data[len(f.data)-1] == '\n' }

// body is the fragment without its LF.
func (f fragment) body() []byte {
	if f.terminated() {
		return f.data[:len(f.data)-1]
	}
	return f.data
}

// eachLine visits an input's lines in order without materializing a stream.
func eachLine(in *input, visit func(fragment) error) error {
	if in.stream == nil {
		data, offset := in.data, int64(0)
		for len(data) > 0 {
			end := bytes.IndexByte(data, '\n') + 1
			if end == 0 {
				end = len(data)
			}
			if err := visit(fragment{data: data[:end], offset: offset, starts: true, ends: true}); err != nil {
				return err
			}
			data, offset = data[end:], offset+int64(end)
		}
		return nil
	}
	reader := bufio.NewReaderSize(in.stream, lineBufferBytes)
	offset, starts := int64(0), true
	for {
		data, err := reader.ReadSlice('\n')
		full := errors.Is(err, bufio.ErrBufferFull)
		if len(data) > 0 {
			ends := !full
			if visitErr := visit(fragment{data: data, offset: offset, starts: starts, ends: ends}); visitErr != nil {
				return visitErr
			}
			offset += int64(len(data))
			starts = ends
		}
		switch {
		case err == nil || full:
		case errors.Is(err, io.EOF):
			return nil
		default:
			return err
		}
	}
}
