package repomap

import (
	"context"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/odvcencio/gotreesitter/grammars"
)

var (
	ErrDefinitionIncomplete  = errors.New("definition analysis stopped before parsing completed")
	ErrDefinitionUnavailable = errors.New("definition analysis is unavailable")
)

// DefinitionSpan is a tagged definition with its full CST source span.
type DefinitionSpan struct {
	Kind      string
	Name      string
	StartByte int
	EndByte   int
	StartRow  int // 0-based
	StartCol  int
	EndRow    int
	EndCol    int
}

// DefinitionSpans returns parser definition spans.
func DefinitionSpans(ctx context.Context, langName, filename string, src []byte) (spans []DefinitionSpan, language string, supported bool, err error) {
	entry := detectLangEntry(langName, filename)
	if entry == nil {
		return nil, "", false, nil
	}
	language = entry.Name
	cache := &taggerCache{}
	parsed := cache.tag(ctx, *entry, src)
	spans, err = parsed.definitions()
	return spans, language, true, err
}

func (parsed tagParseResult) definitions() ([]DefinitionSpan, error) {
	if parsed.failure != nil {
		if parsed.failure.Incomplete() {
			return nil, errors.Join(ErrDefinitionIncomplete, parsed.failure)
		}
		return nil, errors.Join(ErrDefinitionUnavailable, parsed.failure)
	}
	if parsed.noTagger {
		return nil, ErrDefinitionUnavailable
	}
	return definitionSpans(parsed.tags), nil
}

func detectLangEntry(langName, filename string) *grammars.LangEntry {
	if n := strings.TrimSpace(langName); n != "" {
		return grammars.DetectLanguageByName(n)
	}
	if filename != "" {
		return filekind.GrammarForPath(filename)
	}
	return nil
}
