package survey

import (
	"context"
	"log/slog"
	"time"

	"github.com/lycaon/lycaon/internal/curationctx"
)

// LogSurveyRepoCall records one composite survey_repo invocation for debug tail.
func LogSurveyRepoCall(ctx context.Context, bundle string, probesRun, snapshotTotal int, coverage ProbeCoverage, duration time.Duration) {
	attrs := []any{
		slog.Int("survey_repo_calls", 1),
		slog.String("bundle", bundle),
		slog.Int("probes_run", probesRun),
		slog.Int("snapshot_total", snapshotTotal),
		slog.Int64("duration_ms", duration.Milliseconds()),
	}
	if coverage.Resolution != "" {
		attrs = append(attrs,
			slog.String("altitude", coverage.Altitude),
			slog.String("resolution", coverage.Resolution),
			slog.String("inventory_state", coverage.InventoryState),
			slog.Int("entries_examined", coverage.EntriesExamined),
			slog.Int("inventory_entries", coverage.InventoryEntries),
			slog.Int("groups", coverage.Groups),
			slog.Int("groups_folded", coverage.GroupsFolded),
		)
	}
	if sess := curationctx.SessionFrom(ctx).SessionID; sess != "" {
		attrs = append(attrs, slog.String("session_id", sess))
	}
	slog.InfoContext(ctx, "survey_repo", attrs...)
}

// LogSynthesisCurate records the curate trigger outcome for the debug tail.
func LogSynthesisCurate(ctx context.Context, sessionID string, triggered bool, selected, total int) {
	attrs := []any{
		slog.Bool("synthesis_curate_triggered", triggered),
		slog.Int("synthesis_curate_selected_total", selected),
		slog.String("parent_session_id", sessionID),
	}
	if total > 0 {
		attrs = append(attrs, slog.Int("snapshot_total", total))
	}
	if sess := curationctx.SessionFrom(ctx).SessionID; sess != "" {
		attrs = append(attrs, slog.String("session_id", sess))
	}
	slog.InfoContext(ctx, "synthesis_curate", attrs...)
}
