package literal

import (
	"errors"
	"io"
)

// dumpRange selects bytes without gathering the source. Negative PATH offsets
// use the original capture pin's length, including non-seekable decoded inputs.
func (inv *invocation) dumpRange(reader io.Reader, size int64, lengthKey string, negative bool) (io.Reader, int64, error) {
	start := int64(0)
	if value, ok := inv.value("-s"); ok {
		n, err := parseOffset(value)
		if err != nil || n < 0 && !negative {
			return nil, 0, inv.usage(value, "offset (negative offsets require a PATH)")
		}
		if n < 0 {
			n = max(size+n, 0)
		}
		start = min(n, size)
	}
	read, err := io.CopyN(io.Discard, reader, start)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			return nil, 0, err
		}
		return io.LimitReader(reader, 0), read, nil
	}
	if value, ok := inv.value(lengthKey); ok {
		n, err := parseOffset(value)
		if err != nil || n < 0 {
			return nil, 0, inv.usage(value, "nonnegative byte count")
		}
		reader = io.LimitReader(reader, n)
	}
	return reader, start, nil
}

// operandSequence opens only the operand reached by a concatenated byte stream.
// The invocation closes the previous stream before acquiring the next one.
type operandSequence struct {
	inv      *invocation
	operands []string
	current  io.Reader
}

func (s *operandSequence) Read(p []byte) (int, error) {
	for {
		if s.current == nil {
			if len(s.operands) == 0 {
				return 0, io.EOF
			}
			operand := s.operands[0]
			s.operands = s.operands[1:]
			in, err := s.inv.open(operand, false)
			if err != nil {
				return 0, err
			}
			if in == nil {
				continue
			}
			s.current = in.reader()
		}
		n, err := s.current.Read(p)
		if err == io.EOF {
			s.current = nil
			if n > 0 {
				return n, nil
			}
			continue
		}
		return n, err
	}
}
