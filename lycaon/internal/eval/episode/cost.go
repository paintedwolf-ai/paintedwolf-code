package episode

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/lycaon/lycaon/internal/pricing"
)

// CostLedger preserves recorded estimates and their pricing inputs.
type CostLedger struct {
	Version int          `json:"version"`
	Buckets []CostBucket `json:"buckets"`
}

type CostBucket struct {
	Provider           string        `json:"provider"`
	Model              string        `json:"model"`
	Caller             string        `json:"caller"`
	Status             string        `json:"status"`
	Calls              int64         `json:"calls"`
	NoCharge           bool          `json:"no_charge"`
	PromptTokens       int64         `json:"prompt_tokens"`
	CompletionTokens   int64         `json:"completion_tokens"`
	CacheReadTokens    int64         `json:"cache_read_tokens"`
	CacheWriteTokens   int64         `json:"cache_write_tokens"`
	CacheWrite1HTokens int64         `json:"cache_write_1h_tokens"`
	UnpricedTokens     int64         `json:"unpriced_tokens"`
	UnpricedCalls      int64         `json:"unpriced_calls"`
	KnownNanoUSD       *int64        `json:"known_nano_usd"`
	PricingSource      string        `json:"pricing_source"`
	PricedAsOf         string        `json:"priced_as_of"`
	UsageSource        string        `json:"usage_source"`
	Rate               *pricing.Rate `json:"rate"`
}

const costLedgerSQL = `WITH receipts AS (
 SELECT provider_id, model, caller, status, 1 AS calls, no_charge,
 prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cache_write_1h_tokens,
 unpriced_tokens, CASE WHEN estimated_nano_usd IS NULL OR unpriced_tokens > 0 THEN 1 ELSE 0 END AS unpriced_calls,
 estimated_nano_usd, pricing_source, COALESCE(priced_as_of,'') AS priced_as_of, usage_source, rate_snapshot
 FROM llm_calls
 UNION ALL
 SELECT provider_id, model, caller, 'reported', call_count, no_charge,
 prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens, cache_write_1h_tokens,
 unpriced_tokens, CASE WHEN priced_count = 0 THEN call_count ELSE unpriced_count END,
 CASE WHEN priced_count > 0 THEN estimated_nano_usd END,
 pricing_source, priced_as_of, usage_source, rate_snapshot FROM llm_call_rollups
)
SELECT provider_id, model, caller, status, SUM(calls), no_charge,
 SUM(prompt_tokens), SUM(completion_tokens), SUM(cache_read_tokens), SUM(cache_write_tokens), SUM(cache_write_1h_tokens),
 SUM(unpriced_tokens), SUM(unpriced_calls), SUM(estimated_nano_usd), pricing_source, priced_as_of, usage_source, rate_snapshot
FROM receipts GROUP BY provider_id, model, caller, status, no_charge, pricing_source, priced_as_of, usage_source, rate_snapshot
ORDER BY provider_id, model, caller, status, no_charge, pricing_source, priced_as_of, usage_source, rate_snapshot`

// ReadCost reads the entire isolated capture, including unattributed utility calls.
func ReadCost(ctx context.Context, capture string) (CostLedger, error) {
	result := CostLedger{Version: 1, Buckets: []CostBucket{}}
	database, err := openCapture(ctx, capture)
	if err != nil {
		return result, err
	}
	defer func() { _ = database.Close() }()
	rows, err := database.QueryContext(ctx, costLedgerSQL)
	if err != nil {
		return result, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		bucket, err := readCostBucket(rows)
		if err != nil {
			return result, err
		}
		result.Buckets = append(result.Buckets, bucket)
	}
	return result, rows.Err()
}

func readCostBucket(rows *sql.Rows) (CostBucket, error) {
	var b CostBucket
	var nano sql.NullInt64
	var raw string
	err := rows.Scan(&b.Provider, &b.Model, &b.Caller, &b.Status, &b.Calls, &b.NoCharge,
		&b.PromptTokens, &b.CompletionTokens, &b.CacheReadTokens, &b.CacheWriteTokens, &b.CacheWrite1HTokens,
		&b.UnpricedTokens, &b.UnpricedCalls, &nano, &b.PricingSource, &b.PricedAsOf, &b.UsageSource, &raw)
	if err != nil {
		return b, err
	}
	if nano.Valid {
		b.KnownNanoUSD = &nano.Int64
	}
	var fields map[string]json.RawMessage
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return b, fmt.Errorf("decode captured rate fields: %w", err)
		}
	}
	if len(fields) > 0 {
		if err := json.Unmarshal([]byte(raw), &b.Rate); err != nil {
			return b, fmt.Errorf("decode captured rate: %w", err)
		}
		if b.Rate != nil && !b.Rate.Valid() {
			return b, fmt.Errorf("captured rate is invalid")
		}
	}
	return b, nil
}
