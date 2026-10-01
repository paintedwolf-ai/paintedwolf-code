package session

import (
	"context"
	"strings"
	"unicode"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
)

const maxSessionTitleRunes = 40

// NameSession derives a short session title from the first user message.
func NameSession(ctx context.Context, namer llm.UtilityNamer, firstUserMessage string) string {
	text := strings.TrimSpace(firstUserMessage)
	if text == "" {
		return ""
	}
	if llm.MockOnlyFromEnv() {
		return trimSessionTitle(text)
	}
	if namer == nil {
		return ""
	}
	system, err := guidance.RenderCatalog(ctx, guidance.UtilitySessionTitleSystemRef, nil)
	if err != nil {
		return ""
	}
	raw, err := namer.Name(ctx, system, text)
	if err != nil || strings.TrimSpace(raw) == "" {
		return ""
	}
	return trimSessionTitle(raw)
}

func trimSessionTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	runes := []rune(s)
	if len(runes) > maxSessionTitleRunes {
		prefix := runes[:maxSessionTitleRunes]
		cut := len(prefix)
		for i := len(prefix) - 1; i >= 0; i-- {
			if unicode.IsSpace(prefix[i]) {
				cut = i
				break
			}
		}
		if cut > 0 {
			s = string(prefix[:cut])
		} else {
			s = string(prefix)
		}
	}
	s = strings.TrimRightFunc(s, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
	return strings.TrimSpace(s)
}
