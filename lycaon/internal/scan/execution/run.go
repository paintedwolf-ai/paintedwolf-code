package execution

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/repochange"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/pkg/api"
)

func (r *Runner) execute(ctx context.Context, job *api.CodeScan) {
	if job == nil {
		return
	}
	if job.ClaimToken == "" {
		r.fail(ctx, job, fmt.Errorf("scan %s claimed without a lease token", job.ID))
		return
	}
	claimCtx, stopHeartbeat := r.scanClaimContext(ctx, job)
	defer stopHeartbeat()
	ctx = claimCtx
	if r.Settings != nil && !r.Settings.Effective().Enabled {
		r.cancel(ctx, job, scannersDisabledReason)
		return
	}
	livePaths, err := r.Store.LoadPaths(ctx, job.ID)
	if err != nil {
		r.failWithCode(ctx, job, scanbase.FailurePathsUnavailable, err)
		return
	}
	contract, err := scanbase.SelectedScannerContract(ctx, r.Registry, job.CanonicalPath, job.ScannerID, job.Categories...)
	if err != nil {
		r.failWithCode(ctx, job, scanbase.FailureDefinitionChanged, err)
		return
	}
	_, currentFingerprint, err := scancatalog.ExecutionManifest(contract)
	strictManifest := job.ExecutionManifest != nil && job.ExecutionManifest.DefinitionFingerprint != ""
	if err != nil || (strictManifest && (job.ExecutionFingerprint == "" || currentFingerprint != job.ExecutionFingerprint)) {
		if err == nil {
			err = fmt.Errorf("captured scanner definition no longer matches selected definition")
		}
		r.failWithCode(ctx, job, scanbase.FailureDefinitionChanged, err)
		return
	}
	policy := contract.Runtime.Normalized()
	// Admission follows the scanned source and its current epoch.
	release, err := r.Broker.Acquire(ctx, backgroundwork.Request{
		Key:       scanAdmissionKey(job),
		Epoch:     repochange.CurrentEpoch(job.CanonicalPath).Value,
		Lane:      job.CanonicalPath,
		Priority:  scanWorkPriority(job.Trigger),
		Resources: []backgroundwork.Resource{backgroundwork.ResourceCPU},
		Units:     map[backgroundwork.Resource]int{backgroundwork.ResourceCPU: policy.CPUUnits},
	})
	if errors.Is(err, backgroundwork.ErrSuperseded) {
		r.cancel(ctx, job, "superseded by a newer scan generation")
		return
	}
	if err != nil {
		r.fail(ctx, job, err)
		return
	}
	defer release()
	startedAt := time.Now().UTC()
	started, err := r.Store.SetExecutionPolicy(ctx, job, policy, startedAt)
	if err != nil {
		r.fail(ctx, job, err)
		return
	}
	if !started {
		return
	}
	job.Runtime = runtimePolicyAPI(policy)
	job.StartedAt = &startedAt
	source, ok := r.resolveScanSource(ctx, job, contract, livePaths)
	if !ok {
		return
	}
	req := scanbase.ScanRequest{
		ProjectDir:  source.ProjectDir,
		Categories:  job.Categories,
		ScannerID:   job.ScannerID,
		Paths:       source.Paths,
		FileTimeout: r.FileTimeout,
	}
	scanCtx, stopScan := r.scannerContext(ctx, job, policy)
	defer stopScan()
	var result *scanoutput.Result
	engineRan := false
	if job.TargetKind == api.ScanTargetPaths && len(livePaths) == 0 && len(job.DeletedPaths) > 0 {
		result = &scanoutput.Result{Categories: append([]api.ScanCategory(nil), job.Categories...)}
	} else {
		result, err = r.runInChunks(scanCtx, job, req)
		engineRan = true
	}
	r.reconcileSoftRuntime(ctx, job, policy, startedAt)
	if ctx.Err() != nil || errors.Is(scanCtx.Err(), context.Canceled) {
		reason := "scan interrupted"
		if preempted := r.preemptionReason(job.ID); preempted != "" {
			reason = preempted
		}
		if r.Settings != nil && !r.Settings.Effective().Enabled {
			reason = scannersDisabledReason
		}
		r.cancel(ctx, job, reason)
		return
	}
	if errors.Is(scanCtx.Err(), context.DeadlineExceeded) {
		r.timeout(ctx, job, policy)
		return
	}
	if err != nil {
		if r.Settings != nil && !r.Settings.Effective().Enabled {
			r.cancel(ctx, job, scannersDisabledReason)
			return
		}
		r.failWithCode(ctx, job, scanbase.FailureEngine, err)
		return
	}
	if r.Settings != nil && !r.Settings.Effective().Enabled {
		r.cancel(ctx, job, scannersDisabledReason)
		return
	}
	scanoutput.NormalizeResultPaths(result, source.ProjectDir)
	if engineRan {
		// The engine read the live tree; what moved under it is named.
		r.fenceMovedSource(ctx, job, source, result)
	}
	if engineRan {
		r.cacheCurrentFindings(scanCtx, job, contract, result)
	}
	coverage := scanoutput.CoverageForResult(result, job.SourceCaptureQuality, job.SourceAdmissionMode)
	r.compareWithBase(scanCtx, job, contract, result)
	if !r.ingestScanResult(ctx, job, result, contract) {
		return
	}
	ingested, err := r.Store.Get(ctx, job.ID)
	if err != nil || ingested == nil {
		if err == nil {
			err = fmt.Errorf("scan disappeared before terminal commit")
		}
		r.failWithCode(ctx, job, scanbase.FailureCommit, err)
		return
	}
	won, err := r.markCompleteWithAuthority(ctx, job, result, ingested.Findings, result.Warnings, coverage)
	if err != nil {
		r.failWithCode(ctx, job, scanbase.FailureCommit, err)
		return
	}
	if !won {
		// Another terminal writer settled the scan.
		return
	}
	// The committed transition can retire the lease context immediately.
	// Finish history and notification with the bounded terminal lifetime.
	ctx, stopTerminal := terminalCallbackContext(ctx)
	defer stopTerminal()
	updated, _ := r.Store.Get(ctx, job.ID)
	if updated == nil {
		job.Status = api.CodeScanStatusComplete
		if result != nil {
			job.FindingsCount = result.FindingsCount
		}
		updated = job
	}
	r.recordFindingHistory(ctx, updated)
	r.notifyTerminal(ctx, updated)
}

func runtimePolicyAPI(policy scancatalog.RuntimePolicy) *api.ScanRuntimePolicy {
	policy = policy.Normalized()
	return &api.ScanRuntimePolicy{
		SoftLimitMs: policy.SoftLimitSec * 1000, HardLimitMs: policy.HardLimitSec * 1000,
		CPUUnits: policy.CPUUnits, Parallelism: policy.Parallelism,
	}
}

// scanAdmissionKey groups source-equivalent work across epochs.
func scanAdmissionKey(job *api.CodeScan) string {
	categories := make([]string, 0, len(job.Categories))
	for _, category := range job.Categories {
		categories = append(categories, string(category))
	}
	sort.Strings(categories)
	return "scan\x00" + job.CanonicalPath + "\x00" + job.ScannerID + "\x00" + strings.Join(categories, ",")
}

func scanWorkPriority(trigger api.ScanTrigger) backgroundwork.Priority {
	switch trigger {
	case api.ScanTriggerWriteBurst, api.ScanTriggerAuthorityRefresh:
		return backgroundwork.PriorityProactive
	default:
		return backgroundwork.PriorityIntegrity
	}
}
