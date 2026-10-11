package toolapi

import (
	"context"
	"strconv"
	"strings"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func scanDisplaySubject(ctx context.Context, tctx tools.ToolContext, coord scanbase.ScanCoordinator, ids []string) string {
	if tctx.Effects.Out == nil {
		return ""
	}
	canonical, err := scanbase.CanonicalPath(tctx.ActiveRootPath())
	if err != nil {
		return ""
	}
	labels := make([]string, 0, len(ids))
	for _, id := range ids {
		rec, err := coord.Summary(ctx, id)
		if err != nil || rec == nil || rec.CanonicalPath != canonical {
			return ""
		}
		labels = append(labels, scanLabel(rec))
	}
	if len(labels) > 1 {
		return strconv.Itoa(len(labels)) + " scans · " + strings.Join(labels, "; ")
	}
	return strings.Join(labels, "; ")
}

func scanLabel(rec *api.CodeScan) string {
	label := categoryStrings(rec.Categories)
	if label == "" {
		label = "Scan"
	}
	if rec.StartedAt != nil {
		label += " · " + rec.StartedAt.UTC().Format(time.RFC3339)
	}
	return label
}

func captureFullPassSubject(ctx context.Context, tctx tools.ToolContext, coord scanbase.ScanCoordinator, id string) {
	if tctx.Effects.Out == nil {
		return
	}
	store := scanbase.StoreFromCoordinator(coord)
	if store == nil {
		return
	}
	pass, err := store.FullPass(ctx, id)
	canonical, pathErr := scanbase.CanonicalPath(tctx.ActiveRootPath())
	if err != nil || pathErr != nil || pass == nil || pass.CanonicalPath != canonical {
		return
	}
	tctx.SetDisplaySubject("Full scan · " + pass.RequestedAt.UTC().Format(time.RFC3339))
}
