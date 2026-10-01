package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/scan/obligation"
	"github.com/lycaon/lycaon/pkg/api"
)

func insertLandedChangeTx(ctx context.Context, q *db.Queries, plan obligation.Plan) error {
	changedJSON, err := db.MarshalJSON(nonNilPaths(plan.ChangedPaths))
	if err != nil {
		return err
	}
	deletedJSON, err := db.MarshalJSON(nonNilPaths(plan.DeletedPaths))
	if err != nil {
		return err
	}
	createdAt := plan.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	if plan.Required {
		if strings.TrimSpace(plan.ScanID) == "" {
			return fmt.Errorf("required landed change missing scan id")
		}
		if strings.TrimSpace(plan.AssessmentID) == "" {
			return fmt.Errorf("required landed change missing assessment id")
		}
		if plan.InitialFailure == "" && (strings.TrimSpace(plan.ExecutionFingerprint) == "" || plan.ExecutionManifest.DefinitionFingerprint == "") {
			return fmt.Errorf("required landed change missing scanner execution identity")
		}
		var scanPaths, scanDeleted []string
		if plan.PathScoped {
			upserted, deleted, accumulatedErr := accumulatedLandedDeltaTx(ctx, q, plan)
			if accumulatedErr != nil {
				return accumulatedErr
			}
			scanPaths = obligation.PathScopedTarget(plan.CanonicalPath, upserted)
			scanDeleted = obligation.PathScopedTarget(plan.CanonicalPath, deleted)
			if len(scanPaths) == 0 && len(scanDeleted) == 0 && plan.InitialFailure == "" {
				plan.InitialFailure = "landed paths have no scannable target"
			}
		}
		pathsJSON, marshalErr := db.MarshalJSON(nonNilPaths(scanPaths))
		if marshalErr != nil {
			return marshalErr
		}
		scanDeletedJSON, marshalErr := db.MarshalJSON(nonNilPaths(scanDeleted))
		if marshalErr != nil {
			return marshalErr
		}
		status := "pending"
		completedAt := db.NullString("")
		errorText := db.NullString("")
		sourceSnapshotID := api.SourceSnapshotWarming
		if plan.InitialFailure != "" {
			status = "failed"
			completedAt = db.NullString(db.FormatTime(createdAt))
			errorText = db.NullString(plan.InitialFailure)
			sourceSnapshotID = ""
		}
		if err := q.InsertCodeScan(ctx, db.InsertCodeScanRowParams{
			ID: plan.ScanID, CanonicalPath: plan.CanonicalPath, CategoriesJson: `["sast"]`,
			ScannerID: db.NullString(plan.ScannerID), Status: status, CreatedAt: db.FormatTime(createdAt),
			DelegationID:     plan.DelegationID,
			SourceSnapshotID: sourceSnapshotID, Trigger: string(api.ScanTriggerLandedChange),
			CompletedAt: completedAt, PathsJson: pathsJSON.String,
			ReuseKey: "landed-change:" + plan.ScanID, Error: errorText,
		}); err != nil {
			return err
		}
		if strings.TrimSpace(plan.WorkflowRunID) != "" {
			if _, err := q.BindScanWorkflowRun(ctx, db.BindScanWorkflowRunParams{
				WorkflowRunID: plan.WorkflowRunID, ScanID: plan.ScanID, CreatedAt: db.FormatTime(createdAt),
			}); err != nil {
				return err
			}
		}
		requiredScanners := []string{}
		if strings.TrimSpace(plan.ScannerID) != "" {
			requiredScanners = append(requiredScanners, plan.ScannerID)
		}
		requiredScannersJSON, marshalErr := db.MarshalJSON(requiredScanners)
		if marshalErr != nil {
			return marshalErr
		}
		targetKind := string(api.ScanTargetFull)
		if plan.PathScoped {
			targetKind = string(api.ScanTargetPaths)
		}
		if err := q.InsertSecurityAssessment(ctx, db.InsertSecurityAssessmentParams{
			ID: plan.AssessmentID, CanonicalPath: plan.CanonicalPath,
			SourceSnapshotID:     sourceSnapshotID,
			RequiredScannersJson: requiredScannersJSON.String,
			TargetKind:           targetKind, TargetPathsJson: pathsJSON.String,
			DeletedPathsJson: scanDeletedJSON.String,
			Trigger:          string(api.ScanTriggerLandedChange), CreatedAt: db.FormatTime(createdAt),
		}); err != nil {
			return err
		}
		if _, err := q.BindScanAssessment(ctx, db.BindScanAssessmentParams{
			AssessmentID: plan.AssessmentID, ScanID: plan.ScanID, CreatedAt: db.FormatTime(createdAt),
		}); err != nil {
			return err
		}
		manifestJSON, marshalErr := db.MarshalJSON(plan.ExecutionManifest)
		if marshalErr != nil {
			return marshalErr
		}
		coverageStatus := ""
		failureCode := ""
		if plan.InitialFailure != "" {
			coverageStatus = string(api.ScanCoverageUnavailable)
			failureCode = plan.InitialFailureCode
			if failureCode == "" {
				failureCode = "SCAN_OBLIGATION_FAILED"
			}
		}
		if err := q.InsertScanRunFacts(ctx, db.InsertScanRunFactsParams{
			ScanID: plan.ScanID, AssessmentID: plan.AssessmentID, TargetKind: targetKind,
			TargetPathsJson: pathsJSON.String, DeletedPathsJson: scanDeletedJSON.String,
			ExecutionManifestJson: manifestJSON.String,
			ExecutionFingerprint:  plan.ExecutionFingerprint,
			FingerprintScheme:     plan.FingerprintScheme,
			CoverageStatus:        coverageStatus, FailureCode: failureCode,
		}); err != nil {
			return err
		}
	}
	required := int64(0)
	if plan.Required {
		required = 1
	}
	return q.InsertLandedChange(ctx, db.InsertLandedChangeParams{
		ID: plan.ID, WorkerJobID: plan.WorkerJobID, CanonicalPath: plan.CanonicalPath,
		DelegationID: plan.DelegationID, WorkflowRunID: plan.WorkflowRunID,
		ChangedPathsJson: changedJSON.String, DeletedPathsJson: deletedJSON.String,
		ScanRequired: required, ScanID: db.NullString(plan.ScanID), CreatedAt: db.FormatTime(createdAt),
	})
}

func nonNilPaths(paths []string) []string {
	if paths == nil {
		return []string{}
	}
	return paths
}

func accumulatedLandedDeltaTx(ctx context.Context, q *db.Queries, plan obligation.Plan) ([]string, []string, error) {
	state := make(map[string]bool, len(plan.ChangedPaths))
	apply := func(changed, deleted []string) {
		for _, item := range changed {
			item = strings.TrimSpace(item)
			if item != "" {
				state[item] = true
			}
		}
		for _, item := range deleted {
			item = strings.TrimSpace(item)
			if item != "" {
				state[item] = false
			}
		}
	}
	if strings.TrimSpace(plan.DelegationID) != "" {
		rows, err := q.ListLandedPathDeltas(ctx, db.ListLandedPathDeltasParams{
			DelegationID: plan.DelegationID, CanonicalPath: plan.CanonicalPath,
		})
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			var changed, deleted []string
			if err := json.Unmarshal([]byte(row.ChangedPathsJson), &changed); err != nil {
				return nil, nil, fmt.Errorf("decode prior landed changed paths: %w", err)
			}
			if err := json.Unmarshal([]byte(row.DeletedPathsJson), &deleted); err != nil {
				return nil, nil, fmt.Errorf("decode prior landed deleted paths: %w", err)
			}
			apply(changed, deleted)
		}
	}
	apply(plan.ChangedPaths, plan.DeletedPaths)
	upserted := make([]string, 0, len(state))
	deleted := make([]string, 0, len(state))
	for path, present := range state {
		if present {
			upserted = append(upserted, path)
		} else {
			deleted = append(deleted, path)
		}
	}
	sort.Strings(upserted)
	sort.Strings(deleted)
	return upserted, deleted, nil
}
