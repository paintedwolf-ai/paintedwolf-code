package survey

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/hostmarker"
	"github.com/lycaon/lycaon/internal/structrewrite"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

type readSymbolCandidate struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

func readSymbolArg(args map[string]any) string {
	raw, ok := args["symbol"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(raw)
}

func readKindArg(args map[string]any) string {
	raw, ok := args["kind"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(raw)
}

func readSymbolArgsConflict(args map[string]any) bool {
	if readSymbolArg(args) == "" {
		return false
	}
	if readHasRanges(args) {
		return true
	}
	if _, ok := args["offset"]; ok {
		return true
	}
	if _, ok := args["limit"]; ok {
		return true
	}
	mode := readModeArg(args)
	return mode == "outline" || mode == "content"
}

func (t *ReadTool) runSymbol(ctx context.Context, path, text string, args map[string]any, source *surveyreceipt.SourceContext, capture *readCapture) (string, error) {
	if readSymbolArgsConflict(args) {
		return "", &tools.ToolReject{
			Code: "READ_ARGS_CONFLICT",
			Data: map[string]any{
				"detail": "use symbol alone — not with offset, limit, ranges, or mode",
			},
		}
	}
	symbol := readSymbolArg(args)
	kind := readKindArg(args)
	content := []byte(text)
	matches, err := structrewrite.ExtractSymbol(ctx, "", path, content, structrewrite.SymbolRef{
		Name: symbol,
		Kind: kind,
	})
	if err != nil {
		if reject := sourceview.ParseReject(path, "source", err); reject != nil {
			return "", reject
		}
		return "", fmt.Errorf("read symbol failed: %w", err)
	}
	lang, supported := structrewrite.SupportedLanguage("", path)
	resp := ReadResponse{
		Path:   path,
		Mode:   "symbol",
		Symbol: symbol,
	}
	if kind != "" {
		resp.Kind = kind
	}
	if !supported {
		resp.Count = 0
		resp.Note = fmt.Sprintf("no grammar for %s; use plain read", path)
		return marshalReadSymbolResponse(path, resp, source)
	}
	resp.Language = lang
	switch len(matches) {
	case 0:
		resp.Count = 0
		resp.Note = fmt.Sprintf("symbol %q not found in %s", symbol, path)
	case 1:
		resp.Count = 1
		startLine := matches[0].StartRow + 1
		endLine := matches[0].EndRow + 1
		resp.StartLine = startLine
		resp.EndLine = endLine
		capture.lines(startLine, endLine)
		resp.Content = hostmarker.FormatNumberedLines(linesFromMatch(text, matches[0]), startLine)
		resp.TotalLines = toolkit.CountLines(text)
	default:
		resp.Count = len(matches)
		resp.Note = fmt.Sprintf("symbol %q is ambiguous in %s — pass kind to disambiguate", symbol, path)
		resp.Candidates = make([]readSymbolCandidate, 0, len(matches))
		for _, m := range matches {
			resp.Candidates = append(resp.Candidates, readSymbolCandidate{
				Kind:      m.Bindings["kind"],
				Name:      symbol,
				StartLine: m.StartRow + 1,
				EndLine:   m.EndRow + 1,
			})
		}
	}
	return marshalReadSymbolResponse(path, resp, source)
}

func linesFromMatch(text string, m structrewrite.Match) []string {
	lines := strings.Split(text, "\n")
	start := m.StartRow
	end := m.EndRow
	if start < 0 {
		start = 0
	}
	if end >= len(lines) {
		end = len(lines) - 1
	}
	if start > end {
		return nil
	}
	return lines[start : end+1]
}

func marshalReadSymbolResponse(path string, resp ReadResponse, source *surveyreceipt.SourceContext) (string, error) {
	raw, err := surveyjson.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("read encode: %w", err)
	}
	return attachReadReceiptWithSource(path, len(raw), false, string(raw), source), nil
}
