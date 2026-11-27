package worker

import (
	"strings"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

const weaMaxDiagPaths = 64

// observeCommittedPromotionWEA emits a bounded WEA worker-landing observation
// after CommitPromotion succeeds. When diagnostics are disabled, it returns
// immediately with zero allocations.
func observeCommittedPromotionWEA(in promoteFinishInput) bool {
	if !cmddiag.Enabled() || in.task == nil {
		return false
	}
	task := in.task
	handle := strings.TrimSpace(task.ResultHandle)
	if handle == "" {
		handle = strings.TrimSpace(in.jobID)
	}
	workerID := strings.TrimSpace(task.WorkerName)
	if workerID == "" {
		workerID = strings.TrimSpace(task.WorkerRecordID)
	}
	sessionID := strings.TrimSpace(in.sessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(task.ParentSessionID)
	}

	// Collect bounded committed result changed paths.
	// If the worker task recorded explicit TouchedPaths, use those as the
	// authoritative committed path set; otherwise fall back to Files or in.landed
	// so partial/stub tasks do not emit false-positive warnings.
	rawChanged := task.TouchedPaths
	if len(rawChanged) == 0 {
		rawChanged = task.Files
	}
	if len(rawChanged) == 0 {
		rawChanged = in.landed
	}
	changedLimit := min(len(rawChanged), weaMaxDiagPaths)
	changedPaths := make([]string, 0, changedLimit)
	for i := 0; i < changedLimit; i++ {
		if p := strings.TrimSpace(rawChanged[i]); p != "" {
			changedPaths = append(changedPaths, p)
		}
	}

	landedLimit := min(len(in.landed), weaMaxDiagPaths)
	landedFacts := make([]cmddiag.WEALandedPathFact, 0, landedLimit)
	for i := 0; i < landedLimit; i++ {
		p := strings.TrimSpace(in.landed[i])
		if p == "" {
			continue
		}
		landedFacts = append(landedFacts, cmddiag.WEALandedPathFact{
			ResultHandle: handle,
			WorkerID:     workerID,
			JobID:        in.jobID,
			Path:         p,
			Settled:      true,
			SourceRef:    cmddiag.DigestReference("promotion", in.jobID, p),
		})
	}

	obs := cmddiag.WEAObservation{
		SessionID:    sessionID,
		ProjectID:    strings.TrimSpace(task.ProjectID),
		WorkerID:     workerID,
		JobID:        in.jobID,
		ResultHandle: handle,
		Settled:      true,
		WorkerResults: []cmddiag.WEAWorkerResultFact{
			{
				ResultHandle: handle,
				WorkerID:     workerID,
				JobID:        in.jobID,
				SessionID:    sessionID,
				Settled:      true,
				ChangedPaths: changedPaths,
				SourceRef:    cmddiag.DigestReference("worker_result", handle),
			},
		},
		LandedPaths: landedFacts,
	}
	return cmddiag.ObserveWEA(obs)
}
