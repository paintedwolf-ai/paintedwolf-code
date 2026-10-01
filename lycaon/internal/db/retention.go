package db

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

const (
	// DefaultOperationJournalRetention is the idempotency receipt lifetime.
	DefaultOperationJournalRetention = 14 * 24 * time.Hour
	DefaultRetentionBatchSize        = 500
	// DefaultFTSOptimizeInterval limits full-index merges.
	DefaultFTSOptimizeInterval = 24 * time.Hour
	// Expired per-file findings are recomputed on the next read.
	DefaultBlobFindingsRetention = 90 * 24 * time.Hour
)

// RetentionConfig controls bounded background store cleanup.
type RetentionConfig struct {
	Enabled                           bool
	OperationJournals                 time.Duration
	FTSOptimizeInterval               time.Duration
	BlobFindings                      time.Duration
	IncrementalVacuumMinFreelistPages int64
	BatchSize                         int
}

func DefaultRetention() RetentionConfig {
	return RetentionConfig{
		Enabled:                           !retentionDisabledByEnv(),
		OperationJournals:                 DefaultOperationJournalRetention,
		FTSOptimizeInterval:               DefaultFTSOptimizeInterval,
		BlobFindings:                      DefaultBlobFindingsRetention,
		IncrementalVacuumMinFreelistPages: 256,
		BatchSize:                         DefaultRetentionBatchSize,
	}
}

// RunMaintenance performs bounded cleanup under write pressure.
func RunMaintenance(ctx context.Context, sqlDB *Store, cfg RetentionConfig) error {
	if sqlDB == nil || !cfg.Enabled {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sqlDB.maintenanceSignals():
		}
		for {
			report, err := RunRetention(ctx, sqlDB, cfg)
			if err != nil {
				return err
			}
			if report.TotalDeleted() == 0 {
				break
			}
			slog.InfoContext(ctx, "store maintenance batch complete", "deleted", report.TotalDeleted())
		}
	}
}

type RetentionReport struct {
	// OperationJournals counts deleted receipts or compacted editor replay payloads per table.
	OperationJournals map[string]int64
	OrphanLLMCalls    int64
	// OrphanBlueprintGrants counts revocations, not deleted rows.
	OrphanBlueprintGrants int64
	// BlobFindings counts aged per-file findings cache rows removed.
	BlobFindings        int64
	IncrementalVacuumed bool
	FTSOptimized        []string
}

func (r RetentionReport) TotalDeleted() int64 {
	total := r.OrphanLLMCalls + r.BlobFindings
	for _, n := range r.OperationJournals {
		total += n
	}
	return total
}

func (r RetentionReport) JournalDeleted(table string) int64 {
	return r.OperationJournals[table]
}

// RunRetention removes one bounded batch of stale store rows.
func RunRetention(ctx context.Context, sqlDB Handle, cfg RetentionConfig) (RetentionReport, error) {
	var rep RetentionReport
	if sqlDB == nil || !cfg.Enabled {
		return rep, nil
	}
	now := time.Now().UTC()
	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = DefaultRetentionBatchSize
	}

	var err error
	rep.OperationJournals, err = purgeOperationJournals(ctx, sqlDB, now, cfg.OperationJournals, batchSize)
	if err != nil {
		return rep, err
	}

	n, err := purgeUnreachableLLMCalls(ctx, sqlDB, batchSize)
	if err != nil {
		return rep, err
	}
	rep.OrphanLLMCalls = n

	optimized, err := optimizeFTSTables(ctx, sqlDB, now, cfg.FTSOptimizeInterval)
	if err != nil {
		return rep, err
	}
	rep.FTSOptimized = optimized

	n, err = revokeOrphanBlueprintGrants(ctx, sqlDB, now)
	if err != nil {
		return rep, err
	}
	rep.OrphanBlueprintGrants = n

	n, err = purgeAgedBlobFindings(ctx, sqlDB, now, cfg.BlobFindings, batchSize)
	if err != nil {
		return rep, err
	}
	rep.BlobFindings = n

	if rep.TotalDeleted() > 0 && cfg.IncrementalVacuumMinFreelistPages > 0 {
		vacuumed, err := runIncrementalVacuum(ctx, sqlDB, cfg.IncrementalVacuumMinFreelistPages)
		if err != nil {
			return rep, err
		}
		rep.IncrementalVacuumed = vacuumed
	}
	return rep, nil
}

func runIncrementalVacuum(ctx context.Context, sqlDB Handle, minFreelistPages int64) (bool, error) {
	var mode int
	if err := sqlDB.QueryRowContext(ctx, `PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		return false, fmt.Errorf("read auto_vacuum mode: %w", err)
	}
	if mode != 2 { // SQLite INCREMENTAL mode.
		return false, nil
	}
	var freePages int64
	if err := sqlDB.QueryRowContext(ctx, `PRAGMA freelist_count`).Scan(&freePages); err != nil {
		return false, fmt.Errorf("read freelist count: %w", err)
	}
	if freePages < minFreelistPages {
		return false, nil
	}
	if _, err := sqlDB.ExecContext(ctx, `PRAGMA incremental_vacuum(1024)`); err != nil {
		return false, fmt.Errorf("incremental vacuum store: %w", err)
	}
	return true, nil
}

func retentionDisabledByEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LYCAON_STORE_RETENTION"))) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

func cutoffTime(now time.Time, retention time.Duration) string {
	if retention <= 0 {
		return FormatTime(now)
	}
	return FormatTime(now.Add(-retention))
}

type operationJournal struct {
	name  string
	query string
}

var operationJournals = []operationJournal{
	{
		name: "prompt_submissions",
		query: `DELETE FROM prompt_submissions WHERE rowid IN (
			SELECT rowid FROM prompt_submissions
			WHERE status IN ('complete', 'failed', 'interrupted', 'canceled')
			  AND COALESCE(NULLIF(completed_at, ''), created_at) < ?
			ORDER BY rowid LIMIT ?
		)`,
	},
	{
		name: "source_file_requests",
		query: `DELETE FROM source_file_requests WHERE rowid IN (
			SELECT rowid FROM source_file_requests
			WHERE state IN ('completed', 'canceled') AND COALESCE(completed_at, created_at) < ?
			ORDER BY rowid LIMIT ?
		)`,
	},
	{
		name: "source_mutations",
		query: `DELETE FROM source_mutations WHERE rowid IN (
			SELECT rowid FROM source_mutations
			WHERE status IN ('committed', 'failed', 'diverged') AND created_at < ?
			  AND (status='committed' OR COALESCE(json_extract(plan_json, '$.hold_started'),0)=0)
			  AND NOT EXISTS (SELECT 1 FROM source_file_requests r WHERE r.id=source_mutations.id AND r.state IN ('queued','running','failed','interrupted'))
			ORDER BY rowid LIMIT ?
		)`,
	},
	{
		name: "editor_mutations",
		query: `UPDATE editor_mutations SET response_json=NULL, content='', before_bytes=NULL,
            after_bytes=NULL, checkpoint=NULL, before_checkpoint=NULL, replay_compacted=1 WHERE rowid IN (
			SELECT rowid FROM editor_mutations
			WHERE replay_compacted=0 AND status IN ('complete', 'conflict', 'failed') AND created_at < ?
			ORDER BY rowid LIMIT ?
		)`,
	},
	{
		name: "project_removals",
		query: `DELETE FROM project_removals WHERE rowid IN (
            SELECT rowid FROM project_removals WHERE settled = 1 AND created_at < ? ORDER BY rowid LIMIT ?
        )`,
	},
	{
		name: "command_invocations",
		query: `DELETE FROM command_invocations WHERE rowid IN (
			SELECT rowid FROM command_invocations WHERE created_at < ? ORDER BY rowid LIMIT ?
		)`,
	},
	// Purge workflow receipts only after their run terminates.
	{
		name: "workflow_verdict_operations",
		query: `DELETE FROM workflow_verdict_operations WHERE rowid IN (
			SELECT rowid FROM workflow_verdict_operations
			WHERE status IN ('committed', 'failed', 'diverged') AND created_at < ?
			  AND run_id IN (SELECT id FROM workflow_runs
			                 WHERE status IN ('complete', 'canceled', 'interrupted'))
			ORDER BY rowid LIMIT ?
		)`,
	},
	{
		name: "workflow_commands",
		query: `DELETE FROM workflow_commands WHERE (run_id, source_revision) IN (
			SELECT run_id, source_revision FROM workflow_commands WHERE committed_at < ?
			  AND run_id IN (SELECT id FROM workflow_runs
			                 WHERE status IN ('complete', 'canceled', 'interrupted'))
			ORDER BY committed_at, run_id, source_revision LIMIT ?
		)`,
	},
	{
		name: "workflow_start_operations",
		query: `DELETE FROM workflow_start_operations WHERE rowid IN (
			SELECT rowid FROM workflow_start_operations WHERE committed_at < ?
			  AND workflow_run_id IN (SELECT id FROM workflow_runs
			                          WHERE status IN ('complete', 'canceled', 'interrupted'))
			ORDER BY rowid LIMIT ?
		)`,
	},
	{
		name: "workflow_teardown_operations",
		query: `DELETE FROM workflow_teardown_operations WHERE rowid IN (
			SELECT rowid FROM workflow_teardown_operations
			WHERE status = 'complete' AND created_at < ?
			  AND run_id IN (SELECT id FROM workflow_runs
			                 WHERE status IN ('complete', 'canceled', 'interrupted'))
			ORDER BY rowid LIMIT ?
		)`,
	},
	{
		name: "approval_operations",
		query: `DELETE FROM approval_operations WHERE rowid IN (
			SELECT rowid FROM approval_operations
			WHERE status IN ('committed', 'rolled_back') AND created_at < ?
			ORDER BY rowid LIMIT ?
		)`,
	},
	{
		name: "rewind_operations",
		query: `DELETE FROM rewind_operations WHERE rowid IN (
			SELECT rowid FROM rewind_operations
			WHERE status IN ('committed', 'rolled_back', 'diverged') AND created_at < ?
			ORDER BY rowid LIMIT ?
		)`,
	},
}

func purgeOperationJournals(ctx context.Context, sqlDB Handle, now time.Time, retention time.Duration, batchSize int) (map[string]int64, error) {
	if retention <= 0 {
		return nil, nil
	}
	cutoff := cutoffTime(now, retention)
	out := make(map[string]int64, len(operationJournals))
	for _, journal := range operationJournals {
		res, err := sqlDB.ExecContext(ctx, journal.query, cutoff, batchSize)
		if err != nil {
			return out, fmt.Errorf("purge %s: %w", journal.name, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			out[journal.name] = n
		}
	}
	return out, nil
}

// purgeUnreachableLLMCalls removes receipts whose project is absent.
func purgeUnreachableLLMCalls(ctx context.Context, sqlDB Handle, batchSize int) (int64, error) {
	res, err := sqlDB.ExecContext(ctx, `
		DELETE FROM llm_calls WHERE rowid IN (
			SELECT rowid FROM llm_calls
			WHERE project_id != '' AND project_id NOT IN (SELECT id FROM projects)
			ORDER BY rowid LIMIT ?
		)
	`, batchSize)
	if err != nil {
		return 0, fmt.Errorf("purge unreachable llm_calls: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

type llmRollupKey struct {
	projectID, sessionID, parentSessionID, providerID, model, caller, day string
	pricingSource, pricedAsOf, usageSource, rateSnapshot                  string
	noCharge                                                              int64
}

type llmRollupAgg struct {
	callCount, promptTokens, completionTokens       int64
	cacheReadTokens, cacheWriteTokens               int64
	estimatedNanoUSD, unpricedCount                 int64
	cacheWrite1HTokens, unpricedTokens, pricedCount int64
	cacheSavings, unpricedCacheTokens               int64
}

// RollupLLMCallIDs folds explicitly reviewed receipts into spend/provenance totals.
func RollupLLMCallIDs(ctx context.Context, sqlDB Handle, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return 0, err
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin llm_calls rollup tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT rowid, project_id, session_id, parent_session_id, provider_id, model, caller,
		       substr(COALESCE(NULLIF(completed_at, ''), started_at), 1, 10) AS day,
		       prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
		       COALESCE(estimated_nano_usd, 0), unpriced, cache_write_1h_tokens, unpriced_tokens,
		       estimated_nano_usd IS NOT NULL, pricing_source, COALESCE(priced_as_of, ''), usage_source, no_charge, rate_snapshot, cache_savings_nano_usd, unpriced_cache_tokens
		FROM llm_calls
		WHERE status = 'reported' AND id IN (SELECT value FROM json_each(?))
 AND NOT EXISTS(SELECT 1 FROM history_busy_projects p WHERE p.project_id = llm_calls.project_id)
 AND NOT EXISTS(SELECT 1 FROM history_protections p WHERE p.protected = 1 AND p.project_id = llm_calls.project_id AND (p.session_id IS NULL OR p.session_id = llm_calls.session_id))
		ORDER BY rowid
	`, string(raw))
	if err != nil {
		return 0, fmt.Errorf("select reviewed llm_calls: %w", err)
	}
	defer func() { _ = rows.Close() }()

	aggregates := make(map[llmRollupKey]*llmRollupAgg)
	var rowIDs []int64
	for rows.Next() {
		var rowID int64
		var key llmRollupKey
		var promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens, estimatedNanoUSD, unpriced int64
		var cacheWrite1H, unpricedTokens, pricedCount, cacheSavings, unpricedCacheTokens int64
		if err := rows.Scan(&rowID, &key.projectID, &key.sessionID, &key.parentSessionID, &key.providerID, &key.model, &key.caller, &key.day,
			&promptTokens, &completionTokens, &cacheReadTokens, &cacheWriteTokens, &estimatedNanoUSD, &unpriced,
			&cacheWrite1H, &unpricedTokens, &pricedCount, &key.pricingSource, &key.pricedAsOf, &key.usageSource, &key.noCharge, &key.rateSnapshot, &cacheSavings, &unpricedCacheTokens); err != nil {
			return 0, fmt.Errorf("scan reviewed llm_call: %w", err)
		}
		rowIDs = append(rowIDs, rowID)
		agg, ok := aggregates[key]
		if !ok {
			agg = &llmRollupAgg{}
			aggregates[key] = agg
		}
		agg.callCount++
		agg.promptTokens += promptTokens
		agg.completionTokens += completionTokens
		agg.cacheReadTokens += cacheReadTokens
		agg.cacheWriteTokens += cacheWriteTokens
		agg.estimatedNanoUSD += estimatedNanoUSD
		agg.unpricedCount += unpriced
		agg.unpricedTokens += unpricedTokens
		agg.cacheWrite1HTokens += cacheWrite1H
		agg.pricedCount += pricedCount
		agg.cacheSavings += cacheSavings
		agg.unpricedCacheTokens += unpricedCacheTokens
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate reviewed llm_calls: %w", err)
	}
	if len(rowIDs) == 0 {
		return 0, nil
	}

	for key, agg := range aggregates {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO llm_call_rollups (
				project_id, session_id, parent_session_id, provider_id, model, caller, day,
				call_count, prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
				estimated_nano_usd, unpriced_count, cache_write_1h_tokens, unpriced_tokens, priced_count,
				pricing_source, priced_as_of, usage_source, no_charge, rate_snapshot, cache_savings_nano_usd, unpriced_cache_tokens
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(project_id, session_id, parent_session_id, provider_id, model, caller, day, pricing_source, priced_as_of, usage_source, no_charge, rate_snapshot) DO UPDATE SET
				call_count = call_count + excluded.call_count,
				prompt_tokens = prompt_tokens + excluded.prompt_tokens,
				completion_tokens = completion_tokens + excluded.completion_tokens,
				cache_read_tokens = cache_read_tokens + excluded.cache_read_tokens,
				cache_write_tokens = cache_write_tokens + excluded.cache_write_tokens,
				estimated_nano_usd = estimated_nano_usd + excluded.estimated_nano_usd,
				unpriced_count = unpriced_count + excluded.unpriced_count,
				cache_write_1h_tokens = cache_write_1h_tokens + excluded.cache_write_1h_tokens,
				unpriced_tokens = unpriced_tokens + excluded.unpriced_tokens,
				priced_count = priced_count + excluded.priced_count,
				cache_savings_nano_usd = cache_savings_nano_usd + excluded.cache_savings_nano_usd,
				unpriced_cache_tokens = unpriced_cache_tokens + excluded.unpriced_cache_tokens
		`, key.projectID, key.sessionID, key.parentSessionID, key.providerID, key.model, key.caller, key.day,
			agg.callCount, agg.promptTokens, agg.completionTokens, agg.cacheReadTokens, agg.cacheWriteTokens,
			agg.estimatedNanoUSD, agg.unpricedCount, agg.cacheWrite1HTokens, agg.unpricedTokens, agg.pricedCount,
			key.pricingSource, key.pricedAsOf, key.usageSource, key.noCharge, key.rateSnapshot, agg.cacheSavings, agg.unpricedCacheTokens); err != nil {
			return 0, fmt.Errorf("upsert llm_call_rollups: %w", err)
		}
	}

	rowIDsJSON, err := json.Marshal(rowIDs)
	if err != nil {
		return 0, fmt.Errorf("encode receipt row selection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO history_pruned_bodies(class,owner_id,project_id,pruned_at,reason)
 SELECT 'receipt_detail',id,project_id,?,'retention_policy' FROM llm_calls WHERE rowid IN (SELECT value FROM json_each(?)) ON CONFLICT(class,owner_id) DO NOTHING`, FormatTime(time.Now().UTC()), string(rowIDsJSON)); err != nil {
		return 0, fmt.Errorf("retain receipt tombstones: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM llm_calls WHERE rowid IN (SELECT value FROM json_each(?))`, string(rowIDsJSON),
	); err != nil {
		return 0, fmt.Errorf("delete rolled-up llm_calls: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit llm_calls rollup: %w", err)
	}
	return int64(len(rowIDs)), nil
}

// External-content indexes accumulate fragments during insert/delete churn.
var ftsOptimizeTables = []string{"messages_fts", "evidence_fts"}

// Each index has its own persisted maintenance interval.
func optimizeFTSTables(ctx context.Context, sqlDB Handle, now time.Time, interval time.Duration) ([]string, error) {
	if interval <= 0 {
		return nil, nil
	}
	var optimized []string
	for _, name := range ftsOptimizeTables {
		due, err := ftsOptimizeDue(ctx, sqlDB, name, now, interval)
		if err != nil {
			return optimized, err
		}
		if !due {
			continue
		}
		if err := runFTSOptimize(ctx, sqlDB, name); err != nil {
			return optimized, fmt.Errorf("optimize %s: %w", name, err)
		}
		if _, err := sqlDB.ExecContext(ctx, `
			INSERT INTO fts_maintenance (name, last_optimized_at) VALUES (?, ?)
			ON CONFLICT(name) DO UPDATE SET last_optimized_at = excluded.last_optimized_at
		`, name, FormatTime(now)); err != nil {
			return optimized, fmt.Errorf("stamp fts optimize latch %s: %w", name, err)
		}
		optimized = append(optimized, name)
	}
	return optimized, nil
}

// runFTSOptimize uses fixed statements because table names cannot be bound parameters.
func runFTSOptimize(ctx context.Context, sqlDB Handle, name string) error {
	var err error
	switch name {
	case "messages_fts":
		_, err = sqlDB.ExecContext(ctx, `INSERT INTO messages_fts(messages_fts) VALUES('optimize')`)
	case "evidence_fts":
		_, err = sqlDB.ExecContext(ctx, `INSERT INTO evidence_fts(evidence_fts) VALUES('optimize')`)
	default:
		return fmt.Errorf("unknown fts table %q", name)
	}
	return err
}

func ftsOptimizeDue(ctx context.Context, sqlDB Handle, name string, now time.Time, interval time.Duration) (bool, error) {
	var lastAt string
	err := sqlDB.QueryRowContext(ctx, `SELECT last_optimized_at FROM fts_maintenance WHERE name = ?`, name).Scan(&lastAt)
	if IsNoRows(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("read fts optimize latch %s: %w", name, err)
	}
	last, err := ParseTime(lastAt)
	if err != nil {
		return false, fmt.Errorf("parse fts optimize latch %s: %w", name, err)
	}
	return now.Sub(last) >= interval, nil
}

func purgeAgedBlobFindings(ctx context.Context, sqlDB Handle, now time.Time, retention time.Duration, batchSize int) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	res, err := sqlDB.ExecContext(ctx, `
		DELETE FROM scan_blob_findings WHERE (execution_fingerprint, content_id) IN (
			SELECT execution_fingerprint, content_id FROM scan_blob_findings
			WHERE created_at < ? ORDER BY created_at LIMIT ?
		)`, cutoffTime(now, retention), batchSize)
	if err != nil {
		return 0, fmt.Errorf("purge aged blob findings: %w", err)
	}
	return res.RowsAffected()
}
