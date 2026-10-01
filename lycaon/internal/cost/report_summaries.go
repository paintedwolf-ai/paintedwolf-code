package cost

import (
	"context"
	"database/sql"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

const reportSummariesSQL = reportAttributionSQL + `, buckets AS MATERIALIZED (
 SELECT '@project' AS bucket, * FROM attributed
 UNION ALL
 SELECT CASE WHEN root_id <> '' THEN root_id
   WHEN session_id = '' AND parent_session_id = '' THEN '@utilities' ELSE '@retired' END AS bucket, *
 FROM attributed WHERE root_id = '' OR root_id IN (SELECT value FROM json_each(?2))
 )`

// Remove the UTC suffix before MAX so fractional seconds sort after whole seconds.
const costBucketSummarySQL = `, eligible AS MATERIALIZED (
 SELECT * FROM buckets WHERE status IN ('reported', 'unknown') AND (
 bucket LIKE '@%' OR status = 'unknown' OR
 (caller IN ('', 'coordinator') AND session_id = root_id) OR
 (caller = 'worker' AND parent_session_id = root_id) OR caller = 'summarizer'
 )
), workers AS (
 SELECT bucket, COUNT(DISTINCT session_id) AS tasks FROM eligible
 WHERE status = 'reported' AND caller = 'worker' GROUP BY bucket
)
SELECT e.bucket, e.status, e.caller, e.pricing_source, COALESCE(NULLIF(MAX(rtrim(e.priced_as_of, 'Z')), '') || 'Z', ''), e.usage_source, e.no_charge, e.priced,
 SUM(e.call_count), SUM(e.prompt_tokens), SUM(e.completion_tokens), SUM(e.cache_read_tokens), SUM(e.cache_write_tokens),
 SUM(e.unpriced_tokens), SUM(e.cache_savings_nano_usd), SUM(e.unpriced_cache_tokens), SUM(e.estimated_nano_usd), COALESCE(w.tasks,0)
FROM eligible e LEFT JOIN workers w ON w.bucket = e.bucket
GROUP BY e.bucket, e.status, e.caller, e.pricing_source, e.usage_source, e.no_charge, e.priced`

func readReportSummaries(ctx context.Context, tx *sql.Tx, projectID string, ids []string) (map[string]api.CostSummary, error) {
	raw, err := db.MarshalJSON(ids)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, reportSummariesSQL+costBucketSummarySQL, projectID, raw.String)
	if err != nil {
		return nil, err
	}
	return readCostSummaryRows(rows)
}

func readCostSummaryRows(rows *sql.Rows) (map[string]api.CostSummary, error) {
	var err error
	defer func() { _ = rows.Close() }()
	events := make(map[string][]UsageEvent)
	unknown := make(map[string]UnknownCalls)
	workers := make(map[string]int)
	for rows.Next() {
		var evt UsageEvent
		var bucket, status, asOf string
		var noCharge, priced, tasks int
		var savings, nano int64
		if err := rows.Scan(&bucket, &status, &evt.Caller, &evt.PricingSource, &asOf, &evt.UsageSource, &noCharge, &priced,
			&evt.CallCount, &evt.PromptTokens, &evt.CompletionTokens, &evt.CacheReadInputTokens, &evt.CacheCreationInputTokens,
			&evt.UnpricedTokens, &savings, &evt.UnpricedCacheTokens, &nano, &tasks); err != nil {
			return nil, err
		}
		evt.NoCharge = noCharge != 0
		workers[bucket] = tasks
		if status == "unknown" {
			counts := unknown[bucket]
			counts.Total += evt.CallCount
			if !evt.NoCharge {
				counts.Charged += evt.CallCount
			}
			unknown[bucket] = counts
			if _, ok := events[bucket]; !ok {
				events[bucket] = nil
			}
			continue
		}
		evt.CacheSavingsNanoUSD = savings
		if priced != 0 {
			evt.EstimatedNanoUSD = &nano
		}
		if asOf != "" {
			evt.PricedAsOf, err = db.ParseTime(asOf)
			if err != nil {
				return nil, err
			}
		}
		events[bucket] = append(events[bucket], evt)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make(map[string]api.CostSummary, len(events))
	for key, evts := range events {
		summary := buildCostSummary(evts, unknown[key])
		summary.Workers.TaskCount = workers[key]
		out[key] = summary
	}
	return out, nil
}
