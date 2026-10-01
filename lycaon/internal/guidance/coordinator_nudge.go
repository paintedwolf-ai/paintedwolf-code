package guidance

import (
	"context"
	"fmt"
	"strings"
)

// FormatCoordinatorNudge renders a structured coordinator nudge.
func FormatCoordinatorNudge(ctx context.Context, f *StaticRejectFormatter, code string, data map[string]any) (string, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return "", fmt.Errorf("guidance: empty nudge code")
	}
	if f == nil {
		return "", fmt.Errorf("guidance: reject formatter required for %q", code)
	}
	entry, ok := f.lookup(code)
	if !ok {
		return "", fmt.Errorf("unknown guidance code %q", code)
	}
	if err := entryCoversTool(code, entry, data); err != nil {
		return "", err
	}
	entry.Effect = "nudge"
	return RenderUnifiedRejectBlock(ctx, code, entry, data)
}
