package survey

import (
	"fmt"
	"sort"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

type readRangeSpec struct {
	Offset int
	Limit  int
}

type readRangeBlock struct {
	Offset  int    `json:"offset"`
	Limit   int    `json:"limit"`
	EndLine int    `json:"end_line"`
	Content string `json:"content"`
}

func readHasRanges(args map[string]any) bool {
	raw, ok := args["ranges"].([]any)
	return ok && len(raw) > 0
}

func readArgsConflict(args map[string]any) bool {
	if !readHasRanges(args) {
		return false
	}
	if _, ok := args["offset"]; ok {
		return true
	}
	if _, ok := args["limit"]; ok {
		return true
	}
	return readModeArg(args) == "outline"
}

// parseReadRangeSpecs clamps ranges to the batch caps and reports any truncation.
func parseReadRangeSpecs(args map[string]any) ([]readRangeSpec, string, error) {
	raw, ok := args["ranges"].([]any)
	if !ok || len(raw) == 0 {
		return nil, "", toolkit.MissingArg("ranges")
	}
	requestedRanges := len(raw)
	if requestedRanges > readcaps.BatchMaxRanges {
		raw = raw[:readcaps.BatchMaxRanges]
	}
	parsed := make([]readRangeSpec, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, "", fmt.Errorf("ranges[%d]: expected object", i)
		}
		offset := toolkit.BoundedIntArg(m, "offset", 1, 1, 1<<30).Effective
		limit := toolkit.BoundedIntArg(m, "limit", readcaps.LineLimit, 1, readcaps.LineLimit).Effective
		parsed = append(parsed, readRangeSpec{Offset: offset, Limit: limit})
	}

	requestedLines := 0
	for _, spec := range parsed {
		requestedLines += spec.Limit
	}

	// Serve ranges in request order until the line budget runs out; the last
	// surviving range shrinks to the remaining budget.
	out := make([]readRangeSpec, 0, len(parsed))
	remaining := readcaps.BatchMaxTotalLines
	for _, spec := range parsed {
		if remaining <= 0 {
			break
		}
		if spec.Limit > remaining {
			spec.Limit = remaining
		}
		remaining -= spec.Limit
		out = append(out, spec)
	}

	servedLines := readcaps.BatchMaxTotalLines - remaining
	note := ""
	if requestedRanges > readcaps.BatchMaxRanges || requestedLines > servedLines {
		note = fmt.Sprintf(
			"served %d of %d requested ranges (%d of %d lines) — per-call budget is %d ranges / %d lines; call read again with the remaining ranges",
			len(out), requestedRanges, servedLines, requestedLines,
			readcaps.BatchMaxRanges, readcaps.BatchMaxTotalLines,
		)
	}
	return out, note, nil
}

func (t *ReadTool) runBatch(path, text string, args map[string]any, source *surveyreceipt.SourceContext, capture *readCapture) (string, error) {
	if readArgsConflict(args) {
		return "", &tools.ToolReject{
			Code: "READ_ARGS_CONFLICT",
			Data: map[string]any{
				"detail": "use ranges alone, or offset/limit/mode=content — not both",
			},
		}
	}
	specs, clampNote, err := parseReadRangeSpecs(args)
	if err != nil {
		return "", err
	}
	totalLines := toolkit.CountLines(text)
	sort.Slice(specs, func(i, j int) bool {
		if specs[i].Offset != specs[j].Offset {
			return specs[i].Offset < specs[j].Offset
		}
		return specs[i].Limit < specs[j].Limit
	})
	resp := ReadResponse{
		Path:       path,
		Mode:       "ranges",
		TotalLines: totalLines,
		Ranges:     make([]readRangeBlock, 0, len(specs)),
		Clamped:    clampNote,
	}
	for _, spec := range specs {
		if spec.Offset > totalLines && totalLines > 0 {
			return "", readOffsetBeyondEOF(path, spec.Offset, totalLines)
		}
		page, _, endLine, _ := PaginateLines(text, spec.Offset, spec.Limit)
		capture.lines(spec.Offset, endLine)
		resp.Ranges = append(resp.Ranges, readRangeBlock{
			Offset:  spec.Offset,
			Limit:   spec.Limit,
			EndLine: endLine,
			Content: hostmarker.FormatNumberedLines(page, spec.Offset),
		})
	}
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("read encode: %w", err)
	}
	return attachReadReceiptWithSource(path, len(raw), false, string(raw), source), nil
}

// attachReadReceiptWithSource is the receipt path for read modes that skip the
// age/reference context but still state the served bytes' ledger identity.
func attachReadReceiptWithSource(path string, bytesReturned int, truncated bool, output string, source *surveyreceipt.SourceContext) string {
	r := surveyreceipt.New("read", path, 1, bytesReturned, truncated)
	r.Source = source
	return surveyreceipt.Attach(output, r)
}
