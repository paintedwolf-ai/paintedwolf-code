package search

import (
	"encoding/base64"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/tools"
)

// grepRecord holds one transformed record under the program's byte admission.
type grepRecord struct {
	inv     *invocation
	bytes   []byte
	release func()
}

func (b *grepRecord) close() {
	b.bytes = nil
	if b.release != nil {
		b.release()
		b.release = nil
	}
}

func (b *grepRecord) grow(n int) error {
	if err := b.inv.ctx.Err(); err != nil {
		return err
	}
	needed := int64(len(b.bytes)) + int64(n)
	if b.inv.out.maximum > 0 && needed > b.inv.out.maximum {
		return tools.CommandLimitExceeded("stream_throughput_bytes", b.inv.out.maximum, needed)
	}
	if n <= cap(b.bytes)-len(b.bytes) {
		return nil
	}
	capacity := max(int(needed), max(256, 2*cap(b.bytes)))
	if b.inv.out.maximum > 0 {
		capacity = min(capacity, int(b.inv.out.maximum))
	}
	var release func()
	if memory := b.inv.owners.Revisions.Memory; memory != nil {
		var err error
		release, err = memory.Reserve(b.inv.ctx, int64(capacity))
		if err != nil {
			return err
		}
	}
	next := make([]byte, len(b.bytes), capacity)
	copy(next, b.bytes)
	oldRelease := b.release
	b.bytes, b.release = next, release
	if oldRelease != nil {
		oldRelease()
	}
	return nil
}

func (b *grepRecord) text(parts ...string) error {
	for _, part := range parts {
		if err := b.grow(len(part)); err != nil {
			return err
		}
		b.bytes = append(b.bytes, part...)
	}
	return nil
}

func (b *grepRecord) data(raw []byte) error {
	if err := b.grow(len(raw)); err != nil {
		return err
	}
	b.bytes = append(b.bytes, raw...)
	return nil
}

func (b *grepRecord) jsonData(raw []byte) error {
	if !utf8.Valid(raw) {
		if err := b.text(`{"bytes":"`); err != nil {
			return err
		}
		n := base64.StdEncoding.EncodedLen(len(raw))
		if err := b.grow(n); err != nil {
			return err
		}
		start := len(b.bytes)
		b.bytes = b.bytes[:start+n]
		base64.StdEncoding.Encode(b.bytes[start:], raw)
		return b.text(`"}`)
	}
	if err := b.text(`{"text":"`); err != nil {
		return err
	}
	start := 0
	for i, c := range raw {
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		if err := b.data(raw[start:i]); err != nil {
			return err
		}
		escaped := []byte{'\\', c}
		switch c {
		case '\n':
			escaped[1] = 'n'
		case '\r':
			escaped[1] = 'r'
		case '\t':
			escaped[1] = 't'
		case '\b':
			escaped[1] = 'b'
		case '\f':
			escaped[1] = 'f'
		default:
			if c < 0x20 {
				const hex = "0123456789abcdef"
				escaped = []byte{'\\', 'u', '0', '0', hex[c>>4], hex[c&15]}
			}
		}
		if err := b.data(escaped); err != nil {
			return err
		}
		start = i + 1
	}
	if err := b.data(raw[start:]); err != nil {
		return err
	}
	return b.text(`"}`)
}

func (b *grepRecord) replacement(replacement string, record []byte, groups []int) error {
	start := 0
	for i := 0; i < len(replacement); i++ {
		if replacement[i] != '$' || i+1 >= len(replacement) {
			continue
		}
		if err := b.text(replacement[start:i]); err != nil {
			return err
		}
		if replacement[i+1] == '$' {
			if err := b.text("$"); err != nil {
				return err
			}
			i++
			start = i + 1
			continue
		}
		n, size, ok := replacementGroup(replacement[i+1:])
		if !ok {
			start = i
			continue
		}
		if n >= 0 && n < len(groups)/2 && groups[2*n] >= 0 {
			if err := b.data(record[groups[2*n]:groups[2*n+1]]); err != nil {
				return err
			}
		}
		i += size
		start = i + 1
	}
	return b.text(replacement[start:])
}
