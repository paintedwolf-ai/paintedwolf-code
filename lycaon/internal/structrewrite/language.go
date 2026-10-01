// Package structrewrite matches and rewrites syntax trees using $VAR bindings
// and $$$VAR sequences.
package structrewrite

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// ErrLanguageUnknown identifies an unresolved grammar name or filename.
type ErrLanguageUnknown struct {
	Name     string
	Filename string
}

func (e *ErrLanguageUnknown) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("unknown grammar %q", e.Name)
	}
	return fmt.Sprintf("no grammar for file %q", e.Filename)
}

// An explicit grammar name takes precedence over filename detection.
func resolveLanguage(name, filename string) (*gotreesitter.Language, string, error) {
	entry := detectEntry(name, filename)
	if entry == nil || entry.Language == nil {
		return nil, "", &ErrLanguageUnknown{Name: name, Filename: filename}
	}
	return entry.Language(), entry.Name, nil
}

// SupportedLanguage resolves a grammar name or filename without loading it.
func SupportedLanguage(name, filename string) (canonical string, ok bool) {
	entry := detectEntry(name, filename)
	if entry == nil || entry.Language == nil {
		return "", false
	}
	return entry.Name, true
}

func detectEntry(name, filename string) *grammars.LangEntry {
	if n := strings.TrimSpace(name); n != "" {
		return grammars.DetectLanguageByName(n)
	}
	if filename != "" {
		return filekind.GrammarForPath(filename)
	}
	return nil
}

// expandoChar selects a placeholder for `$` in pattern identifiers.
// It keeps `$` when the grammar accepts it and otherwise uses a letter rune.
func expandoChar(lang string) rune {
	switch lang {
	case "javascript", "jsx", "typescript", "tsx", "flow",
		"dart", "groovy", "solidity":
		return '$'
	case "css", "scss", "less", "nix":
		return '_'
	default:
		return 'µ'
	}
}

// expandoFallbacks lists alternate identifier runes in preference order.
// Collision-prone ASCII fallbacks are last.
var expandoFallbacks = []rune{'µ', '$', '_', 'Q'}

// expandoCandidates lists the runes to try for a grammar, preferred first.
func expandoCandidates(lang string) []rune {
	preferred := expandoChar(lang)
	out := []rune{preferred}
	for _, c := range expandoFallbacks {
		if c != preferred {
			out = append(out, c)
		}
	}
	return out
}
