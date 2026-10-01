package execution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/events"
	"github.com/lycaon/lycaon/internal/project"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

func (r *Runner) cancel(ctx context.Context, job *api.CodeScan, reason string) {
	if r == nil || r.Store == nil || job == nil {
		return
	}
	ctx, stop := terminalCallbackContext(ctx)
	defer stop()
	won, err := r.Store.FinalizeFailure(ctx, job, api.CodeScanStatusCanceled, "SCAN_CANCELED", reason)
	if err != nil {
		slog.ErrorContext(ctx, "record scan cancellation", "scan_id", job.ID, "error", err)
		return
	}
	if !won {
		return
	}
	job.Status = api.CodeScanStatusCanceled
	job.Error = reason
	job.FailureCode = "SCAN_CANCELED"
	job.CoverageStatus = api.ScanCoverageUnavailable
	now := time.Now().UTC()
	job.CompletedAt = &now
	r.notifyTerminal(ctx, job)
}

func (r *Runner) markCompleteWithAuthority(ctx context.Context, job *api.CodeScan, result *scanoutput.Result, findings []api.SecurityFinding, warnings []api.ScanWarning, coverage api.ScanCoverageStatus) (bool, error) {
	if r == nil || r.Store == nil {
		return false, errors.New("scan runner: store required")
	}
	spillCap := r.ResultSpillBytes
	if spillCap <= 0 {
		raw, err := surveyjson.Marshal(result)
		if err != nil {
			return false, err
		}
		return r.Store.FinalizeCompleteJSON(ctx, job, raw, findings, warnings, coverage)
	}
	evidenceRoot := r.hostDataDir(ctx, job)
	if evidenceRoot == "" {
		raw, err := surveyjson.Marshal(result)
		if err != nil {
			return false, err
		}
		return r.Store.FinalizeCompleteJSON(ctx, job, raw, findings, warnings, coverage)
	}
	_ = os.MkdirAll(evidenceRoot, 0o700)
	spillDir := filepath.Join(evidenceRoot, scanbase.ScanResultSpillDir)
	raw, err := scanbase.SpillResultJSON(result, spillCap, spillDir, job.ID)
	if err != nil {
		return false, err
	}
	return r.Store.FinalizeCompleteJSON(ctx, job, raw, findings, warnings, coverage)
}

// failTerminal records a terminal failure without retry.
func (r *Runner) failTerminal(ctx context.Context, job *api.CodeScan, cause error) {
	if r == nil || job == nil || cause == nil {
		return
	}
	if ctx.Err() != nil {
		r.cancel(ctx, job, "scan interrupted")
		return
	}
	ctx, stop := terminalCallbackContext(ctx)
	defer stop()
	won, err := r.Store.FinalizeFailure(ctx, job, api.CodeScanStatusFailed, scanbase.FailureSourceUnavailable, cause.Error())
	if err != nil || !won {
		return
	}
	job.Status = api.CodeScanStatusFailed
	job.Error = cause.Error()
	job.FailureCode = scanbase.FailureSourceUnavailable
	job.CoverageStatus = api.ScanCoverageUnavailable
	r.notifyTerminal(ctx, job)
}

func (r *Runner) fail(ctx context.Context, job *api.CodeScan, cause error) {
	r.failWithCode(ctx, job, scanbase.FailureEngine, cause)
}

func (r *Runner) failWithCode(ctx context.Context, job *api.CodeScan, code string, cause error) {
	if ctx.Err() != nil {
		r.cancel(ctx, job, "scan interrupted")
		return
	}
	ctx, stop := terminalCallbackContext(ctx)
	defer stop()
	if cause == nil {
		cause = errors.New("scan failed")
	}
	won, err := r.Store.FinalizeFailure(ctx, job, api.CodeScanStatusFailed, code, cause.Error())
	if err != nil {
		slog.ErrorContext(ctx, "record scan failure", "scan_id", job.ID, "error", err)
		return
	}
	if !won {
		// Another terminal writer settled the scan.
		return
	}
	job.Status = api.CodeScanStatusFailed
	job.Error = cause.Error()
	job.FailureCode = code
	job.CoverageStatus = api.ScanCoverageUnavailable
	r.notifyTerminal(ctx, job)
}

func (r *Runner) timeout(ctx context.Context, job *api.CodeScan, policy scancatalog.RuntimePolicy) {
	ctx, stop := terminalCallbackContext(ctx)
	defer stop()
	message := fmt.Sprintf("scanner exceeded hard runtime limit of %s", time.Duration(policy.HardLimitSec)*time.Second)
	won, err := r.Store.FinalizeFailure(ctx, job, api.CodeScanStatusTimedOut, scanbase.FailureTimeout, message)
	if err != nil || !won {
		return
	}
	job.Status = api.CodeScanStatusTimedOut
	job.Error = message
	job.FailureCode = scanbase.FailureTimeout
	job.CoverageStatus = api.ScanCoverageUnavailable
	if updated, getErr := r.Store.Get(ctx, job.ID); getErr == nil && updated != nil {
		r.notifyTerminal(ctx, updated)
		return
	}
	r.notifyTerminal(ctx, job)
}

// hostDataDir uses the project ID when available and a path key otherwise.
func (r *Runner) hostDataDir(ctx context.Context, job *api.CodeScan) string {
	if r == nil || job == nil {
		return ""
	}
	if r.Events != nil {
		if id, ok := events.ResolvedProjectID(ctx, r.Events.Lookup, job.CanonicalPath); ok {
			return project.HostDataDir(r.DataDir, id)
		}
	}
	return project.PathKeyedHostDataDir(r.DataDir, job.CanonicalPath)
}

func (r *Runner) notifyTerminal(ctx context.Context, job *api.CodeScan) {
	if r == nil || r.OnTerminal == nil || job == nil {
		return
	}
	callbackCtx, cancel := terminalCallbackContext(ctx)
	defer cancel()
	r.OnTerminal(callbackCtx, *job)
}

func terminalCallbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), terminalCallbackTimeout)
}
