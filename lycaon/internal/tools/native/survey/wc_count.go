package survey

import (
	"bytes"
	"errors"
	"unicode"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// ErrInvalidEncoding reports bytes that are not UTF-8 where a count needs
// Unicode scalars; byte, line and word counts remain defined.
var ErrInvalidEncoding = errors.New("invalid UTF-8 encoding")

// TextCounts are wc's counts over one captured byte stream. Every count
// comes from the same bytes; none mixes an editor view with disk size.
type TextCounts struct {
	// Lines counts LF bytes, so CRLF is one line and an unterminated last
	// line is not counted.
	Lines int64
	// Words counts maximal runs outside ASCII space, tab, LF, CR, VT and FF.
	Words int64
	Bytes int64
	// Chars counts Unicode scalars; MaxLineLength is the longest line in
	// scalars, excluding its LF or CRLF terminator.
	Chars, MaxLineLength int64
	// InvalidEncoding marks input whose scalar counts are undefined.
	InvalidEncoding bool
}

var wcDisplayColumns = runewidth.Condition{StrictEmojiNeutral: true}

// CountMetrics selects work beyond the byte total, which is always collected.
type CountMetrics uint8

const (
	CountLines CountMetrics = 1 << iota
	CountWords
	CountChars
	CountLineLength
	CountDisplayWidth
	allCountMetrics = CountLines | CountWords | CountChars | CountLineLength | CountDisplayWidth
)

// NewCounter collects only the selected metrics; zero selects bytes alone.
func NewCounter(metrics CountMetrics) Counter {
	return Counter{metrics: metrics}
}

// Counter accumulates counts across chunks. Its zero value counts bytes alone.
type Counter struct {
	metrics                 CountMetrics
	counts                  TextCounts
	inWord                  bool
	line                    int64
	pending                 []byte
	cr                      bool
	display, longestDisplay int64
}

// CountBytes counts one complete captured byte sequence.
func CountBytes(content []byte) TextCounts {
	c := NewCounter(allCountMetrics)
	_, _ = c.Write(content)
	return c.Counts()
}

func (c *Counter) Write(p []byte) (int, error) {
	metrics := c.metrics
	c.counts.Bytes += int64(len(p))
	if metrics&CountLines != 0 {
		c.counts.Lines += int64(bytes.Count(p, []byte{'\n'}))
	}
	if metrics&CountWords != 0 {
		c.words(p)
	}
	if metrics&(CountChars|CountLineLength|CountDisplayWidth) == 0 {
		return len(p), nil
	}
	c.decode(p, metrics)
	return len(p), nil
}

func (c *Counter) words(p []byte) {
	for _, b := range p {
		switch b {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			c.inWord = false
		default:
			if !c.inWord {
				c.counts.Words++
			}
			c.inWord = true
		}
	}
}

func (c *Counter) decode(p []byte, metrics CountMetrics) {
	data := p
	if len(c.pending) > 0 {
		data = append(c.pending, p...)
		c.pending = nil
	}
	for len(data) > 0 {
		if !utf8.FullRune(data) {
			c.pending = append([]byte(nil), data...)
			break
		}
		r, size := utf8.DecodeRune(data)
		if r == utf8.RuneError && size == 1 {
			c.counts.InvalidEncoding = true
		}
		data = data[size:]
		c.scalar(r, metrics)
	}
}

func (c *Counter) scalar(r rune, metrics CountMetrics) {
	if metrics&CountChars != 0 {
		c.counts.Chars++
	}
	if metrics&CountDisplayWidth != 0 {
		c.displayScalar(r)
	}
	if metrics&CountLineLength == 0 {
		return
	}
	if r == '\n' {
		length := c.line
		if c.cr {
			length--
		}
		c.counts.MaxLineLength = max(c.counts.MaxLineLength, length)
		c.line, c.cr = 0, false
		return
	}
	c.line++
	c.cr = r == '\r'
}

func (c *Counter) displayScalar(r rune) {
	switch r {
	case '\n', '\r', '\f':
		c.longestDisplay = max(c.longestDisplay, c.display)
		c.display = 0
	case '\t':
		c.display = (c.display/8 + 1) * 8
	default:
		if unicode.IsPrint(r) {
			c.display += int64(wcDisplayColumns.RuneWidth(r))
		}
	}
}

// DisplayWidth returns wc -L's terminal columns, including eight-column tabs.
func (c *Counter) DisplayWidth() int64 {
	return max(c.longestDisplay, c.display)
}

// Counts returns the totals; a trailing incomplete scalar is invalid encoding.
func (c *Counter) Counts() TextCounts {
	out := c.counts
	if len(c.pending) > 0 {
		out.InvalidEncoding = true
	}
	out.MaxLineLength = max(out.MaxLineLength, c.line)
	return out
}

func countWcFile(content []byte, includeWords bool) TextCounts {
	metrics := CountLines
	if includeWords {
		metrics |= CountWords
	}
	counter := NewCounter(metrics)
	_, _ = counter.Write(content)
	return counter.Counts()
}
