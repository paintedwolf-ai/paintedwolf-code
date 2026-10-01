package cadence

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

type dispatchGeneration struct {
	canonical       string
	snapshot        sourcesnapshot.Snapshot
	headSHA         string
	executions      map[string]dispatchExecution
	executionErrors map[string]error
	selections      map[string]dispatchSelection
}

// A cohort shares one assessment for a full pass or a generation's deltas.
type dispatchCohort struct {
	passID string
	rows   []scanbase.SeriesRow
}

func dispatchCohorts(claimed []scanbase.SeriesRow) []dispatchCohort {
	order := make([]string, 0, 1)
	byPass := make(map[string][]scanbase.SeriesRow)
	for _, row := range claimed {
		if _, seen := byPass[row.DispatchPassID]; !seen {
			order = append(order, row.DispatchPassID)
		}
		byPass[row.DispatchPassID] = append(byPass[row.DispatchPassID], row)
	}
	out := make([]dispatchCohort, 0, len(order))
	for _, passID := range order {
		out = append(out, dispatchCohort{passID: passID, rows: byPass[passID]})
	}
	return out
}

type cohortAssessment struct {
	id       string
	required []string
	trigger  api.ScanTrigger
	draft    *scanbase.AssessmentDraft
}

func (c *Service) enqueueCohort(ctx context.Context, generation dispatchGeneration, cohort dispatchCohort) ([]*api.CodeScan, []dispatchFailure) {
	assessment, err := c.ensureCohortAssessment(ctx, generation, cohort)
	if err != nil {
		c.logDispatch(ctx, "requeued", generation.canonical, cohort.rows, err)
		if cohort.passID != "" && errors.Is(err, scanbase.ErrAssessmentIdentityMismatch) {
			// Members cannot join a pass that started over another generation.
			for _, row := range cohort.rows {
				c.leavePass(ctx, row)
			}
		} else {
			c.requeueDispatches(ctx, cohort.rows)
		}
		return nil, []dispatchFailure{{Error: err.Error()}}
	}
	if cohort.passID != "" {
		if err := c.Store.MarkFullPassStarted(ctx, cohort.passID, c.now()); err != nil {
			slog.WarnContext(ctx, "mark full pass started", "component", "scan_cadence", "pass_id", cohort.passID, "error", err)
		}
	}
	records := make([]*api.CodeScan, 0, len(cohort.rows))
	failures := make([]dispatchFailure, 0)
	for _, row := range cohort.rows {
		if executionErr := generation.executionErrors[row.ScannerID]; executionErr != nil {
			failures = append(failures, dispatchFailure{ScannerID: row.ScannerID, Error: executionErr.Error()})
			c.releaseUndispatched(ctx, generation.canonical, row, executionErr)
			continue
		}
		execution := generation.executions[row.ScannerID]
		selection := generation.selections[row.ScannerID].target
		// Pass members carry the pass's trigger; a delta keeps what armed its scanner.
		trigger := row.DispatchTrigger
		if cohort.passID != "" {
			trigger = assessment.trigger
		}
		record, enqueueErr := c.Coordinator.Enqueue(ctx, scanbase.EnqueueRequest{
			ProjectDir: generation.canonical, Categories: row.Categories, ScannerID: row.ScannerID,
			Paths: selection.Paths, DeletedPaths: selection.DeletedPaths, TargetKind: selection.Kind,
			HeadSHA: generation.headSHA, SourceSnapshotID: generation.snapshot.ID, Trigger: trigger,
			AssessmentID: assessment.id, RequiredScanners: assessment.required,
			ExecutionManifest: &execution.Manifest, ExecutionFingerprint: execution.Fingerprint,
			BaseSnapshotID: selection.BaseSnapshotID, Assessment: assessment.draft,
		})
		if enqueueErr != nil {
			failures = append(failures, dispatchFailure{ScannerID: row.ScannerID, Error: enqueueErr.Error()})
			c.releaseUndispatched(ctx, generation.canonical, row, enqueueErr)
			continue
		}
		if err := c.finishDispatch(ctx, row, record); err != nil {
			failures = append(failures, dispatchFailure{ScannerID: row.ScannerID, Error: err.Error()})
			continue
		}
		if cohort.passID != "" {
			if err := c.Store.BindFullPassScan(ctx, cohort.passID, record.ID); err != nil {
				slog.WarnContext(ctx, "bind full pass requesters", "component", "scan_cadence",
					"pass_id", cohort.passID, "scan_id", record.ID, "error", err)
			}
		}
		records = append(records, record)
	}
	return records, failures
}

// Full-pass assessments cover every requested scanner; delta cohorts get fresh assessments.
func (c *Service) ensureCohortAssessment(ctx context.Context, generation dispatchGeneration, cohort dispatchCohort) (cohortAssessment, error) {
	concrete, recorded := c.Coordinator.(*scanbase.CoordinatorImpl)
	recorded = recorded && concrete != nil
	if cohort.passID != "" {
		request, err := c.Store.ReadFullPassRequest(ctx, cohort.passID)
		if err != nil {
			return cohortAssessment{}, err
		}
		assessment := cohortAssessment{id: request.ID, required: scanbase.UniqueSortedStrings(request.Scanners), trigger: request.Trigger}
		if !recorded {
			return assessment, nil
		}
		assessment.draft = &scanbase.AssessmentDraft{
			ID: assessment.id, CanonicalPath: generation.canonical, SourceSnapshotID: generation.snapshot.ID,
			RequiredScanners: assessment.required, Target: scanbase.FullTargetSelection(generation.snapshot),
			Trigger: request.Trigger,
		}
		_, err = concrete.Store.EnsureAssessment(ctx, *assessment.draft)
		return assessment, err
	}
	if !recorded {
		return cohortAssessment{trigger: cohort.rows[0].DispatchTrigger}, nil
	}
	required := make([]string, 0, len(cohort.rows))
	for _, row := range cohort.rows {
		required = append(required, row.ScannerID)
	}
	assessment := cohortAssessment{id: uuid.NewString(), required: required, trigger: cohort.rows[0].DispatchTrigger}
	assessment.draft = &scanbase.AssessmentDraft{
		ID: assessment.id, CanonicalPath: generation.canonical, SourceSnapshotID: generation.snapshot.ID,
		RequiredScanners: required, Target: assessmentTarget(generation.snapshot, cohort.rows, generation.selections),
		Trigger: assessment.trigger,
	}
	_, err := concrete.Store.EnsureAssessment(ctx, *assessment.draft)
	return assessment, err
}
