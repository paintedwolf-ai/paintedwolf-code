package scan

import (
	"context"
	"fmt"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

// scanEnqueue writes host scan rows.
type scanEnqueue interface {
	Enqueue(ctx context.Context, req EnqueueRequest) (*api.CodeScan, error)
}

// EnqueueMatchingScanners enqueues one row per overlapping registry scanner.
// Each row carries the engine's declared categories.
func EnqueueMatchingScanners(ctx context.Context, coord scanEnqueue, reg CodeScannerRegistry, req EnqueueRequest) ([]*api.CodeScan, error) {
	if coord == nil {
		return nil, fmt.Errorf("scan coordinator not configured")
	}
	if reg == nil {
		return nil, fmt.Errorf("scan registry not configured")
	}
	categories, err := ResolveScanCategories(req.Categories)
	if err != nil {
		return nil, err
	}
	candidates := ListSelectedScanners(ctx, reg, req.ProjectDir, categories...)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w %v", scancatalog.ErrNoScannerForCategories, categories)
	}
	if len(candidates) == 1 {
		req.ScannerID = candidates[0].ID
		req.Categories = engineScanCategories(candidates[0])
		rec, err := coord.Enqueue(ctx, req)
		if err != nil {
			return nil, err
		}
		return []*api.CodeScan{rec}, nil
	}
	out := make([]*api.CodeScan, 0, len(candidates))
	for _, meta := range candidates {
		sub := req
		sub.ScannerID = meta.ID
		sub.Categories = engineScanCategories(meta)
		rec, err := coord.Enqueue(ctx, sub)
		if err != nil {
			return nil, fmt.Errorf("enqueue %s: %w", meta.ID, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

func engineScanCategories(meta ScannerMeta) []api.ScanCategory {
	return normalizeCategories(append([]api.ScanCategory(nil), meta.Categories...))
}
