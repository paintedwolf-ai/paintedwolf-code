package scan

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/inspector"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// SecurityCloseoutChecker validates anchored evidence for delegation closeout.
type SecurityCloseoutChecker struct {
	Store    *SQLStore
	Evidence inspector.EvidenceStore
	Settings *settings.SecurityScannersStore
}

type closeoutEval struct {
	OK       bool
	Pending  bool
	ScanID   string
	Guidance []api.ScanGuidanceSummary
	Reason   string
}

// Check evaluates durable scan evidence without creating work.
func (c *SecurityCloseoutChecker) Check(ctx context.Context, delegationID, projectDir string) error {
	if c == nil {
		return nil
	}
	result, err := c.evaluate(ctx, delegationID, projectDir)
	if err != nil {
		return err
	}
	if result.OK {
		return nil
	}
	if result.Pending {
		return &GatePendingError{
			ScanID:   result.ScanID,
			Guidance: result.Guidance,
			Reason:   result.Reason,
		}
	}
	return fmt.Errorf("security gate failed: %s", result.Reason)
}

func (c *SecurityCloseoutChecker) evaluate(ctx context.Context, delegationID, projectDir string) (closeoutEval, error) {
	if delegationID == "" || projectDir == "" {
		return closeoutEval{OK: true}, nil
	}
	if c.Settings != nil && !c.Settings.Effective().Enabled {
		return closeoutEval{OK: true}, nil
	}
	if c.Store == nil {
		return closeoutEval{Reason: "scan obligation ledger is unavailable"}, nil
	}
	landed, err := c.Store.LatestRequiredLandedChangeForDelegation(ctx, delegationID)
	if err != nil {
		return closeoutEval{}, err
	}
	if landed == nil {
		return closeoutEval{OK: true}, nil
	}
	scan, err := c.Store.Get(ctx, landed.ScanID)
	if err != nil {
		return closeoutEval{}, err
	}
	if scan == nil {
		return closeoutEval{Reason: "landed change is missing its scan obligation"}, nil
	}
	switch scan.Status {
	case api.CodeScanStatusPending, api.CodeScanStatusRunning:
		return closeoutEval{Pending: true, ScanID: scan.ID, Reason: "scan obligation is not complete"}, nil
	case api.CodeScanStatusFailed:
		return closeoutEval{ScanID: scan.ID, Guidance: scan.Guidance, Reason: "scan obligation failed"}, nil
	case api.CodeScanStatusTimedOut:
		return closeoutEval{ScanID: scan.ID, Guidance: scan.Guidance, Reason: "scan obligation timed out"}, nil
	case api.CodeScanStatusCanceled:
		return closeoutEval{ScanID: scan.ID, Reason: "scan obligation was canceled"}, nil
	case api.CodeScanStatusSuperseded:
		return closeoutEval{ScanID: scan.ID, Reason: "scan obligation was superseded"}, nil
	case api.CodeScanStatusComplete:
	default:
		return closeoutEval{ScanID: scan.ID, Reason: "scan obligation has unknown status"}, nil
	}
	if c.Evidence == nil {
		return closeoutEval{ScanID: scan.ID, Reason: "security evidence store is unavailable"}, nil
	}
	records, readErr := c.Evidence.ReadAll(ctx, projectDir, delegationID, api.ScanTaskID(scan.ID), evidence.GateTypeSecurity)
	if readErr != nil {
		return closeoutEval{}, readErr
	}
	latest := inspector.Latest(records)
	if latest == nil {
		return closeoutEval{ScanID: scan.ID, Reason: "scan obligation is missing security evidence"}, nil
	}
	ok, reason := inspector.SecurityEvidenceMatchesSnapshot(*latest, scan.SourceSnapshotID)
	if !ok {
		return closeoutEval{ScanID: scan.ID, Guidance: scan.Guidance, Reason: reason}, nil
	}
	return closeoutEval{OK: true, ScanID: scan.ID}, nil
}
