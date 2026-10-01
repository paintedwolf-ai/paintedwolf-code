package standingpatterns

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/structrewrite"
)

var patternSmokeSource = map[string][]byte{
	"go":         []byte("package main\n\nfunc main() {}\n"),
	"javascript": []byte("function main() {}\n"),
	"typescript": []byte("function main(): void {}\n"),
	"tsx":        []byte("function main(): void {}\n"),
	"python":     []byte("def main():\n    pass\n"),
	"rust":       []byte("fn main() {}\n"),
}

func validatePatternCompile(ctx context.Context, pattern string, langs []string) error {
	tryLangs := langs
	if len(tryLangs) == 0 {
		tryLangs = []string{"go"}
	}
	var lastErr error
	ok := false
	for _, lang := range tryLangs {
		src, smokeOK := patternSmokeSource[lang]
		if !smokeOK {
			if _, supported := structrewrite.SupportedLanguage(lang, ""); !supported {
				lastErr = fmt.Errorf("unknown grammar %q", lang)
				continue
			}
			src = []byte("\n")
		}
		if _, err := structrewrite.Run(ctx, structrewrite.Request{
			LangName: lang,
			Source:   src,
			Pattern:  pattern,
		}); err != nil {
			lastErr = err
			continue
		}
		ok = true
	}
	if !ok {
		if lastErr != nil {
			return fmt.Errorf("pattern invalid: %w", lastErr)
		}
		return fmt.Errorf("pattern invalid for declared langs")
	}
	return nil
}

func langAllowed(lang string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	for _, a := range allowed {
		if a == lang {
			return true
		}
	}
	return false
}
