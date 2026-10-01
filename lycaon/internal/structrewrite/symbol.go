package structrewrite

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/repomap"
)

// ExtractSymbol includes decorators and export wrappers in definition spans.
// Unsupported grammars and absent definitions return no matches.
func ExtractSymbol(ctx context.Context, langName, filename string, src []byte, ref SymbolRef) (matches []Match, err error) {
	defer func() {
		if r := recover(); r != nil {
			matches, err = nil, sourceAnalysisRecovered("extract symbol", langName, len(src), r)
		}
	}()
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		return nil, fmt.Errorf("symbol name required")
	}
	if _, ok := SupportedLanguage(langName, filename); !ok {
		return nil, nil
	}
	spans, _, supported, err := repomap.DefinitionSpans(ctx, langName, filename, src)
	if err != nil {
		return nil, err
	}
	if !supported {
		return nil, nil
	}
	spans, err = expandDefinitionSpans(ctx, langName, filename, src, spans)
	if err != nil {
		return nil, err
	}
	kind := strings.TrimSpace(ref.Kind)
	matches = collectNamedDefinitions(spans, src, name, kind)
	if len(matches) == 0 {
		if i := strings.LastIndex(name, "."); i >= 0 && i+1 < len(name) {
			return ExtractSymbol(ctx, langName, filename, src, SymbolRef{Name: name[i+1:], Kind: kind})
		}
		return nil, nil
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].StartRow != matches[j].StartRow {
			return matches[i].StartRow < matches[j].StartRow
		}
		return matches[i].StartCol < matches[j].StartCol
	})
	return matches, nil
}

func collectNamedDefinitions(spans []repomap.DefinitionSpan, src []byte, name, kind string) []Match {
	var matches []Match
	for _, sp := range spans {
		if sp.Name != name {
			continue
		}
		if kind != "" && sp.Kind != kind {
			continue
		}
		matches = append(matches, definitionSpanToMatch(sp, src))
	}
	return matches
}

// ListDefinitionNames returns unique tagged definition names in source order,
// capped so a reject payload stays small.
func ListDefinitionNames(ctx context.Context, langName, filename string, src []byte) ([]string, error) {
	const capNames = 24
	spans, _, ok, err := repomap.DefinitionSpans(ctx, langName, filename, src)
	if err != nil || !ok {
		return nil, err
	}
	seen := make(map[string]bool, len(spans))
	out := make([]string, 0, min(len(spans), capNames))
	for _, sp := range spans {
		name := strings.TrimSpace(sp.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
		if len(out) == capNames {
			break
		}
	}
	return out, nil
}

func definitionSpanToMatch(sp repomap.DefinitionSpan, src []byte) Match {
	start := sp.StartByte
	end := sp.EndByte
	if start < 0 {
		start = 0
	}
	if end > len(src) {
		end = len(src)
	}
	if start > end {
		start, end = end, start
	}
	return Match{
		StartByte: start,
		EndByte:   end,
		StartRow:  sp.StartRow,
		StartCol:  sp.StartCol,
		EndRow:    sp.EndRow,
		EndCol:    sp.EndCol,
		Text:      string(src[start:end]),
		Bindings:  map[string]string{"kind": sp.Kind},
	}
}
