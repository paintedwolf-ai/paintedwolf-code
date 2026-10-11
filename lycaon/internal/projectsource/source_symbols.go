package projectsource

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/repomap"
)

// SourceSymbolsMaxEntries bounds one file outline.
const SourceSymbolsMaxEntries = 2000

// SourceSymbolKind is the closed wire set for in-file navigation outlines.
type SourceSymbolKind string

const (
	SourceSymbolKindFunction SourceSymbolKind = "function"
	SourceSymbolKindMethod   SourceSymbolKind = "method"
	SourceSymbolKindType     SourceSymbolKind = "type"
	SourceSymbolKindClass    SourceSymbolKind = "class"
	SourceSymbolKindConstant SourceSymbolKind = "constant"
	SourceSymbolKindHeading  SourceSymbolKind = "heading"
)

// SourceSymbol is one declaration in document order.
type SourceSymbol struct {
	Name string
	Kind SourceSymbolKind
	Line int // 1-based
}

// SourceSymbolsResult is the outline for GET …/source/symbols.
type SourceSymbolsResult struct {
	SHA256    string
	Symbols   []SourceSymbol
	Truncated bool
}

// SourceSymbolsRequest addresses one root-relative file.
type SourceSymbolsRequest struct {
	Path   string
	RootID string
}

// ListProjectSourceSymbols returns document-order declarations.
func ListProjectSourceSymbols(ctx context.Context, p ProjectSource, req SourceSymbolsRequest) (SourceSymbolsResult, error) {
	if err := ctx.Err(); err != nil {
		return SourceSymbolsResult{}, err
	}
	read, err := ReadProjectSource(p, SourceReadRequest{
		Path:   req.Path,
		RootID: req.RootID,
	})
	if err != nil {
		return SourceSymbolsResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return SourceSymbolsResult{}, err
	}
	analysis := fileoutline.AnalyzeText(ctx, read.Path, []byte(read.Content))
	if err := ctx.Err(); err != nil {
		return SourceSymbolsResult{}, err
	}
	symbols, err := sourceSymbolsFromAnalysis(analysis)
	if err != nil {
		return SourceSymbolsResult{}, err
	}
	truncated := false
	if len(symbols) > SourceSymbolsMaxEntries {
		symbols = symbols[:SourceSymbolsMaxEntries]
		truncated = true
	}
	if symbols == nil {
		symbols = []SourceSymbol{}
	}
	return SourceSymbolsResult{SHA256: read.SHA256, Symbols: symbols, Truncated: truncated}, nil
}

// SourceSymbolsForContent projects definitions into symbol categories.
func SourceSymbolsForContent(ctx context.Context, relPath string, src []byte) ([]SourceSymbol, error) {
	analysis := fileoutline.AnalyzeText(ctx, relPath, src)
	return sourceSymbolsFromAnalysis(analysis)
}

func sourceSymbolsFromAnalysis(analysis fileoutline.Result) ([]SourceSymbol, error) {
	if err := analysis.DefinitionError(); err != nil {
		return nil, err
	}
	spans := append([]repomap.DefinitionSpan(nil), analysis.Definitions...)
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].StartRow != spans[j].StartRow {
			return spans[i].StartRow < spans[j].StartRow
		}
		return spans[i].StartCol < spans[j].StartCol
	})
	out := make([]SourceSymbol, 0, len(spans))
	for _, span := range spans {
		name := strings.TrimSpace(span.Name)
		kind, known := sourceSymbolKind(span.Kind)
		if name == "" || !known {
			continue
		}
		out = append(out, SourceSymbol{
			Name: name,
			Kind: kind,
			Line: span.StartRow + 1,
		})
	}
	if len(out) == 0 {
		for _, symbol := range analysis.Symbols {
			name := strings.TrimSpace(symbol.Name)
			kind, known := sourceSymbolKind(symbol.Kind)
			if name == "" || !known || symbol.Line < 1 {
				continue
			}
			out = append(out, SourceSymbol{Name: name, Kind: kind, Line: symbol.Line})
		}
	}
	return out, nil
}

// Symbol categories are coarser than parser definition kinds.
func sourceSymbolKind(kind string) (SourceSymbolKind, bool) {
	switch strings.TrimSpace(kind) {
	case "function":
		return SourceSymbolKindFunction, true
	case "method", "constructor":
		return SourceSymbolKindMethod, true
	case "class", "object":
		return SourceSymbolKindClass, true
	case "type", "interface", "module", "tag", "struct":
		return SourceSymbolKindType, true
	case "constant", "const", "variable", "field", "label", "number", "target":
		return SourceSymbolKindConstant, true
	case "section":
		return SourceSymbolKindHeading, true
	default:
		return "", false
	}
}
