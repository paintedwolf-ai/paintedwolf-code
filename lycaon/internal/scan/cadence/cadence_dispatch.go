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

// dispatchRoot gives due scanners one source generation and requeues failed claims.
func (c *Service) dispatchRoot(ctx context.Context, canonical string) ([]*api.CodeScan, []dispatchFailure) {
	claimed, err := c.claimDueSeries(ctx, canonical)
	if err != nil {
		c.cleanupDispatches(ctx, claimed)
		slog.WarnContext(ctx, "claim due scan series", "path", canonical, "error", err)
		return nil, []dispatchFailure{{Error: err.Error()}}
	}
	if len(claimed) == 0 {
		return nil, nil
	}
	ctx, release, current := c.beginRootDispatch(ctx, canonical, claimed[0].DispatchToken)
	if !current {
		return nil, nil
	}
	defer release()
	c.logDispatch(ctx, "claimed", canonical, claimed, nil)

	stopHeartbeat := c.heartbeatClaims(ctx, claimed)
	owned := claimed
	defer func() {
		stopHeartbeat()
		c.cleanupDispatches(ctx, owned)
	}()
	snapshot, headSHA, publishErr := c.publishGeneration(ctx, canonical, claimed)
	if publishErr != nil {
		c.logDispatch(ctx, "requeued", canonical, claimed, publishErr)
		return nil, []dispatchFailure{{Error: publishErr.Error()}}
	}
	executions, executionErrors := c.dispatchExecutions(ctx, canonical, claimed)
	selections, selectionErr := c.dispatchTargetSelections(ctx, claimed, snapshot, executions)
	if selectionErr != nil {
		c.logDispatch(ctx, "requeued", canonical, claimed, selectionErr)
		return nil, []dispatchFailure{{Error: selectionErr.Error()}}
	}
	unchanged, baselined, claimed := splitSelections(claimed, selections)
	if len(unchanged) > 0 {
		c.logDispatch(ctx, "unchanged", canonical, unchanged, nil)
		c.finishNoopDispatches(ctx, unchanged)
	}
	if len(baselined) > 0 {
		c.logDispatch(ctx, "baselined", canonical, baselined, nil, "snapshot_id", snapshot.ID)
		c.finishBaselineDispatches(ctx, baselined, snapshot, executions)
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
		enqueued, failed := c.enqueueCohort(ctx, generation, cohort)
		records = append(records, enqueued...)
		failures = append(failures, failed...)
	}
	c.logDispatch(ctx, "enqueued", canonical, claimed, nil, "targets", targetSummary(claimed, selections))
	return records, failures
}

// Retry changed-file work; release members from a pass that started without them.
func (c *Service) releaseUndispatched(ctx context.Context, canonical string, row scanbase.SeriesRow, cause error) {
	if row.DispatchPassID == "" {
		c.logDispatch(ctx, "requeued", canonical, []scanbase.SeriesRow{row}, cause)
		c.requeueDispatch(ctx, row)
		return
	}
	c.logDispatch(ctx, "left_pass", canonical, []scanbase.SeriesRow{row}, cause, "pass_id", row.DispatchPassID)
	c.leavePass(ctx, row)
}

// Capture duration scales with the repository. Cancellation ends owned work;
// heartbeat expiry recovers dispatchers that actually disappeared.
func (c *Service) publishGeneration(ctx context.Context, canonical string, claimed []scanbase.SeriesRow) (sourcesnapshot.Snapshot, string, error) {
	started := time.Now()
	snapshot, headSHA, err := c.Coordinator.PublishSourceGeneration(ctx, canonical)
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

func (c *Service) heartbeatClaims(ctx context.Context, claimed []scanbase.SeriesRow) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(dispatchHeartbeatEvery)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := c.now()
				for _, row := range claimed {
					if err := c.Store.HeartbeatClaim(ctx, row, now); err != nil {
						slog.WarnContext(ctx, "heartbeat scan dispatch claim", "scanner_id", row.ScannerID, "error", err)
					}
				}
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

func (c *Service) logDispatch(ctx context.Context, state, canonical string, rows []scanbase.SeriesRow, err error, extra ...any) {
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
