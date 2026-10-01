package repomap

import (
	"context"

	"github.com/lycaon/lycaon/internal/bundledhint"
)

func envelopeHint(ctx context.Context, code string) string {
	return bundledhint.Message(ctx, code, nil)
}
