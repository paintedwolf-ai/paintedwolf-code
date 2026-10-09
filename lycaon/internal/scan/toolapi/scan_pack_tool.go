package toolapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/guidance"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	defaultScanPackWait = 10 * time.Minute
	maxScanPackWait     = 30 * time.Minute
)

func runScanPack(
	ctx context.Context,
	args map[string]any,
	tctx tools.ToolContext,
	coord scanbase.ScanCoordinator,
	scannerReg scanbase.CodeScannerRegistry,
	full scanbase.FullScanRequester,
	rejectFmt *guidance.StaticRejectFormatter,
	secStore *settings.SecurityScannersStore,
) (string, error) {
	if securityScannersOff(secStore) {
		return "", securityDisabledReject(rejectFmt)
	}
	// Root resolution owns the structured no-root rejection.
	projectDir, err := resolveScanRoot(ctx, tctx)
	if err != nil {
		return "", err
	}
	categories, err := scanbase.ParseScanCategoryArgs(args["categories"])
	if err != nil {
		return "", scanbase.FormatPackReject(&scanbase.PackReject{
			Code: "SCAN_PACK_CATEGORY_INVALID",
			Data: map[string]any{
				"detail":           err.Error(),
				"valid_categories": packCategoryHintList(scannerReg),
			},
		}, rejectFmt)
	}
	if len(scannerReg.List(categories...)) == 0 {
		return "", scanbase.FormatPackReject(&scanbase.PackReject{
			Code: "SCAN_PACK_NO_HOST_ENGINE",
			Data: map[string]any{
				"categories":       categoryStrings(categories),
				"project_dir":      projectDir,
				"engines":          strings.Join(RegistryScannerIDs(scannerReg), ", "),
				"scan_no_engines":  len(RegistryScannerIDs(scannerReg)) == 0,
				"valid_categories": packCategoryHintList(scannerReg),
			},
		}, rejectFmt)
	}
	paths, err := resolveScanPaths(ctx, args["paths"], tctx)
	if err != nil {
		return "", err
	}
	completion, _ := args["completion"].(string)
	waits := strings.TrimSpace(completion) == "summary"
	sessionID := strings.TrimSpace(tctx.Identity.SessionID)
	if len(paths) > 0 {
		ids, err := enqueuePathScanPack(ctx, coord, scannerReg, projectDir, categories, paths, sessionID)
		if err != nil {
			return "", err
		}
		if len(ids) > 0 && tctx.Effects.Out != nil {
			tctx.Effects.Out.OwnerRef = ids[0]
		}
		if !waits {
			return scanPackReceipt(ctx, coord, ids, ReceiptOptions{})
		}
		return waitForScanPackReceipt(ctx, coord, ids, scanPackWaitDuration(args["timeout_ms"]))
	}
	pass, err := requestScanPackPass(ctx, scannerReg, full, projectDir, categories, sessionID)
	if err != nil {
		return "", err
	}
	if tctx.Effects.Out != nil {
		tctx.Effects.Out.OwnerRef = pass.ID
	}
	if !waits {
		return FullPassReceipt(ctx, coord, pass, ReceiptOptions{})
	}
	return waitForFullPassReceipt(ctx, coord, pass, scanPackWaitDuration(args["timeout_ms"]))
}

func waitForFullPassReceipt(ctx context.Context, coord scanbase.ScanCoordinator, pass scanbase.FullPass, timeout time.Duration) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	latest, waitErr := waitForFullPass(waitCtx, coord, pass.ID)
	cancel()
	if waitErr == nil {
		return FullPassReceipt(ctx, coord, latest, ReceiptOptions{})
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, context.Canceled) {
		if latest.ID == "" {
			latest = pass
		}
		return FullPassReceipt(ctx, coord, latest, ReceiptOptions{waitTimedOut: true})
	}
	return "", waitErr
}

func waitForScanPackReceipt(ctx context.Context, coord scanbase.ScanCoordinator, ids []string, timeout time.Duration) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	completedIDs, waitErr := waitForScanPack(waitCtx, coord, ids)
	cancel()
	if len(completedIDs) > 0 {
		ids = completedIDs
	}
	if waitErr == nil {
		return scanPackReceipt(ctx, coord, ids, ReceiptOptions{})
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, context.Canceled) {
		return scanPackReceipt(ctx, coord, ids, ReceiptOptions{waitTimedOut: true})
	}
	return "", waitErr
}

func scanPackWaitDuration(raw any) time.Duration {
	milliseconds := int64(defaultScanPackWait / time.Millisecond)
	switch value := raw.(type) {
	case int:
		milliseconds = int64(value)
	case int64:
		milliseconds = value
	case float64:
		milliseconds = int64(value)
	}
	if milliseconds <= 0 {
		return defaultScanPackWait
	}
	duration := time.Duration(milliseconds) * time.Millisecond
	if duration > maxScanPackWait {
		return maxScanPackWait
	}
	return duration
}

// enqueuePathScanPack creates ad hoc scans of explicit paths.
func enqueuePathScanPack(ctx context.Context, coord scanbase.ScanCoordinator, scannerReg scanbase.CodeScannerRegistry, projectDir string, categories []api.ScanCategory, paths []string, sessionID string) ([]string, error) {
	records, err := scanbase.EnqueueMatchingScanners(ctx, coord, scannerReg, scanbase.EnqueueRequest{
		ProjectDir: projectDir, Categories: categories, Paths: paths,
		SessionID: sessionID, Trigger: api.ScanTriggerScanPack,
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return ids, nil
}

// requestScanPackPass asks the cadence for a full pass by every selected scanner of the categories.
func requestScanPackPass(ctx context.Context, scannerReg scanbase.CodeScannerRegistry, full scanbase.FullScanRequester, projectDir string, categories []api.ScanCategory, sessionID string) (scanbase.FullPass, error) {
	scanners := scanbase.ListSelectedScanners(ctx, scannerReg, projectDir, categories...)
	if len(scanners) == 0 {
		return scanbase.FullPass{}, fmt.Errorf("%w %v", scancatalog.ErrNoScannerForCategories, categories)
	}
	scannerIDs := make([]string, 0, len(scanners))
	for _, meta := range scanners {
		scannerIDs = append(scannerIDs, meta.ID)
	}
	return full.RequestFull(ctx, projectDir, scannerIDs, api.ScanTriggerScanPack, scanbase.FullScanContext{SessionID: sessionID})
}
