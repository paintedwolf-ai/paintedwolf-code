package query

import (
	"math/bits"

	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// inputPositions retains one newline bit per consumed byte, rather than a copy
// of all input text. Its bound is one eighth of the existing input limit and
// capacity is admitted by the same native buffered-byte owner.
type inputPositions struct {
	buffer                  *native.BufferedBytes
	maximum, read           int64
	label                   string
	ranked, lines, lastLF   int64
	cachedOffset            int64
	cachedLine              int
	followingLF, lastReadLF int64
}

func newInputPositions(inv *invocation, maximum int64, label string) (*inputPositions, error) {
	buffer, err := inv.owners.Revisions.NewBufferedBytes(inv.ctx, maximum, "stream_bytes")
	if err != nil {
		return nil, err
	}
	return &inputPositions{buffer: buffer, maximum: maximum, label: label, lastLF: -1, cachedOffset: -1, followingLF: -1, lastReadLF: -1}, nil
}

func (p *inputPositions) Write(data []byte) (int, error) {
	if int64(len(data)) > p.maximum-p.read {
		return 0, safecmd.Reject("JQ_INPUT_TOO_LARGE", map[string]any{"path": p.label, "max_bytes": p.maximum, "bytes": p.read + int64(len(data))})
	}
	needed := (p.read+int64(len(data))+7)/8 - int64(len(p.buffer.Bytes))
	var zero [4096]byte
	for needed > 0 {
		n := min(needed, int64(len(zero)))
		if _, err := p.buffer.Write(zero[:n]); err != nil {
			return 0, err
		}
		needed -= n
	}
	for i, b := range data {
		if b == '\n' {
			offset := p.read + int64(i)
			p.buffer.Bytes[offset/8] |= 1 << uint(offset%8)
			p.lastReadLF = offset
		}
	}
	p.read += int64(len(data))
	p.cachedOffset = -1
	return len(data), nil
}

func (p *inputPositions) close() { p.buffer.Close() }

// rank counts complete newline positions before offset. Decoding advances
// monotonically; a diagnostic looking backwards resets the small rank cursor.
func (p *inputPositions) rank(offset int64) (int64, int64) {
	offset = min(max(offset, 0), p.read)
	if offset < p.ranked {
		p.ranked, p.lines, p.lastLF = 0, 0, -1
	}
	for p.ranked < offset {
		index := p.ranked / 8
		low, high := uint(p.ranked%8), uint(min(int64(8), offset-index*8))
		mask := p.buffer.Bytes[index] & byte((uint16(1)<<high)-1) & byte(0xff<<low)
		p.lines += int64(bits.OnesCount8(mask))
		if mask != 0 {
			p.lastLF = index*8 + int64(bits.Len8(mask)) - 1
		}
		p.ranked = index*8 + int64(high)
	}
	return p.lines, p.lastLF
}

func (p *inputPositions) lineAt(offset int64) int {
	offset = min(max(offset, 0), p.read)
	if p.cachedOffset == offset {
		return p.cachedLine
	}
	end := p.read
	if p.followingLF >= offset {
		end = p.followingLF + 1
	} else if offset <= p.lastReadLF {
		for index := offset / 8; index < int64(len(p.buffer.Bytes)); index++ {
			mask := p.buffer.Bytes[index]
			if index == offset/8 {
				mask &= byte(0xff << uint(offset%8))
			}
			if mask != 0 {
				p.followingLF = index*8 + int64(bits.TrailingZeros8(mask))
				end = p.followingLF + 1
				break
			}
		}
	}
	line, _ := p.rank(end)
	p.cachedOffset, p.cachedLine = offset, int(line)
	return int(line)
}

func (p *inputPositions) lineColumn(offset int64) (int, int) {
	offset = min(max(offset, 0), p.read)
	line, last := p.rank(offset)
	return int(line) + 1, int(offset - last - 1)
}
