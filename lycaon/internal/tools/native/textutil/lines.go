package textutil

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/tools/native/nativeutility"
)

// readAll buffers one input under stream_bytes, charging memory first.
func (inv *invocation) readAll(r io.Reader) ([]byte, error) {
	const chunk = 64 << 10
	maximum := inv.limit("stream_bytes")
	var buffer bytes.Buffer
	for {
		if err := inv.reserve(chunk); err != nil {
			return nil, err
		}
		buffer.Grow(chunk)
		n, err := io.CopyN(&buffer, r, chunk)
		if int64(buffer.Len()) > maximum {
			return nil, nativeutility.LimitExceeded("stream_bytes", maximum, int64(buffer.Len()))
		}
		if errors.Is(err, io.EOF) || (err == nil && n < chunk) {
			return buffer.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// eachRecord streams every input's LF records to visit.
func (inv *invocation) eachRecord(visit func(record []byte, terminated bool) error) error {
	sources, err := inv.openInputs(inv.operands())
	if err != nil {
		return err
	}
	defer closeSources(sources)
	for _, source := range sources {
		records, err := inv.records(source.reader, '\n')
		if err != nil {
			return err
		}
		for {
			record, terminated, err := records.nextTerminated()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if err := visit(record, terminated); err != nil {
				return err
			}
		}
	}
	return nil
}

// runTac writes each input's records in reverse order, as GNU tac does: the
// separator (-s, default LF) follows each record, so a final record without
// one is joined to the next record written.
func runTac(inv *invocation) error {
	separator := []byte{'\n'}
	if value, ok := inv.value("-s"); ok {
		if value.Value == "" {
			return inv.usage(value, "a nonempty separator")
		}
		separator = []byte(value.Value)
	}
	sources, err := inv.openInputs(inv.operands())
	if err != nil {
		return err
	}
	defer closeSources(sources)
	for _, source := range sources {
		content, err := inv.readAll(source.reader)
		if err != nil {
			return err
		}
		var records [][]byte
		for len(content) > 0 {
			end := bytes.Index(content, separator)
			if end < 0 {
				end = len(content)
			} else {
				end += len(separator)
			}
			records = append(records, content[:end])
			content = content[end:]
		}
		for _, record := range slices.Backward(records) {
			if _, err := inv.out.Write(record); err != nil {
				return err
			}
		}
	}
	return nil
}

// runRev reverses each line by Unicode scalar; an invalid byte is one scalar.
func runRev(inv *invocation) error {
	return inv.eachRecord(func(record []byte, terminated bool) error {
		var sizes []int
		for i := 0; i < len(record); {
			_, size := utf8.DecodeRune(record[i:])
			sizes = append(sizes, size)
			i += size
		}
		out := make([]byte, 0, len(record)+1)
		end := len(record)
		for _, size := range slices.Backward(sizes) {
			out = append(out, record[end-size:end]...)
			end -= size
		}
		if terminated {
			out = append(out, '\n')
		}
		_, err := inv.out.Write(out)
		return err
	})
}

// runFold wraps lines at -w columns (default 80): tabs advance to the next
// multiple of eight, backspace steps back and carriage return resets, unless
// -b counts bytes. -s breaks after the last blank that fits.
func runFold(inv *invocation) error {
	width := 80
	if value, ok := inv.value("-w"); ok {
		n, err := strconv.Atoi(value.Value)
		if err != nil || n < 1 {
			return inv.usage(value, "a positive WIDTH")
		}
		width = n
	}
	f := folder{width: width, bytes: inv.flag("-b"), spaces: inv.flag("-s")}
	return inv.eachRecord(func(record []byte, terminated bool) error {
		out := f.fold(record)
		if terminated {
			out = append(out, '\n')
		}
		_, err := inv.out.Write(out)
		return err
	})
}

type folder struct {
	width         int
	bytes, spaces bool
}

func (f folder) advance(column int, unit []byte) int {
	switch {
	case f.bytes:
		return column + 1
	case unit[0] == '\b':
		return max(column-1, 0)
	case unit[0] == '\r':
		return 0
	case unit[0] == '\t':
		return column + 8 - column%8
	}
	return column + 1
}

func (f folder) fold(record []byte) []byte {
	var out, line []byte
	column := 0
	for i := 0; i < len(record); {
		size := 1
		if !f.bytes {
			_, size = utf8.DecodeRune(record[i:])
		}
		unit := record[i : i+size]
		if next := f.advance(column, unit); next > f.width && len(line) > 0 {
			cut := len(line)
			if f.spaces {
				if blank := bytes.LastIndexAny(line, " \t"); blank >= 0 {
					cut = blank + 1
				}
			}
			out = append(append(out, line[:cut]...), '\n')
			line = append([]byte{}, line[cut:]...)
			column = 0
			for j := 0; j < len(line); {
				s := 1
				if !f.bytes {
					_, s = utf8.DecodeRune(line[j:])
				}
				column = f.advance(column, line[j:j+s])
				j += s
			}
			continue
		}
		column = f.advance(column, unit)
		line = append(line, unit...)
		i += size
	}
	return append(out, line...)
}
