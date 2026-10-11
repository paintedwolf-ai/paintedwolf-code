package toolapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"

	"github.com/lycaon/lycaon/internal/guidance"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/tools"
)

const toolScanCompare = "scan_compare"

// FormatCompareReject maps CompareReject to a structured reject block for the model.
func FormatCompareReject(err error, formatter *guidance.StaticRejectFormatter) error {
	if err == nil {
		return nil
	}
	var reject *scanbase.CompareReject
	if !errors.As(err, &reject) || reject == nil || reject.Code == "" {
		return err
	}
	return toolrejection.FormatDecisionReject(reject.Code, reject.Data, formatter)
}

func runScanCompare(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
	coord scanbase.ScanCoordinator,
	rejectFmt *guidance.StaticRejectFormatter,
) (string, error) {
	projectDir := strings.TrimSpace(tctx.ActiveRootPath())
	if projectDir == "" {
		return "", fmt.Errorf("session has no project_dir")
	}
	oldScanID := strings.TrimSpace(drilldownStringArg(args, "old_scan_id"))
	newScanID := strings.TrimSpace(drilldownStringArg(args, "new_scan_id"))
	if newScanID == "" {
		return "", fmt.Errorf("new_scan_id is required")
	}
	resolvedNew, err := scanbase.ResolveProjectScanID(ctx, coord, projectDir, newScanID)
	if err != nil {
		return "", err
	}
	if resolvedNew == "" {
		return "", FormatCompareReject(&scanbase.CompareReject{Code: scanbase.CompareRejectNewNotFound}, rejectFmt)
	}
	if err := assertScanInSessionProject(ctx, coord, projectDir, resolvedNew, scanbase.CompareRejectNewNotFound); err != nil {
		return "", FormatCompareReject(err, rejectFmt)
	}
	if oldScanID != "" {
		resolvedOldExplicit, err := scanbase.ResolveProjectScanID(ctx, coord, projectDir, oldScanID)
		if err != nil {
			return "", err
		}
		if resolvedOldExplicit == "" {
			return "", FormatCompareReject(&scanbase.CompareReject{Code: scanbase.CompareRejectOldNotFound}, rejectFmt)
		}
		oldScanID = resolvedOldExplicit
	}
	resolvedOld, err := scanbase.ResolveCompareBaseline(ctx, coord, oldScanID, resolvedNew)
	if err != nil {
		return "", FormatCompareReject(err, rejectFmt)
	}
	if err := assertScanInSessionProject(ctx, coord, projectDir, resolvedOld, scanbase.CompareRejectOldNotFound); err != nil {
		return "", FormatCompareReject(err, rejectFmt)
	}
	resp, err := coord.Compare(ctx, resolvedOld, resolvedNew)
	if err != nil {
		return "", FormatCompareReject(err, rejectFmt)
	}
	left := scanDisplaySubject(ctx, tctx, coord, []string{resolvedOld})
	right := scanDisplaySubject(ctx, tctx, coord, []string{resolvedNew})
	if left != "" && right != "" {
		tctx.SetDisplaySubject(left + " → " + right)
	}
	return marshalDrilldownJSON(resp)
}

func assertScanInSessionProject(ctx context.Context, coord scanbase.ScanCoordinator, sessionDir, scanID, notFoundCode string) error {
	canonical, err := scanbase.CanonicalPath(sessionDir)
	if err != nil {
		return err
	}
	rec, err := coord.Get(ctx, scanID)
	if err != nil {
		return err
	}
	if rec == nil {
		return &scanbase.CompareReject{Code: notFoundCode}
	}
	if rec.CanonicalPath != canonical {
		return &scanbase.CompareReject{Code: scanbase.CompareRejectProjectMismatch}
	}
	return nil
}
