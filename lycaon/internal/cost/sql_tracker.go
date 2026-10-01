package cost

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
)

// SQLTracker persists provider receipts and spend summaries.
type SQLTracker struct {
	db db.Handle

	mu     sync.RWMutex
	pricer Pricer
}

func NewSQLTracker(database db.Handle, pricer Pricer) *SQLTracker {
	return &SQLTracker{db: database, pricer: pricer}
}

// RecoverStartedCalls marks receipts left by stopped processes as unknown.
func (t *SQLTracker) RecoverStartedCalls(ctx context.Context) error {
	if t == nil || t.db == nil {
		return fmt.Errorf("cost tracker database is not configured")
	}
	_, err := t.db.ExecContext(ledgerCtx(ctx), `
		UPDATE llm_calls
		SET status = 'unknown', completed_at = ?
		WHERE status = 'started'
	`, db.FormatTime(time.Now().UTC()))
	return err
}

func (t *SQLTracker) BeginCall(ctx context.Context, evt UsageEvent) error {
	if t == nil || t.db == nil {
		return fmt.Errorf("cost tracker database is not configured")
	}
	if strings.TrimSpace(evt.ID) == "" {
		return fmt.Errorf("cost call id is required")
	}
	started := evt.StartedAt.UTC()
	if started.IsZero() {
		started = time.Now().UTC()
	}
	_, err := t.db.ExecContext(ledgerCtx(ctx), `
		INSERT INTO llm_calls (
			id, session_id, parent_session_id, project_id, provider_id, model, caller,
			status, started_at, no_charge
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'started', ?, ?)
		ON CONFLICT(id) DO NOTHING
	`, evt.ID, evt.SessionID, evt.ParentSessionID, evt.ProjectID, evt.ProviderID, evt.Model,
		normalizeCaller(evt.Caller), db.FormatTime(started), boolInt(t.NoCharge(evt.ProviderID, evt.Model)))
	return err
}

func (t *SQLTracker) MarkCallUnknown(ctx context.Context, callID string) error {
	if t == nil || t.db == nil || strings.TrimSpace(callID) == "" {
		return nil
	}
	_, err := t.db.ExecContext(ledgerCtx(ctx), `
		UPDATE llm_calls
		SET status = 'unknown', completed_at = COALESCE(completed_at, ?)
		WHERE id = ? AND status = 'started'
	`, db.FormatTime(time.Now().UTC()), callID)
	return err
}

// VoidCall deletes a receipt before usage becomes observable.
func (t *SQLTracker) VoidCall(ctx context.Context, callID string) error {
	if t == nil || t.db == nil || strings.TrimSpace(callID) == "" {
		return nil
	}
	_, err := t.db.ExecContext(ledgerCtx(ctx), `
		DELETE FROM llm_calls
		WHERE id = ? AND status = 'started'
	`, callID)
	return err
}

// ClaimSpendWarning records one warning for each session and ceiling.
func (t *SQLTracker) ClaimSpendWarning(ctx context.Context, sessionID string, ceilingUSD float64) (bool, error) {
	if t == nil || t.db == nil {
		return false, fmt.Errorf("cost tracker database is not configured")
	}
	if strings.TrimSpace(sessionID) == "" || ceilingUSD <= 0 {
		return false, nil
	}
	ceilingNanoUSD, err := USDToNano(ceilingUSD)
	if err != nil || ceilingNanoUSD == 0 {
		return false, err
	}
	result, err := t.db.ExecContext(ledgerCtx(ctx), `
		INSERT INTO session_spend_warnings (session_id, ceiling_nano_usd, fired_at)
		VALUES (?, ?, ?)
		ON CONFLICT(session_id) DO UPDATE SET
			ceiling_nano_usd = excluded.ceiling_nano_usd,
			fired_at = excluded.fired_at
		WHERE session_spend_warnings.ceiling_nano_usd != excluded.ceiling_nano_usd
	`, sessionID, ceilingNanoUSD, db.FormatTime(time.Now().UTC()))
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ClearSpendWarning removes a session's warning latch.
func (t *SQLTracker) ClearSpendWarning(ctx context.Context, sessionID string) error {
	if t == nil || t.db == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	_, err := t.db.ExecContext(ledgerCtx(ctx), `DELETE FROM session_spend_warnings WHERE session_id = ?`, sessionID)
	return err
}

// NoCharge reports whether the active pricer rules out token charges.
func (t *SQLTracker) NoCharge(providerID, model string) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	pricer := t.pricer
	t.mu.RUnlock()
	return pricer != nil && pricer.NoCharge(providerID, model)
}

// ledgerCtx lets receipt writes finish after caller cancellation.
func ledgerCtx(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithoutCancel(ctx)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (t *SQLTracker) RecordUsage(ctx context.Context, evt UsageEvent) error {
	if t == nil || t.db == nil {
		return fmt.Errorf("cost tracker database is not configured")
	}
	if strings.TrimSpace(evt.ID) == "" {
		evt.ID = uuid.NewString()
	}
	started := evt.StartedAt.UTC()
	if started.IsZero() {
		started = time.Now().UTC()
	}
	if evt.EstimatedNanoUSD == nil {
		evt.UnpricedTokens = evt.PromptTokens + evt.CompletionTokens
	}
	if evt.UnpricedTokens > 0 {
		evt.Unpriced = true
	}
	if evt.RateSnapshot == nil {
		evt.UnpricedCacheTokens = evt.CacheReadInputTokens + evt.CacheCreationInputTokens
	}
	snapshot := "{}"
	if evt.RateSnapshot != nil {
		raw, err := json.Marshal(evt.RateSnapshot)
		if err != nil {
			return err
		}
		snapshot = string(raw)
	}
	var estimated any
	if evt.EstimatedNanoUSD != nil {
		estimated = *evt.EstimatedNanoUSD
	}
	var pricedAsOf any
	if !evt.PricedAsOf.IsZero() {
		pricedAsOf = db.FormatTime(evt.PricedAsOf.UTC())
	}
	unpriced := 0
	if evt.Unpriced {
		unpriced = 1
	}
	source := evt.UsageSource
	if source == "" {
		source = UsageFromProvider
	}
	_, err := t.db.ExecContext(ledgerCtx(ctx), `
		INSERT INTO llm_calls (
			id, session_id, parent_session_id, project_id, provider_id, model, caller,
			status, prompt_tokens, completion_tokens, cache_read_tokens, cache_write_tokens,
			estimated_nano_usd, unpriced, pricing_source, priced_as_of, started_at, completed_at,
			no_charge, usage_source, cache_write_1h_tokens, unpriced_tokens, rate_snapshot, cache_savings_nano_usd, unpriced_cache_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'reported', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			session_id = excluded.session_id,
			parent_session_id = excluded.parent_session_id,
			project_id = excluded.project_id,
			provider_id = excluded.provider_id,
			model = excluded.model,
			caller = excluded.caller,
			status = 'reported',
			prompt_tokens = excluded.prompt_tokens,
			completion_tokens = excluded.completion_tokens,
			cache_read_tokens = excluded.cache_read_tokens,
			cache_write_tokens = excluded.cache_write_tokens,
			estimated_nano_usd = excluded.estimated_nano_usd,
			unpriced = excluded.unpriced,
			pricing_source = excluded.pricing_source,
			priced_as_of = excluded.priced_as_of,
			completed_at = excluded.completed_at,
			usage_source = excluded.usage_source,
			cache_write_1h_tokens = excluded.cache_write_1h_tokens,
			unpriced_tokens = excluded.unpriced_tokens,
			rate_snapshot = excluded.rate_snapshot,
			cache_savings_nano_usd = excluded.cache_savings_nano_usd,
			unpriced_cache_tokens = excluded.unpriced_cache_tokens
	`, evt.ID, evt.SessionID, evt.ParentSessionID, evt.ProjectID, evt.ProviderID, evt.Model,
		normalizeCaller(evt.Caller), evt.PromptTokens, evt.CompletionTokens,
		evt.CacheReadInputTokens, evt.CacheCreationInputTokens, estimated, unpriced,
		evt.PricingSource, pricedAsOf, db.FormatTime(started), db.FormatTime(time.Now().UTC()),
		boolInt(t.NoCharge(evt.ProviderID, evt.Model)), string(source), evt.CacheCreation1HInputTokens, evt.UnpricedTokens, snapshot, evt.CacheSavingsNanoUSD, evt.UnpricedCacheTokens)
	return err
}

func (t *SQLTracker) Estimate(_ context.Context, providerID, model string, usage TokenUsage) (CostEstimate, error) {
	t.mu.RLock()
	pricer := t.pricer
	t.mu.RUnlock()
	if pricer == nil {
		return CostEstimate{Currency: "USD", Unpriced: true}, nil
	}
	return pricer.EstimateCost(providerID, model, usage)
}

func (t *SQLTracker) SetPricer(pricer Pricer) {
	t.mu.Lock()
	t.pricer = pricer
	t.mu.Unlock()
}

var _ CostTracker = (*SQLTracker)(nil)
var _ CallLedger = (*SQLTracker)(nil)
