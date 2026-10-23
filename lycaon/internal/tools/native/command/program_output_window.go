package command

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

func snapHeadEnd(data []byte, capBytes int) int {
	if capBytes >= len(data) {
		return len(data)
	}
	if capBytes <= 0 {
		return 0
	}
	if idx := bytes.LastIndexByte(data[:capBytes], '\n'); idx >= 0 {
		return idx + 1
	}
	return 0
}

func snapTailStart(data []byte, headEnd, capBytes int) int {
	start := max(len(data)-capBytes, headEnd)
	if start >= len(data) {
		return len(data)
	}
	if start > 0 && data[start-1] == '\n' {
		return start
	}
	if idx := bytes.IndexByte(data[start:], '\n'); idx >= 0 && start+idx+1 < len(data) {
		return start + idx + 1
	}
	return len(data)
}

func countLines(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	n := bytes.Count(b, []byte{'\n'})
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

func formatDecimalCommas(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		b.WriteString(s[:rem])
	}
	for i := rem; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func formatOmittedSize(bytesCount int64) string {
	return fmt.Sprintf("%s bytes", formatDecimalCommas(int(bytesCount)))
}

// outputSample keeps only the model's window while counting the entire screened
// stream. The retained body, never this preview, supplies subsequent shell reads.
type outputSample struct {
	head, tail []byte
	budget     int
	total      int64
	lines      int
	last       byte
}

type deliveredOutputPart struct {
	start int64
	data  []byte
}

func sampleOutput(reader io.Reader, budget int) (outputSample, error) {
	sample := outputSample{budget: budget}
	buffer := make([]byte, 64<<10)
	tailLimit := budget - budget/2 + 1
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			data := buffer[:n]
			sample.total += int64(n)
			sample.lines += bytes.Count(data, []byte{'\n'})
			sample.last = data[n-1]
			keep := min(n, max(0, budget-len(sample.head)))
			sample.head = append(sample.head, data[:keep]...)
			if n >= tailLimit {
				sample.tail = append(sample.tail[:0], data[n-tailLimit:]...)
			} else {
				drop := max(0, len(sample.tail)+n-tailLimit)
				copy(sample.tail, sample.tail[drop:])
				sample.tail = append(sample.tail[:len(sample.tail)-drop], data...)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return sample, err
		}
	}
	if sample.total > 0 && sample.last != '\n' {
		sample.lines++
	}
	return sample, nil
}

func (s outputSample) window(ctx context.Context, stream, ref, path string, complete bool) (string, api.CommandProgramOutputWindow, []deliveredOutputPart, error) {
	window := api.CommandProgramOutputWindow{Stream: stream, Ref: ref, OutputPath: path, TotalBytes: s.total, TotalLines: s.lines, Complete: complete, InlineBudgetBytes: s.budget}
	if s.total <= int64(s.budget) {
		return string(s.head), window, []deliveredOutputPart{{0, s.head}}, nil
	}
	headEnd := snapHeadEnd(s.head, s.budget/2)
	tailStart := snapTailStart(s.tail, 0, s.budget-s.budget/2)
	head, tail := s.head[:headEnd], s.tail[tailStart:]
	start := s.total - int64(len(tail))
	omittedLines := s.lines - countLines(head) - countLines(tail)
	nextLine := countLines(head) + 1
	endLine := min(nextLine+149, nextLine+omittedLines-1)
	notice, err := guidance.RenderCommandOutputOmitted(ctx, formatDecimalCommas(omittedLines), formatOmittedSize(start-int64(headEnd)), path, nextLine, max(nextLine, endLine), s.lines, s.total, complete)
	if err != nil {
		return "", window, nil, err
	}
	window.Truncated = true
	window.HeadEndByte, window.HeadEndLine = int64(headEnd), countLines(head)
	window.TailStartByte, window.TailStartLine = start, s.lines-countLines(tail)+1
	window.OmittedBytes, window.OmittedLines = start-int64(headEnd), omittedLines
	return string(head) + notice + "\n" + string(tail), window, []deliveredOutputPart{{0, head}, {start, tail}}, nil
}
