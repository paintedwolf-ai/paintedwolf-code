package worker

import (
	"strings"

	"github.com/lycaon/lycaon/internal/observability/cmddiag"
)

const weaMaxDiagPaths = 64

// observeCommittedPromotionWEA emits a bounded WEA worker-landing observation
// after CommitPromotion succeeds. It reads the settled changed-path proof from
// the owning task record without falling back to landed paths. If the proof is
// absent or exceeds the bounded capture limit, it marks Incomplete/Truncated
// explicitly so missing matches are reported as incomplete rather than false
// contradictions. Zero overhead when diagnostics are disabled.
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

	// Read settled changed-path proof from the owning worker record ONLY.
	// Never fall back to in.landed (which would self-confirm).
	rawChanged := task.TouchedPaths
	if len(rawChanged) == 0 {
		rawChanged = task.Files
	}
	proofIncomplete := len(rawChanged) == 0
	truncated := len(rawChanged) > weaMaxDiagPaths || len(in.landed) > weaMaxDiagPaths

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
		Truncated:    truncated,
		WorkerResults: []cmddiag.WEAWorkerResultFact{
			{
				ResultHandle: handle,
				WorkerID:     workerID,
				JobID:        in.jobID,
				SessionID:    sessionID,
				Settled:      true,
				Incomplete:   proofIncomplete,
				Truncated:    truncated,
				ChangedPaths: changedPaths,
				SourceRef:    cmddiag.DigestReference("worker_result", handle),
			},
		},
		LandedPaths: landedFacts,
	}
	return cmddiag.ObserveWEA(obs)
}
