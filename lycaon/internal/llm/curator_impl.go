package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/jsonfence"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/pkg/api"
)

var _ Curator = (*RegistrySummarizer)(nil)

type parsedCuration struct {
	Selections []evidence.Triple
	Gloss      []GlossLine
}

// Curate selects verbatim spans from snapshot using the lite summarizer model.
func (r *RegistrySummarizer) Curate(ctx context.Context, snapshot evidence.Ledger, focus CurationFocus, budget int) (CurationResult, error) {
	if budget <= 0 {
		budget = defaultCurationBudget
	}
	key := evidence.CurationCacheKey(snapshot, focus.CacheKey())
	if cached, ok := r.loadCuratorCache(key); ok {
		cached.Report.CacheHit = true
		logCuratorOutcome(ctx, focus, cached)
		return cached, nil
	}

	total := countSnapshotCandidates(snapshot)
	p, model, err := r.liteProvider()
	if err != nil || p == nil {
		out := emptyCurationResult(total, true)
		r.storeCuratorCache(key, out)
		logCuratorOutcome(ctx, focus, out)
		return out, nil //nolint:nilerr // no lite provider degrades to an empty curation, which the caller expects
	}

	systemPrompt, err := guidance.RenderCuratorSystemPrompt(ctx)
	if err != nil {
		out := emptyCurationResult(total, true)
		r.storeCuratorCache(key, out)
		logCuratorOutcome(ctx, focus, out)
		return out, nil //nolint:nilerr // a prompt render failure degrades to an empty curation, cached like any other
	}
	userPrompt, err := buildCuratorUserPrompt(ctx, snapshot, focus, budget)
	if err != nil {
		out := emptyCurationResult(total, true)
		r.storeCuratorCache(key, out)
		logCuratorOutcome(ctx, focus, out)
		return out, nil //nolint:nilerr // a prompt build failure degrades to an empty curation, cached like any other
	}

	var (
		unionSel   []MaterializedSelection
		unionGloss []GlossLine
		retries    int
		dropped    int
	)
	for attempt := 0; attempt <= curatorMaxRetries; attempt++ {
		raw, callErr := r.curateComplete(ctx, p, model, systemPrompt, userPrompt)
		if callErr != nil {
			if skipLiteError(callErr) || SecretScreenBlocked(callErr) {
				// A down slot is not a verdict about this snapshot, and a held
				// send is held again on retry.
				out := emptyCurationResult(total, true)
				logCuratorOutcome(ctx, focus, out)
				return out, nil
			}
			retries++
			if attempt == curatorMaxRetries {
				out := emptyCurationResult(total, true)
				r.storeCuratorCache(key, out)
				logCuratorOutcome(ctx, focus, out)
				return out, nil
			}
			continue
		}
		parsed, parseErr := parseCurationResponse(raw)
		if parseErr != nil {
			retries++
			if attempt == curatorMaxRetries {
				break
			}
			continue
		}
		kept, drop := resolveSelections(r.ProjectDir, snapshot, parsed.Selections, budget-len(unionSel))
		dropped += drop
		unionSel = unionSelections(unionSel, kept)
		unionGloss = unionGlossLines(unionGloss, fenceGloss(parsed.Gloss))
		break
	}

	out := buildCurationResult(unionSel, unionGloss, total, dropped, retries, false)
	r.storeCuratorCache(key, out)
	logCuratorOutcome(ctx, focus, out)
	return out, nil
}

func (r *RegistrySummarizer) curateComplete(ctx context.Context, p modelcall.Provider, model, systemPrompt, userPrompt string) (string, error) {
	format := CurateResponseFormat()
	today, err := guidance.RenderTodayLine(ctx, time.Now(), ResolveModelCutoff(model))
	if err != nil {
		return "", err
	}
	req := modelcall.CompletionRequest{
		Model: model,
		Messages: transcript.Project([]api.Message{
			{Role: api.MessageRoleSystem, Content: today + "\n\n" + systemPrompt, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted},
			{Role: api.MessageRoleUser, Content: userPrompt, Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted},
		}),
		// The purpose label plus the session identity stamped at the utility
		// funnel make this call's capture row self-attributing.
		Debug: modelcall.RequestDebug{Purpose: "curate"},
		// Structured selection over a bounded snapshot: thinking-mode
		// deliberation only adds latency and cost.
		Think:          modelcall.ThinkOff,
		ResponseFormat: format,
	}
	resp, err := r.completeUtility(ctx, p, model, req)
	if err != nil && providerretry.IsRequestRejected(err) {
		originalErr := err
		req.ResponseFormat = nil
		resp, err = r.completeUtility(ctx, p, model, req)
		if err != nil {
			return "", originalErr
		}
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func buildCuratorUserPrompt(ctx context.Context, snapshot evidence.Ledger, focus CurationFocus, budget int) (string, error) {
	data := map[string]any{
		"focus":      focus.PromptFocus(),
		"budget":     budget,
		"candidates": curatorCandidates(snapshot),
	}
	return guidance.RenderCuratorUserPrompt(ctx, data)
}

func curatorCandidates(snapshot evidence.Ledger) []map[string]any {
	var out []map[string]any
	for _, handle := range evidence.HandlesSorted(snapshot) {
		rec, ok := snapshot.Handles[handle]
		if !ok || rec.IsGate() || strings.TrimSpace(rec.Path) == "" {
			continue
		}
		out = append(out, map[string]any{
			"handle":  handle,
			"path":    rec.Path,
			"lines":   formatCandidateLines(rec.LineRanges),
			"preview": truncateCuratorPreview(strings.Join(rec.Body, "\n")),
		})
	}
	return out
}

func truncateCuratorPreview(text string) string {
	const maxRunes = 400
	return runeclamp.Clamp(strings.TrimSpace(text), maxRunes)
}

func formatCandidateLines(ranges []evidence.LineRange) string {
	if len(ranges) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		if r.Start <= 0 {
			continue
		}
		if r.End <= 0 || r.End == r.Start {
			parts = append(parts, fmt.Sprintf("%d", r.Start))
			continue
		}
		parts = append(parts, fmt.Sprintf("%d-%d", r.Start, r.End))
	}
	return strings.Join(parts, ",")
}

func countSnapshotCandidates(snapshot evidence.Ledger) int {
	n := 0
	for _, handle := range evidence.HandlesSorted(snapshot) {
		rec, ok := snapshot.Handles[handle]
		if !ok || rec.IsGate() || strings.TrimSpace(rec.Path) == "" {
			continue
		}
		n++
	}
	return n
}

func emptyCurationResult(total int, fallback bool) CurationResult {
	return CurationResult{
		Report: CurationReport{
			Total:    total,
			Fallback: fallback,
		},
	}
}

func buildCurationResult(selections []MaterializedSelection, gloss []GlossLine, total, dropped, retries int, fallback bool) CurationResult {
	return CurationResult{
		Selections: selections,
		Gloss:      gloss,
		Report: CurationReport{
			Selected: len(selections),
			Total:    total,
			Dropped:  dropped,
			Retries:  retries,
			Fallback: fallback,
		},
	}
}

func resolveSelections(projectDir string, snapshot evidence.Ledger, triples []evidence.Triple, limit int) ([]MaterializedSelection, int) {
	if limit <= 0 {
		return nil, len(triples)
	}
	var out []MaterializedSelection
	dropped := 0
	for _, triple := range triples {
		if len(out) >= limit {
			dropped++
			continue
		}
		res := evidence.Resolve(evidence.CitationRoots{ProjectDir: projectDir}, triple, snapshot, "")
		if res.Verdict != evidence.VerdictMatched {
			dropped++
			continue
		}
		lines := evidence.Materialize(snapshot, evidence.Span{
			Path:       res.Path,
			LineRanges: []evidence.LineRange{{Start: res.Line, End: res.Line}},
		})
		if len(lines) == 0 {
			dropped++
			continue
		}
		out = append(out, MaterializedSelection{
			Triple:     triple,
			Resolution: res,
			Lines:      lines,
		})
	}
	return out, dropped
}

func unionSelections(dst []MaterializedSelection, add []MaterializedSelection) []MaterializedSelection {
	seen := map[string]struct{}{}
	for _, s := range dst {
		seen[selectionKey(s.Triple)] = struct{}{}
	}
	for _, s := range add {
		k := selectionKey(s.Triple)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		dst = append(dst, s)
	}
	return dst
}

func selectionKey(t evidence.Triple) string {
	return strings.Join([]string{
		strings.TrimSpace(t.Path),
		fmt.Sprintf("%d", t.Line),
		strings.TrimSpace(t.Excerpt),
	}, "\x00")
}

func unionGlossLines(dst, add []GlossLine) []GlossLine {
	seen := map[string]struct{}{}
	for _, g := range dst {
		seen[strings.TrimSpace(g.Label)] = struct{}{}
	}
	for _, g := range add {
		label := strings.TrimSpace(g.Label)
		if label == "" {
			continue
		}
		if _, ok := seen[label]; ok {
			continue
		}
		seen[label] = struct{}{}
		dst = append(dst, GlossLine{Label: label})
		if len(dst) >= maxGlossLines {
			break
		}
	}
	return dst
}

func fenceGloss(in []GlossLine) []GlossLine {
	var out []GlossLine
	for _, g := range in {
		label := strings.TrimSpace(g.Label)
		if label == "" || strings.Contains(label, "\n") {
			continue
		}
		label = runeclamp.Clamp(label, maxGlossLabelRunes)
		out = append(out, GlossLine{Label: label})
		if len(out) >= maxGlossLines {
			break
		}
	}
	return out
}

func parseCurationResponse(raw string) (parsedCuration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return parsedCuration{}, fmt.Errorf("empty curator response")
	}
	// Strict envelope parsing rejects surrounding prose and trailing content.
	payload, ok := jsonfence.ParseStrict(raw, decodeCurationPayload)
	if !ok {
		var probe curationPayload
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			return parsedCuration{}, err
		}
		return parsedCuration{}, fmt.Errorf("curator response is not a single JSON object")
	}
	var out parsedCuration
	for _, sel := range payload.Selections {
		if strings.TrimSpace(sel.Body) != "" || strings.TrimSpace(sel.Content) != "" {
			continue
		}
		path := strings.TrimSpace(sel.Path)
		excerpt := strings.TrimSpace(sel.Excerpt)
		if path == "" || sel.Line <= 0 || excerpt == "" {
			continue
		}
		out.Selections = append(out.Selections, evidence.Triple{
			Path:    path,
			Line:    sel.Line,
			Excerpt: excerpt,
		})
	}
	out.Gloss = payload.Gloss
	return out, nil
}

// curationPayload is the curator's wire shape. Body/Content are accepted so a
// selection that pastes file text instead of citing it can be recognized and
// dropped rather than silently treated as an excerpt.
type curationPayload struct {
	Selections []struct {
		Path    string `json:"path"`
		Line    int    `json:"line"`
		Excerpt string `json:"excerpt"`
		Body    string `json:"body"`
		Content string `json:"content"`
	} `json:"selections"`
	Gloss []GlossLine `json:"gloss"`
}

func decodeCurationPayload(candidate string) (curationPayload, bool) {
	var out curationPayload
	if err := json.Unmarshal([]byte(candidate), &out); err != nil {
		return curationPayload{}, false
	}
	return out, true
}

func (r *RegistrySummarizer) loadCuratorCache(key string) (CurationResult, bool) {
	if r == nil {
		return CurationResult{}, false
	}
	r.curatorCacheMu.RLock()
	defer r.curatorCacheMu.RUnlock()
	if r.curatorCache == nil {
		return CurationResult{}, false
	}
	v, ok := r.curatorCache[key]
	if !ok {
		return CurationResult{}, false
	}
	return v, true
}

func (r *RegistrySummarizer) storeCuratorCache(key string, result CurationResult) {
	if r == nil {
		return
	}
	r.curatorCacheMu.Lock()
	defer r.curatorCacheMu.Unlock()
	if r.curatorCache == nil {
		r.curatorCache = make(map[string]CurationResult)
	}
	r.curatorCache[key] = result
}

func logCuratorOutcome(ctx context.Context, focus CurationFocus, out CurationResult) {
	attrs := []any{
		slog.String("tool", focus.Tool),
		slog.String("view", focus.View),
		slog.String("target", focus.Target),
		slog.Int("selected", out.Report.Selected),
		slog.Int("total", out.Report.Total),
		slog.Int("dropped", out.Report.Dropped),
		slog.Int("retries", out.Report.Retries),
	}
	if out.Report.Fallback {
		attrs = append(attrs, slog.Bool("fallback", true))
	}
	if out.Report.CacheHit {
		attrs = append(attrs, slog.Bool("cache_hit", true))
	}
	if sess := curationctx.SessionFrom(ctx).SessionID; sess != "" {
		attrs = append(attrs, slog.String("session_id", sess))
	}
	slog.InfoContext(ctx, "tool_curator", attrs...)
}
