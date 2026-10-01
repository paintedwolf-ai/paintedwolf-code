package toolapi

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func securityScannersOff(secStore *settings.SecurityScannersStore) bool {
	return secStore != nil && !secStore.Effective().Enabled
}

func securityDisabledReject(rejectFmt *guidance.StaticRejectFormatter) error {
	return scanbase.FormatPackReject(&scanbase.PackReject{
		Code: "SECURITY_DISABLED",
		Data: map[string]any{
			"detail": settings.SecurityScannersOffDetail(),
		},
	}, rejectFmt)
}

// RegisterScanTools registers scan_pack and drill-down tools. full is what
// scan_pack asks for a whole-tree pass; a path-scoped pack still enqueues
// directly.
func RegisterScanTools(reg *tools.DefaultRegistry, coord scanbase.ScanCoordinator, scannerReg scanbase.CodeScannerRegistry, full scanbase.FullScanRequester, rejectFmt *guidance.StaticRejectFormatter, secStore *settings.SecurityScannersStore) error {
	if reg == nil || coord == nil || scannerReg == nil || full == nil {
		return fmt.Errorf("registry, coordinator, scan registry, and full scan requester required")
	}
	if err := reg.Register("scan_pack", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return runScanPack(ctx, args, tctx, coord, scannerReg, full, rejectFmt, secStore)
	}); err != nil {
		return err
	}
	if err := reg.Register(toolScanList, func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if securityScannersOff(secStore) {
			return "", securityDisabledReject(rejectFmt)
		}
		return runScanList(ctx, args, tctx, coord)
	}); err != nil {
		return err
	}
	if err := reg.Register(toolScanSummary, func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if securityScannersOff(secStore) {
			return "", securityDisabledReject(rejectFmt)
		}
		return runScanSummary(ctx, args, tctx, coord, rejectFmt)
	}); err != nil {
		return err
	}
	if err := reg.Register(toolScanQuery, func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if securityScannersOff(secStore) {
			return "", securityDisabledReject(rejectFmt)
		}
		return runScanQuery(ctx, args, tctx, coord, rejectFmt)
	}); err != nil {
		return err
	}
	if err := reg.Register(toolScanCompare, func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		if securityScannersOff(secStore) {
			return "", securityDisabledReject(rejectFmt)
		}
		return runScanCompare(ctx, args, tctx, coord, rejectFmt)
	}); err != nil {
		return err
	}
	return nil
}

func packCategoryHintList(reg scanbase.CodeScannerRegistry) string {
	cats := RegistryPackCategories(reg)
	cats = append(cats, scanbase.CategoryAll)
	sort.Strings(cats)
	return strings.Join(cats, ", ")
}

func categoryStrings(cats []api.ScanCategory) string {
	parts := make([]string, len(cats))
	for i, c := range cats {
		parts[i] = string(c)
	}
	return strings.Join(parts, ", ")
}
