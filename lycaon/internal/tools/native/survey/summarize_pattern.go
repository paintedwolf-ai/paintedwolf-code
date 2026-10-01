package survey

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"regexp"
	"unicode/utf8"
)

var errSummaryPatternNonText = errors.New("summary pattern source is not text")

type summarizePatternProbe struct {
	SkippedPaths  []string
	Matches       []grepMatch
	MatchCount    int
	MatchingFiles int
	Revision      uint64
	NextPath      string
	CursorFound   bool
}

func scanSummarizePatternContent(ctx context.Context, raw []byte, displayPath string, re *regexp.Regexp, maxSamples int) ([]grepMatch, int, error) {
	reader := bufio.NewReaderSize(bytes.NewReader(raw), 64*1024)
	var samples []grepMatch
	total := 0
	lineNumber := 0
	for {
		line := newSummarizePatternLine(reader)
		loc := re.FindReaderIndex(line)
		content, matched := line.matchCapture(loc)
		line.drain()
		if line.readErr != nil {
			return nil, 0, line.readErr
		}
		if line.invalid {
			return nil, 0, errSummaryPatternNonText
		}
		if !line.consumed {
			break
		}
		lineNumber++
		if loc != nil {
			total++
			if len(samples) < maxSamples {
				samples = append(samples, grepMatch{
					Path: displayPath, Line: lineNumber, Content: content, Match: matched, Count: 1,
				})
			}
		}
		if line.fileEOF {
			break
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
	}
	return samples, total, nil
}

type summarizePatternLine struct {
	readErr     error
	reader      *bufio.Reader
	pendingRune rune
	pendingSize int
	hasPending  bool
	finish      bool
	consumed    bool
	fileEOF     bool
	invalid     bool
	presented   int
	capture     patternCapture
}

func newSummarizePatternLine(reader *bufio.Reader) *summarizePatternLine {
	return &summarizePatternLine{reader: reader, capture: newPatternCapture(literalLineCaptureBytes)}
}

func (l *summarizePatternLine) ReadRune() (rune, int, error) {
	if l.finish {
		return 0, 0, io.EOF
	}
	for {
		r, size, err := l.reader.ReadRune()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				l.readErr = err
			}
			l.fileEOF = true
			if l.hasPending {
				l.finish = true
				return l.emitPending()
			}
			l.finish = true
			return 0, 0, io.EOF
		}
		l.consumed = true
		if r == utf8.RuneError && size == 1 || r == 0 {
			l.invalid = true
		}
		if r == '\n' {
			l.finish = true
			if l.hasPending && l.pendingRune != '\r' {
				return l.emitPending()
			}
			l.hasPending = false
			return 0, 0, io.EOF
		}
		if !l.hasPending {
			l.pendingRune, l.pendingSize, l.hasPending = r, size, true
			continue
		}
		outRune, outSize := l.pendingRune, l.pendingSize
		l.pendingRune, l.pendingSize = r, size
		l.record(outRune)
		return outRune, outSize, nil
	}
}

func (l *summarizePatternLine) emitPending() (rune, int, error) {
	r, size := l.pendingRune, l.pendingSize
	l.hasPending = false
	l.record(r)
	return r, size, nil
}

func (l *summarizePatternLine) record(r rune) {
	var encoded [utf8.UTFMax]byte
	n := utf8.EncodeRune(encoded[:], r)
	l.capture.add(encoded[:n])
	l.presented += n
}

func (l *summarizePatternLine) drain() {
	for {
		if _, _, err := l.ReadRune(); err != nil {
			return
		}
	}
}

func (l *summarizePatternLine) matchCapture(loc []int) (string, string) {
	raw := l.capture.bytes()
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[1:]
	}
	content := string(raw)
	if len(loc) != 2 {
		return content, ""
	}
	start := l.presented - len(raw)
	if loc[0] < start || loc[1] > l.presented {
		return content, ""
	}
	matchStart, matchEnd := loc[0]-start, loc[1]-start
	windowStart := max(matchStart-64, 0)
	for windowStart < matchStart && !utf8.RuneStart(raw[windowStart]) {
		windowStart++
	}
	windowEnd := min(windowStart+literalLineCaptureBytes, len(raw))
	for windowEnd > matchEnd && windowEnd < len(raw) && !utf8.RuneStart(raw[windowEnd]) {
		windowEnd--
	}
	return string(raw[windowStart:windowEnd]), string(raw[matchStart:matchEnd])
}

type patternCapture struct {
	buffer []byte
	start  int
	size   int
}

func newPatternCapture(capacity int) patternCapture {
	return patternCapture{buffer: make([]byte, max(capacity, 1))}
}

func (c *patternCapture) add(raw []byte) {
	for _, value := range raw {
		if c.size < len(c.buffer) {
			c.buffer[(c.start+c.size)%len(c.buffer)] = value
			c.size++
			continue
		}
		c.buffer[c.start] = value
		c.start = (c.start + 1) % len(c.buffer)
	}
}

func (c *patternCapture) bytes() []byte {
	out := make([]byte, c.size)
	for i := range out {
		out[i] = c.buffer[(c.start+i)%len(c.buffer)]
	}
	return out
}
