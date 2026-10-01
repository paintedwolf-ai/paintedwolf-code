// Package compaction fits, measures, and summarizes session context through an explicit summarizer contract.
package compaction

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/sourceref"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompactionReport describes what compaction changed.
type CompactionReport struct {
	TokensBefore         int
	TokensAfter          int
	ChunksCompacted      int
	SessionCompacted     bool
	CompactionGeneration int
	TargetTokens         int
	TargetMet            bool
	Reason               string
	CacheHit             bool
	MeasurementMethod    string
	TextTokensBefore     int
	TextTokensAfter      int
}

// SessionInfo supplies session metadata for compaction snapshots.
type SessionInfo struct {
	ID string
	// AgentType and ParentSessionID attribute worker summaries to their chat.
	AgentType            string
	ParentSessionID      string
	ProjectID            string
	ProjectDir           string
	HostDataDir          string
	MaxToolSpillBytes    int
	Posture              api.SessionPosture
	CompactionGeneration int
	Model                string
	ProviderID           string
	LatestUserRequestID  string
	Attempts             messageview.CompactionAttemptStore
	ProgressMarkdown     string
	// RecallAvailable permits recall instructions in the continuation record.
	RecallAvailable  bool
	ChunkProjections messageview.ChunkProjectionStore
}

// SimpleCompactor implements tiered chunk-first then session summarization compaction.
type SimpleCompactor struct {
	cfg     CompactionConfig
	chunks  *ChunkCompactor
	summary Summarizer
}

// NewSimpleCompactor constructs a production compactor.
func NewSimpleCompactor(cfg CompactionConfig, summary Summarizer) *SimpleCompactor {
	if cfg.SessionMinSavingsTokens <= 0 {
		cfg.SessionMinSavingsTokens = 256
	}
	if cfg.MinSavingsPct <= 0 {
		cfg.MinSavingsPct = 10
	}
	return &SimpleCompactor{
		cfg:     cfg,
		chunks:  NewChunkCompactor(cfg, summary),
		summary: summary,
	}
}

// Compact compacts messages to fit maxTokens and returns a report.
func (c *SimpleCompactor) Compact(ctx context.Context, info SessionInfo, messages []ContextMessage, maxTokens int) (out []ContextMessage, report CompactionReport, err error) {
	ctx = withCompactionSession(ctx, info)
	out = append([]ContextMessage(nil), messages...)
	for i := range out {
		if info.LatestUserRequestID != "" && out[i].ID == info.LatestUserRequestID {
			out[i].ContextPinned = true
		}
	}
	report = CompactionReport{TokensBefore: EstimateMessagesTokens(messages), TargetTokens: maxTokens, CompactionGeneration: info.CompactionGeneration}
	defer func() {
		report.TokensAfter = EstimateMessagesTokens(out)
		report.TargetMet = maxTokens > 0 && report.TokensAfter <= maxTokens
		measurement, measureErr := measureCompactionPair(info, messages, out)
		report.TextTokensBefore, report.TextTokensAfter, report.MeasurementMethod = measurement.Before, measurement.After, measurement.Method
		if err == nil {
			err = measureErr
		}
		if err != nil {
			report.Reason = "failed"
		}
		slog.DebugContext(ctx, "compaction evaluated", "session_id", info.ID,
			"reason", report.Reason, "cache_hit", report.CacheHit,
			"target_tokens", report.TargetTokens, "target_met", report.TargetMet,
			"tokens_before", report.TokensBefore, "tokens_after", report.TokensAfter,
			"measurement_method", report.MeasurementMethod,
			"text_tokens_before", report.TextTokensBefore, "text_tokens_after", report.TextTokensAfter,
			"chunks_compacted", report.ChunksCompacted, "session_compacted", report.SessionCompacted)
	}()
	if !c.cfg.Enabled || maxTokens <= 0 {
		report.Reason = "disabled"
		return
	}
	if report.TokensBefore <= maxTokens {
		report.Reason = "under_target"
		return
	}
	out, report.ChunksCompacted = c.compactOversizedChunks(ctx, info, out, maxTokens)
	if EstimateMessagesTokens(out) <= maxTokens {
		report.Reason = "target_reached"
		return
	}
	var compact []ContextMessage
	compact, report.Reason, report.CacheHit, err = c.compactSession(ctx, info, out, maxTokens)
	if err != nil {
		return
	}
	if compact != nil {
		out = compact
		report.SessionCompacted = true
	}
	return
}

func (c *SimpleCompactor) compactSession(ctx context.Context, info SessionInfo, messages []ContextMessage, maxTokens int) ([]ContextMessage, string, bool, error) {
	tail := retainedCompactionMessages(messages, c.cfg.KeepRecentMessages)
	if len(tail) == len(messages) {
		return nil, "protected_history", false, nil
	}
	// Even an empty summary cannot recover enough space from protected history.
	possible, err := c.usefulCompaction(info, messages, compactionReplacement(info, messages, tail, compactionSummary{}, ""), c.cfg.SessionMinSavingsTokens)
	if err != nil {
		return nil, "failed", false, err
	}
	if !possible {
		return nil, "insufficient_reclaimable_space", false, nil
	}
	revision, err := c.sessionRevision(ctx, info, messages, maxTokens)
	if err != nil {
		return nil, "failed", false, err
	}
	if info.Attempts != nil {
		prior, found, readErr := info.Attempts.GetCompactionAttempt(ctx, info.ID)
		if readErr != nil {
			return nil, "failed", false, readErr
		}
		if found && prior.Revision == revision {
			return nil, prior.Reason, true, nil
		}
	}
	structured, err := c.summarizeSession(ctx, info, SliceMessages(messages, c.cfg.MessageSlice))
	if err != nil {
		return nil, "failed", false, err
	}
	summary, err := renderContinuationRecord(ctx, structured, compactedSources(messages, tail), info.RecallAvailable)
	if err != nil {
		return nil, "failed", false, err
	}
	candidate := compactionReplacement(info, messages, tail, structured, summary)
	useful, err := c.usefulCompaction(info, messages, candidate, c.cfg.SessionMinSavingsTokens)
	if err != nil {
		return nil, "failed", false, err
	}
	if !useful {
		if info.Attempts != nil {
			if err := info.Attempts.PutCompactionAttempt(ctx, info.ID, messageview.CompactionAttempt{Revision: revision, Reason: "insufficient_savings"}); err != nil {
				return nil, "failed", false, err
			}
		}
		return nil, "insufficient_savings", false, nil
	}
	reason := "partial_reduction"
	if EstimateMessagesTokens(candidate) <= maxTokens {
		reason = "target_reached"
	}
	return candidate, reason, false, nil
}

func compactionReplacement(info SessionInfo, out, tail []ContextMessage, structured compactionSummary, summary string) []ContextMessage {
	checkpoint := ContextMessage{
		ID:                   uuid.NewString(),
		Role:                 string(api.MessageRoleUser),
		Content:              compactionCheckpointContent(info),
		Origin:               api.MessageOriginHost,
		Authority:            api.ContentAuthorityNone,
		TrustTier:            api.ContentTrustTierTrusted,
		ContentParts:         compactionCheckpointParts(info),
		CompactionCheckpoint: true,
		HostSecretRedaction:  compactedHostSecretRedaction(out, tail),
	}
	// The summary inherits the least-trusted tier of the removed content.
	summaryTier := compactedTrustFloor(out, tail)
	summaryMsg := ContextMessage{
		SourceContext: sourceref.Mentioned(structured.SourceContext, summary),
		ID:            uuid.NewString(),
		Role:          string(api.MessageRoleAssistant),
		Content:       summary,
		Origin:        api.MessageOriginModel,
		Authority:     api.ContentAuthorityNone,
		TrustTier:     summaryTier,
		ContentParts: []api.MessageContentPart{{
			Content:   summary,
			Origin:    api.MessageOriginModel,
			Authority: api.ContentAuthorityNone,
			TrustTier: summaryTier,
			Source:    "compaction_summary",
		}},
	}

	return append([]ContextMessage{checkpoint, summaryMsg}, tail...)
}

// maxCompactedRedactionSpans bounds checkpoint redaction identities.
const maxCompactedRedactionSpans = 32

// compactedHostSecretRedaction deduplicates absorbed redactions by rule identity.
// Their original offsets are retained only as provenance.
func compactedHostSecretRedaction(all, tail []ContextMessage) *api.HostSecretRedactionMeta {
	absorbed := removedCompactionMessages(all, tail)
	type spanIdentity struct {
		rule  string
		kind  api.RedactionKind
		field string
	}
	seen := make(map[spanIdentity]struct{})
	var spans []api.RedactedSpan
	for _, msg := range absorbed {
		for _, span := range msg.HostSecretRedaction.SpanList() {
			id := spanIdentity{rule: span.RuleID, kind: span.Kind, field: span.Field}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			spans = append(spans, span)
			if len(spans) >= maxCompactedRedactionSpans {
				return api.NewHostSecretRedactionMeta(spans)
			}
		}
	}
	return api.NewHostSecretRedactionMeta(spans)
}

func compactionCheckpointContent(info SessionInfo) string {
	var b strings.Builder
	b.WriteString("Prior conversation compacted. Continuation context follows.")
	if progress := strings.TrimSpace(info.ProgressMarkdown); progress != "" {
		b.WriteString("\n\n## Current progress checklist\n")
		b.WriteString(info.ProgressMarkdown)
	}
	return b.String()
}

func compactionCheckpointParts(info SessionInfo) []api.MessageContentPart {
	parts := []api.MessageContentPart{{
		Content:   "Prior conversation compacted. Continuation context follows.",
		Origin:    api.MessageOriginHost,
		Authority: api.ContentAuthorityNone,
		TrustTier: api.ContentTrustTierTrusted,
		Source:    "compaction",
	}}
	if progress := strings.TrimSpace(info.ProgressMarkdown); progress != "" {
		parts = append(parts, api.MessageContentPart{
			Content:   progress,
			Origin:    api.MessageOriginHost,
			Authority: api.ContentAuthorityNone,
			TrustTier: api.ContentTrustTierTrusted,
			Source:    "progress",
		})
	}
	return parts
}

func withCompactionSession(ctx context.Context, info SessionInfo) context.Context {
	return curationctx.WithSession(ctx, curationctx.Session{
		SessionID:       info.ID,
		Agent:           info.AgentType,
		ParentSessionID: info.ParentSessionID,
		ProjectID:       info.ProjectID,
		ProjectDir:      info.ProjectDir,
	})
}

func (c *SimpleCompactor) compactOversizedChunks(ctx context.Context, info SessionInfo, messages []ContextMessage, maxTokens int) ([]ContextMessage, int) {
	candidates := MergeChunkCandidates(
		FindOversizedChunks(messages, c.cfg, nil),
		FindPromptTailToolChunks(messages, c.cfg, nil),
	)
	compacted := 0
	attempted := 0
	for _, ch := range candidates {
		if ctx.Err() != nil || attempted >= c.cfg.ChunkMaxPerPass || (maxTokens >= 0 && EstimateMessagesTokens(messages) <= maxTokens) {
			break
		}
		minSavings := c.cfg.ChunkMinSavingsTokens
		if ch.PromptTail {
			minSavings = c.cfg.ChunkProtectedMinSavingsTokens
		}
		savings := ch.Tokens - c.cfg.ChunkTargetTokens
		if savings < minSavings {
			continue
		}
		ch.HostDataDir = info.HostDataDir
		ch.MaxToolSpillBytes = info.MaxToolSpillBytes
		ch.EvidenceHandles = messages[ch.MessageIndex].EvidenceHandles
		projection := c.projectChunk(ctx, info, ch, messages[ch.MessageIndex], minSavings)
		if projection.Attempted {
			attempted++
		}
		if !projection.Accepted {
			continue
		}
		messages[ch.MessageIndex] = projection.Message
		compacted++
	}
	return messages, compacted
}

// CompactOversizedChunksOnly shrinks individual oversized messages without session summarization.
func (c *SimpleCompactor) CompactOversizedChunksOnly(ctx context.Context, info SessionInfo, messages []ContextMessage) ([]ContextMessage, int) {
	if !c.cfg.Enabled {
		return messages, 0
	}
	ctx = withCompactionSession(ctx, info)
	out := append([]ContextMessage(nil), messages...)
	return c.compactOversizedChunks(ctx, info, out, -1)
}

func (c *SimpleCompactor) summarizeSession(ctx context.Context, info SessionInfo, sliced []ContextMessage) (compactionSummary, error) {
	if c.summary == nil {
		return compactionSummary{}, fmt.Errorf("summarizer not configured")
	}
	systemPrompt, err := guidance.RenderCompactionSystemPrompt(ctx, guidance.CompactionSessionSummarySystemRef)
	if err != nil {
		return compactionSummary{}, err
	}
	budgets := []int{c.cfg.SummaryInputTokens, c.cfg.SummaryRetryInputTokens}
	var lastErr error
	for attempt, inputTokens := range budgets {
		input, buildErr := BuildCompactionInput(ctx, info, sliced, inputTokens, c.cfg.SummaryMessageTokens)
		if buildErr != nil {
			return compactionSummary{}, buildErr
		}
		raw, summarizeErr := c.summary.Summarize(
			WithSummaryFormat(ctx), systemPrompt, input.Text, c.cfg.SummaryOutputTokens)
		if summarizeErr != nil {
			lastErr = summarizeErr
			if attempt == 0 && retryCompactionProviderError(summarizeErr) {
				continue
			}
			return compactionSummary{}, summarizeErr
		}
		structured, parseErr := parseCompactionSummary(raw)
		if parseErr != nil {
			lastErr = parseErr
			continue
		}
		structured.SourceContext = sourceref.ForResponse(input.Messages, raw)
		return structured, nil
	}
	return compactionSummary{}, lastErr
}

func retryCompactionProviderError(err error) bool {
	return errors.Is(err, failure.ErrProviderOutputTruncated) ||
		errors.Is(err, failure.ErrProviderEmptyCompletion) ||
		errors.Is(err, failure.ErrProviderContextTooSmall)
}

// compactedSources lists recallable rows absent from the full prompt.
func compactedSources(all, tail []ContextMessage) []CompactedSource {
	const maxSources = 16
	kept := retainedMessageIDs(tail)
	out := make([]CompactedSource, 0)
	seen := make(map[string]struct{})
	for _, msg := range all {
		_, inTail := kept[msg.ID]
		if inTail && msg.CompactedChunk == nil {
			continue
		}
		if !inTail && msg.CompactedChunk == nil && len(msg.EvidenceHandles) == 0 {
			continue
		}
		src := CompactedSource{
			Handles:   append([]string(nil), msg.EvidenceHandles...),
			Tool:      strings.TrimSpace(msg.ToolName),
			MessageID: strings.TrimSpace(msg.ID),
		}
		key := strings.Join(src.Handles, ",")
		if key == "" {
			key = "message:" + src.MessageID
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, src)
		if len(out) == maxSources {
			break
		}
	}
	return out
}

// alignCompactionTail removes tool results whose producing calls were dropped.
func alignCompactionTail(messages []ContextMessage) []ContextMessage {
	i := 0
	for i < len(messages) && messages[i].Role == string(api.MessageRoleTool) {
		i++
	}
	if i == 0 {
		out := make([]ContextMessage, len(messages))
		copy(out, messages)
		return out
	}
	return append([]ContextMessage(nil), messages[i:]...)
}

// EstimateTokens implements ContextCompactor.
func (c *SimpleCompactor) EstimateTokens(_ context.Context, text string) (int, error) {
	return tokenest.EstimateDefault(text), nil
}

// BudgetRemaining implements ContextCompactor.
func (c *SimpleCompactor) BudgetRemaining(_ context.Context, _ string, used int) (int, error) {
	return BudgetRemaining(c.cfg.TargetTokens, used), nil
}

// Config returns the active compaction configuration.
func (c *SimpleCompactor) Config() CompactionConfig {
	return c.cfg
}

// TruncateSummarizer is a deterministic fallback when lite model is unavailable.
type TruncateSummarizer struct{}

// Summarize returns a truncated excerpt as the summary.
func (TruncateSummarizer) Summarize(_ context.Context, _, userPrompt string, maxTokens int) (string, error) {
	return truncateHeadTail(userPrompt, maxTokens), nil
}

// ErrNoLiteProvider indicates that no lite model is configured.
var ErrNoLiteProvider = errors.New("no lite model configured")

// UnavailableSummarizer returns a failure without substitute content.
type UnavailableSummarizer struct{}

// Summarize returns ErrNoLiteProvider.
func (UnavailableSummarizer) Summarize(context.Context, string, string, int) (string, error) {
	return "", ErrNoLiteProvider
}

// MockSummarizer returns fixed summary text for tests.
type MockSummarizer struct {
	Text string
	Err  error
}

// Summarize returns configured mock text.
func (m MockSummarizer) Summarize(ctx context.Context, _, _ string, _ int) (string, error) {
	if m.Err != nil {
		return "", m.Err
	}
	if SummaryFormatRequested(ctx) {
		return mockCompactionSummary(m.Text)
	}
	if m.Text != "" {
		return m.Text, nil
	}
	return "Continue coordinator work from compacted context.", nil
}

// compactedTrustFloor preserves the least-trusted tier of absorbed content.
func compactedTrustFloor(before, tail []ContextMessage) api.ContentTrustTier {
	kept := make(map[string]struct{}, len(tail))
	for _, msg := range tail {
		if id := strings.TrimSpace(msg.ID); id != "" {
			kept[id] = struct{}{}
		}
	}
	tiers := make([]api.ContentTrustTier, 0, len(before))
	for _, msg := range before {
		if id := strings.TrimSpace(msg.ID); id != "" {
			if _, ok := kept[id]; ok {
				continue
			}
		}
		tiers = append(tiers, msg.TrustTier)
		for _, part := range msg.ContentParts {
			tiers = append(tiers, part.TrustTier)
		}
	}
	if len(tiers) == 0 {
		// Nothing removed, so the summary restates nothing.
		return api.ContentTrustTierTrusted
	}
	return api.FloorTrustTier(tiers...)
}

// ContextCompactor compacts session message history for LLM calls.
type ContextCompactor interface {
	Compact(ctx context.Context, info SessionInfo, messages []ContextMessage, maxTokens int) ([]ContextMessage, CompactionReport, error)
	EstimateTokens(ctx context.Context, text string) (int, error)
	BudgetRemaining(ctx context.Context, sessionID string, used int) (int, error)
	Config() CompactionConfig
}
