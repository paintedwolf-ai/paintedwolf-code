package survey

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/internal/repomap"
	"github.com/lycaon/lycaon/internal/tools/native/toolkit"
	"github.com/lycaon/lycaon/internal/tools/readcaps"
	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/lycaon/lycaon/pkg/api"
)

type readOutlineSymbol struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Line int    `json:"line"`
}

type readHighlight struct {
	Handle  string `json:"handle,omitempty"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Content string `json:"content"`
}

type readGlossLine struct {
	Label string `json:"label"`
}

type ReadResponse struct {
	Path              string                         `json:"path"`
	Mode              string                         `json:"mode"`
	Content           string                         `json:"content,omitempty"`
	Symbols           []readOutlineSymbol            `json:"symbols,omitempty"`
	Symbol            string                         `json:"symbol,omitempty"`
	Kind              string                         `json:"kind,omitempty"`
	Count             int                            `json:"count,omitempty"`
	Note              string                         `json:"note,omitempty"`
	Highlights        []readHighlight                `json:"highlights,omitempty"`
	Gloss             []readGlossLine                `json:"gloss,omitempty"`
	Selected          int                            `json:"selected,omitempty"`
	Total             int                            `json:"total,omitempty"`
	Candidates        []readSymbolCandidate          `json:"candidates,omitempty"`
	StartLine         int                            `json:"start_line,omitempty"`
	EndLine           int                            `json:"end_line,omitempty"`
	TotalLines        int                            `json:"total_lines"`
	Offset            int                            `json:"offset,omitempty"`
	Limit             int                            `json:"limit,omitempty"`
	Ranges            []readRangeBlock               `json:"ranges,omitempty"`
	Clamped           string                         `json:"clamped,omitempty"`
	Truncated         bool                           `json:"truncated"`
	NextOffset        *int                           `json:"next_offset,omitempty"`
	TruncationBanner  string                         `json:"truncation_banner,omitempty"`
	OutlineSource     string                         `json:"outline_source,omitempty"`
	OutlineKind       api.OutlineKind                `json:"outline_kind,omitempty"`
	Language          string                         `json:"language,omitempty"`
	LogDigest         *logoutline.Digest             `json:"log_digest,omitempty"`
	Parses            *bool                          `json:"parses,omitempty"`
	Errors            []fileoutline.SyntaxDiagnostic `json:"errors,omitempty"`
	ParseFailure      *tsparse.Failure               `json:"parse_failure,omitempty"`
	Diagnostics       *repomap.Diagnostics           `json:"diagnostics,omitempty"`
	SurveyRecommended bool                           `json:"survey_recommended,omitempty"`
}

func readModeArg(args map[string]any) string {
	raw, ok := args["mode"].(string)
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "outline":
		return "outline"
	case "content":
		return "content"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

func readWantsOutline(mode string, args map[string]any, totalLines int) bool {
	if readSymbolArg(args) != "" {
		return false
	}
	if readHasRanges(args) {
		return false
	}
	if mode == "content" {
		return false
	}
	if mode == "outline" {
		return true
	}
	if _, ok := args["offset"]; ok {
		return false
	}
	if _, ok := args["limit"]; ok {
		return false
	}
	return totalLines > readcaps.AutoOutlineThreshold
}

func buildReadOutlineResponse(path string, outline fileoutline.Result) ReadResponse {
	var symbols []readOutlineSymbol
	var summary string
	large := outline.TotalLines > readcaps.AutoOutlineThreshold
	if outline.LogDigest == nil {
		symbols = make([]readOutlineSymbol, 0, len(outline.Symbols))
		for _, sym := range outline.Symbols {
			symbols = append(symbols, readOutlineSymbol{
				Kind: sym.Kind,
				Name: sym.Name,
				Line: sym.Line,
			})
		}
		switch {
		case large && len(outline.Symbols) > 0:
			summary = fmt.Sprintf(
				"%d lines — large file (outline only); symbols[] lists defs with line numbers. For how/what: summarize(path=%q). For edits: read(symbol=…) or ranges at known lines — not full-file paging",
				outline.TotalLines,
				path,
			)
		case large:
			summary = fmt.Sprintf(
				"%d lines — large file (outline only, no defs in symbols[]). For how/what: summarize(path=%q); else grep to anchor then ranges (short files: mode=content once OK)",
				outline.TotalLines,
				path,
			)
		default:
			summary = fmt.Sprintf(
				"%d lines — requested outline; symbols[] lists available definitions. Use mode=content for the full short file",
				outline.TotalLines,
			)
		}
		if outline.Parses != nil && !*outline.Parses && len(outline.Errors) > 0 {
			summary += fmt.Sprintf(
				"; Parse: %d tree-sitter syntax error(s), first at line %d — see errors[] (informational; build/verify is authoritative)",
				len(outline.Errors), outline.Errors[0].Row,
			)
		}
	} else {
		summary = fmt.Sprintf(
			"%d lines — log digest (format %s, %d/%d records parsed); for how/what summarize(path=%q); else survey facets then ranges at cluster anchors (not full-file paging)",
			outline.TotalLines,
			outline.LogDigest.Format,
			outline.LogDigest.ParsedCount,
			outline.LogDigest.RecordCount,
			path,
		)
	}
	resp := ReadResponse{
		Path:              path,
		Mode:              "outline",
		Symbols:           symbols,
		TotalLines:        outline.TotalLines,
		OutlineSource:     outline.Source,
		Language:          outline.Language,
		TruncationBanner:  toolkit.OutlineBanner(summary),
		SurveyRecommended: large,
	}
	if outline.LogDigest == nil {
		resp.OutlineKind = api.OutlineKindSymbols
		resp.Parses = outline.Parses
		resp.Errors = outline.Errors
		resp.Diagnostics = outline.Diagnostics
		resp.ParseFailure = outline.ParseFailure
	} else {
		applyLogOutlineFields(&resp, outline)
	}
	return resp
}

func applyLogOutlineFields(resp *ReadResponse, outline fileoutline.Result) {
	if outline.LogDigest == nil {
		return
	}
	resp.OutlineKind = api.OutlineKindLogDigest
	resp.LogDigest = outline.LogDigest
	resp.OutlineSource = "log"
	resp.Symbols = nil
	resp.Parses = nil
	resp.Errors = nil
	resp.Diagnostics = nil
	resp.Language = ""
}
