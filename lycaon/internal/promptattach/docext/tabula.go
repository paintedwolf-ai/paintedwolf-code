package docext

import (
	"context"

	"github.com/lycaon/lycaon/internal/promptattach/docformat"
)

func extractTabula(ctx context.Context, format docformat.Format, req boundedRequest) (Result, error) {
	if format != docformat.PDF {
		if _, err := checkZipExpansion(req.Bytes, req.Bounds); err != nil {
			return Result{}, err
		}
	}
	out, err := extractInWorker(ctx, format, req)
	if err != nil {
		return Result{}, err
	}
	return out, nil
}
