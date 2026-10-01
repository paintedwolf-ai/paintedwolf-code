package bundledhint

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/guidance"
)

var (
	hintsOnce sync.Once
	hintsCfg  *guidance.HintConfig
)

func bundledHints() *guidance.HintConfig {
	hintsOnce.Do(func() {
		// Stock hints ship in the binary, so there is nothing to locate first.
		hintsCfg, _ = guidance.LoadHintConfigStock()
	})
	return hintsCfg
}

// Message renders a registered banner hint for tool JSON envelope fields.
func Message(ctx context.Context, code string, data map[string]any) string {
	return guidance.EnvelopeHintMessage(ctx, bundledHints(), code, data)
}
