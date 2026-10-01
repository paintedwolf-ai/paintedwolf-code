package grammargen

import (
	"time"

	"github.com/odvcencio/gotreesitter"
)

func generateDartParityLanguageWithTimeout(gram *Grammar, timeout time.Duration) (*gotreesitter.Language, error) {
	gram.EnableLRSplitting = true
	return generateWithTimeout(gram, timeout)
}
