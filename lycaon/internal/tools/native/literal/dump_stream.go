package literal

import (
	"errors"
	"io"
	"math/bits"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
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
	operands commandprogram.Fields
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
			in, err := s.inv.openField(operand, false)
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

// byteMultipliers are the size suffixes od and cmp accept: b is a
// 512-byte block, single letters and iB forms are powers of 1024, B forms
// powers of 1000.
var byteMultipliers = map[string]uint64{
	"": 1, "b": 512, "k": 1 << 10, "K": 1 << 10, "KiB": 1 << 10, "kB": 1000, "KB": 1000,
	"m": 1 << 20, "M": 1 << 20, "MiB": 1 << 20, "MB": 1000 * 1000,
	"G": 1 << 30, "GiB": 1 << 30, "GB": 1000 * 1000 * 1000,
	"T": 1 << 40, "TiB": 1 << 40, "TB": 1000 * 1000 * 1000 * 1000,
}

// byteCount parses a nonnegative count: 0x hexadecimal, leading-zero octal
// or decimal, then an optional multiplier.
func byteCount(value string) (int64, bool) {
	if strings.ContainsAny(value, "_oO") || strings.HasPrefix(value, "0b") || strings.HasPrefix(value, "0B") {
		return 0, false
	}
	digits, multiplier := value, uint64(1)
	n, err := strconv.ParseUint(digits, 0, 63)
	if err != nil {
		digits = strings.TrimRight(value, "bkKmMGTiB")
		var ok bool
		if multiplier, ok = byteMultipliers[value[len(digits):]]; !ok || digits == "" {
			return 0, false
		}
		if n, err = strconv.ParseUint(digits, 0, 63); err != nil {
			return 0, false
		}
	}
	high, product := bits.Mul64(n, multiplier)
	if high != 0 || product > 1<<63-1 {
		return 0, false
	}
	return int64(product), true
}
