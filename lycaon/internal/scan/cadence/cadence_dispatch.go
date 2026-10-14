package cadence

import (
	"context"
	"log/slog"
	"strings"
	"time"

	scanbase "github.com/lycaon/lycaon/internal/scan"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/pkg/api"
)

const dispatchHeartbeatEvery = scanbase.DispatchClaimTTL / 4

const dispatchCleanupTimeout = 5 * time.Second

type dispatchFailure struct {
	ScannerID string
	Error     string
}

type rootDispatcher struct {
	c *Service
}

// dispatchRoot gives due scanners one source generation and requeues failed claims.
func (d rootDispatcher) dispatchRoot(ctx context.Context, canonical string) ([]*api.CodeScan, []dispatchFailure) {
	claims := claimBook{c: d.c}
	claimed, err := claims.claimDueSeries(ctx, canonical)
	if err != nil {
		claims.cleanupDispatches(ctx, claimed)
		slog.WarnContext(ctx, "claim due scan series", "path", canonical, "error", err)
		return nil, []dispatchFailure{{Error: err.Error()}}
	}
	if len(claimed) == 0 {
		return nil, nil
	}
	ctx, release, current := d.c.roots.beginDispatch(ctx, &d.c.mu, canonical, claimed[0].DispatchToken)
	if !current {
		return nil, nil
	}
	defer release()
	d.logDispatch(ctx, "claimed", canonical, claimed, nil)

	stopHeartbeat := claims.heartbeatClaims(ctx, claimed)
	owned := claimed
	defer func() {
		stopHeartbeat()
		claims.cleanupDispatches(ctx, owned)
	}()
	snapshot, headSHA, publishErr := d.publishGeneration(ctx, canonical, claimed)
	if publishErr != nil {
		d.logDispatch(ctx, "requeued", canonical, claimed, publishErr)
		return nil, []dispatchFailure{{Error: publishErr.Error()}}
	}
	executions, executionErrors := d.dispatchExecutions(ctx, canonical, claimed)
	selections, selectionErr := d.dispatchTargetSelections(ctx, claimed, snapshot, executions)
	if selectionErr != nil {
		d.logDispatch(ctx, "requeued", canonical, claimed, selectionErr)
		return nil, []dispatchFailure{{Error: selectionErr.Error()}}
	}
	unchanged, baselined, claimed := splitSelections(claimed, selections)
	if len(unchanged) > 0 {
		d.logDispatch(ctx, "unchanged", canonical, unchanged, nil)
		claims.finishNoopDispatches(ctx, unchanged)
	}
	if len(baselined) > 0 {
		d.logDispatch(ctx, "baselined", canonical, baselined, nil, "snapshot_id", snapshot.ID)
		claims.finishBaselineDispatches(ctx, baselined, snapshot, executions)
	}
	if len(claimed) == 0 {
		return nil, nil
	}
	generation := dispatchGeneration{
		canonical: canonical, snapshot: snapshot, headSHA: headSHA,
		executions: executions, executionErrors: executionErrors, selections: selections,
	}
	var (
		records  []*api.CodeScan
		failures []dispatchFailure
	)
	for _, cohort := range dispatchCohorts(claimed) {
		enqueued, failed := d.enqueueCohort(ctx, generation, cohort)
		records = append(records, enqueued...)
		failures = append(failures, failed...)
	}
	d.logDispatch(ctx, "enqueued", canonical, claimed, nil, "targets", targetSummary(claimed, selections))
	return records, failures
}

// Cancellation ends owned work; heartbeat expiry recovers vanished dispatchers.
func (d rootDispatcher) publishGeneration(ctx context.Context, canonical string, claimed []scanbase.SeriesRow) (sourcesnapshot.Snapshot, string, error) {
	started := time.Now()
	snapshot, headSHA, err := d.c.Coordinator.PublishSourceGeneration(ctx, canonical)
	waited := time.Since(started)
	if err != nil {
		return sourcesnapshot.Snapshot{}, "", err
	}
	slog.InfoContext(ctx, "scan dispatch source generation",
		"component", "scan_cadence", "path", canonical, "scanners", scannerIDs(claimed),
		"snapshot_id", snapshot.ID, "files", snapshot.FileCount, "unobserved", len(snapshot.Unobserved()),
		"admission", string(snapshot.AdmissionMode), "waited_ms", waited.Milliseconds())
	return snapshot, headSHA, nil
}

func (d rootDispatcher) logDispatch(ctx context.Context, state, canonical string, rows []scanbase.SeriesRow, err error, extra ...any) {
	attrs := []any{"component", "scan_cadence", "state", state, "path", canonical, "scanners", scannerIDs(rows)}
	if len(rows) > 0 {
		attrs = append(attrs, "trigger", string(rows[0].DispatchTrigger))
	}
	attrs = append(attrs, extra...)
	if err != nil {
		attrs = append(attrs, "error", err.Error())
		slog.WarnContext(ctx, "scan dispatch", attrs...)
		return
	}
	slog.InfoContext(ctx, "scan dispatch", attrs...)
}

func scannerIDs(rows []scanbase.SeriesRow) string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ScannerID)
	}
	return strings.Join(ids, ",")
}

func proactiveTrigger(trigger api.ScanTrigger) bool {
	switch trigger {
	case api.ScanTriggerWriteBurst, api.ScanTriggerAuthorityRefresh:
		return true
	default:
		return false
	}
}
