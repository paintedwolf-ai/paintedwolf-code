package history

import (
	"context"
	"math"
	"slices"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/pkg/api"
)

type RequestBudgets interface {
	RequestBudget(context.Context, string, modelcall.CompletionRequest) modelcall.RequestBudget
}

// FitRequest reserves the live frame before allocating history and output, and
// reports where fitted history starts so prefix reuse can be attributed.
func (m *Service) FitRequest(ctx context.Context, sess *api.Session, history []api.Message, req modelcall.CompletionRequest, budgets RequestBudgets, policies RequestPolicies) (modelcall.CompletionRequest, error) {
	history, req, err := prepareRequestHistory(ctx, m, sess, history, req)
	if err != nil {
		return req, err
	}
	fitted, err := m.fitRequest(ctx, sess, history, req, budgets, policies)
	if err == nil {
		err = validateRequestIntent(history, fitted.Messages)
	}
	if err == nil && m != nil && sess != nil {
		fitted.Debug.HistoryCut = m.cutoffs.takeFacts(sess.ID)
	}
	return fitted, err
}

func (m *Service) fitRequest(ctx context.Context, sess *api.Session, history []api.Message, req modelcall.CompletionRequest, budgets RequestBudgets, policies RequestPolicies) (modelcall.CompletionRequest, error) {
	var policyGroups [][]api.Message
	if policies != nil {
		policyGroups = policies.PolicyMessageGroups(req.Messages)
	}
	cfg := m.compactionConfig(ctx, sess)
	provider, model := m.Model(ctx, sess)
	if model != "" {
		req.Model = model
	} else {
		model = req.Model
	}
	projection := projectRequest(req.Messages, history)
	projected := messageview.PreserveRequestIntent(m.ApplyView(ctx, sess, projection.history), projection.history)
	view := m.requestView(ctx, sess)
	key := m.cutoffKey(ctx, sess, cfg, view)
	note := func(rows []api.Message) {
		if m != nil && sess != nil {
			m.cutoffs.noteFit(key, sess.ID, projection.history, rows)
		}
	}
	projected, omission, err := m.applyFitCut(ctx, sess, key, view, projected, cfg)
	if err != nil {
		return req, err
	}
	req.Messages = projection.replace(projected)
	projection = projectRequest(req.Messages, projected)
	measure := func(candidate modelcall.CompletionRequest) modelcall.RequestBudget {
		budget := modelcall.RequestBudget{Window: cfg.ModelContextWindow, Input: modelcall.EstimateRequestInput(candidate), Output: modelcall.OrchestrationMaxTokens, Headroom: min(1024, max(0, cfg.ModelContextWindow/8))}
		if budgets != nil {
			budget = budgets.RequestBudget(ctx, provider, candidate)
		}
		if budget.Err != nil {
			return budget
		}
		if calibration := m.Calibration(ctx, sess); !budget.Exact && calibration.Calibrated() {
			tools := modelcall.EstimateRequestInput(modelcall.CompletionRequest{Tools: candidate.Tools})
			transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(candidate.Messages))
			calibration.TranscriptEstimate += tools
			observed := calibration.ProjectBilled(transcript+tools, 0)
			// Composition changes need room beyond the last observed ratio.
			budget.Input = max(budget.Input, observed+(observed+9)/10)
		}
		if budget.Window <= 0 {
			budget.Window = cfg.ModelContextWindow
			budget.Headroom = min(budget.Headroom, max(0, budget.Window/8))
		}
		return budget
	}
	budget := measure(req)
	if budget.Err != nil {
		return req, budget.Err
	}
	hard := cfg.HardCeilingTokens
	if budget.Window <= 0 || (hard <= 0 || budget.Input <= hard) && budget.Input+budget.Output+budget.Headroom <= budget.Window {
		return req, nil
	}
	reserve := req.MaxTokens
	if reserve <= 0 {
		reserve = max(128, min(4096, budget.Window/8))
		if budget.Output > 0 {
			reserve = min(reserve, budget.Output)
		}
		reserve = max(reserve, budget.MinimumOutput)
	}
	frame := req
	frame.Messages = projection.frame
	frameBudget := measure(frame)
	if frameBudget.Err != nil {
		return req, frameBudget.Err
	}
	inputLimit := requestInputLimit(hard, frameBudget.Input)
	ceiling := min(inputLimit, budget.Window-budget.Headroom-reserve)
	if req.MaxTokens <= 0 && budget.Input <= ceiling {
		req.MaxTokens = budget.Window - budget.Headroom - budget.Input
		return req, nil
	}
	if req.MaxTokens <= 0 {
		if availableAfterFrame := budget.Window - budget.Headroom - frameBudget.Input; availableAfterFrame > max(128, budget.MinimumOutput) {
			reserve = max(max(128, budget.MinimumOutput), min(reserve, availableAfterFrame/4))
			ceiling = min(inputLimit, budget.Window-budget.Headroom-reserve)
		}
		if budget.Input <= ceiling {
			req.MaxTokens = budget.Window - budget.Headroom - budget.Input
			return req, nil
		}
	}
	fullHistory := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(projection.history))
	capacity := max(1, (ceiling-frameBudget.Input)*fullHistory/max(1, budget.Input-frameBudget.Input))
	var record *messageview.Continuation
	if view != nil && view.Continuation != nil {
		record = messageview.CloneContinuation(view.Continuation)
	}
	for step := 0; step < 12; step++ {
		if step == 11 {
			capacity = 1
		}
		shrinkTools := capacity <= 1 || hasOversizedToolBody(projection.history)
		rows, err := m.fitRequestHistory(ctx, sess, projection.history, omission, record, cfg, capacity, shrinkTools)
		if err != nil {
			return req, err
		}
		candidate := req
		candidate.Messages = projection.replace(rows)
		budget = measure(candidate)
		if budget.Err != nil {
			return req, budget.Err
		}
		if budget.Input <= ceiling {
			if req.MaxTokens <= 0 && (budget.Output <= 0 || budget.Input+budget.Output+budget.Headroom > budget.Window) {
				candidate.MaxTokens = budget.Window - budget.Headroom - budget.Input
			}
			note(rows)
			return candidate, nil
		}
		if capacity <= 1 {
			available := budget.Window - budget.Headroom - budget.Input
			if req.MaxTokens <= 0 && budget.Input <= inputLimit && available >= max(128, budget.MinimumOutput) {
				candidate.MaxTokens = available
				note(rows)
				return candidate, nil
			}
			break
		}
		capacity = max(1, min(capacity-1, capacity*3/4))
	}
	if policies != nil {
		if fitted, deferred, err := m.fitPolicyFrame(ctx, sess, req, projection.history, omission, record, cfg, budgets, policies, policyGroups, measure, reserve); deferred || err != nil {
			return fitted, err
		}
	}
	return req, &failure.ProviderContextTooSmallError{ProviderID: provider, Model: model,
		PromptTokens: budget.Input, CompletionReserve: reserve, MaxContext: min(budget.Window, inputLimit+reserve)}
}

// requestInputLimit caps input at the hard ceiling. A standing frame that alone
// approaches the ceiling, as on a small local model, keeps a quarter of it for
// history rather than refusing every turn; the provider window still applies.
func requestInputLimit(hard, frameInput int) int {
	if hard <= 0 {
		return math.MaxInt / 2
	}
	return max(hard, frameInput+hard/4)
}

// requestView returns the published view only while its source is current;
// omissions are named separately from what it covers.
func (m *Service) requestView(ctx context.Context, sess *api.Session) *store.CompactionView {
	if m == nil || m.store == nil || sess == nil {
		return nil
	}
	view, ok, err := m.store.GetCompactionView(ctx, sess.ID)
	if err != nil || !ok {
		return nil
	}
	if current, err := m.store.CompactionViewCurrent(ctx, sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq); err != nil || !current {
		return nil
	}
	return view
}

// requestOmission is what later fitting needs to name an interval unchanged:
// the coverage it starts after and any row a held cut already removed.
type requestOmission struct {
	view           *store.CompactionView
	firstDropped   int64
	throughDropped int64
}

// applyFitCut replays a held fit cutoff, so requests after the one that chose it
// render the same rows and notice without refitting.
func (m *Service) applyFitCut(ctx context.Context, sess *api.Session, key cutoffKey, view *store.CompactionView, rows []api.Message, cfg compaction.CompactionConfig) ([]api.Message, requestOmission, error) {
	omission := requestOmission{view: view}
	if m == nil || sess == nil || len(rows) == 0 {
		return rows, omission, nil
	}
	held, bodies := m.cutoffs.heldFit(key, sess.ID, rows)
	if bodies > 0 {
		rows = projectToolBodiesThrough(ctx, rows, bodies)
	}
	if held == 0 {
		return rows, omission, nil
	}
	kept, firstDropped := rowsFromCut(promptassembly.MarkContextPins(sess, rows, min(8192, max(0, cfg.HardCeilingTokens/5))), held)
	_, throughDropped := omittedOrdinals(rows, compaction.ContextMessagesFromAPI(kept))
	omission.firstDropped = firstDropped
	omission.throughDropped = throughDropped
	out, err := m.withOmissionNotice(ctx, sess, kept, omission, firstDropped, throughDropped, true)
	return out, omission, err
}

// rowsFromCut drops unpinned canonical rows below held and any earlier notice.
func rowsFromCut(rows []api.Message, held int64) ([]api.Message, int64) {
	bounds := indexDraftExchanges(rows)
	kept := rows[:0:0]
	firstDropped := int64(0)
	for _, row := range rows {
		lo := bounds.minOrd(row)
		switch {
		case row.ID == historyCoverageNoticeID:
		case row.Ord > 0 && lo < held && !row.ContextPinned:
			if firstDropped == 0 || lo < firstDropped {
				firstDropped = lo
			}
		default:
			kept = append(kept, row)
		}
	}
	return kept, firstDropped
}

// interval returns where the notice goes and the ordinals it names, derived the
// same way whether this request cut the rows or an earlier one did.
func (o requestOmission) interval(kept []api.Message, firstDropped, throughDropped int64) (at int, from, through int64) {
	if o.firstDropped > 0 && (firstDropped == 0 || o.firstDropped < firstDropped) {
		firstDropped = o.firstDropped
	}
	throughDropped = max(throughDropped, o.throughDropped)
	covered := int64(0)
	if o.view != nil {
		covered = o.view.CoveredThroughOrd
	}
	at = slices.IndexFunc(kept, func(row api.Message) bool {
		if row.Ord <= 0 {
			return false
		}
		if throughDropped > 0 {
			return row.Ord > throughDropped && (!row.ContextPinned || row.Ord > covered)
		}
		return !row.ContextPinned && (row.Ord > covered || firstDropped > 0)
	})
	if at < 0 {
		return 0, 1, 0
	}
	through = kept[at].Ord - 1
	for slices.ContainsFunc(kept, func(row api.Message) bool { return row.Ord == through }) {
		through--
	}
	from = covered + 1
	if firstDropped > 0 {
		from = min(from, firstDropped)
	}
	return at, from, through
}

func (m *Service) withOmissionNotice(ctx context.Context, sess *api.Session, kept []api.Message, omission requestOmission, firstDropped, throughDropped int64, prior bool) ([]api.Message, error) {
	if firstDropped == 0 && omission.firstDropped == 0 && !prior {
		return kept, nil
	}
	at, from, through := omission.interval(kept, firstDropped, throughDropped)
	if from > through {
		return kept, nil
	}
	notice, err := omissionNotice(ctx, m.recallAvailable, sess, historyCoverageNoticeID, from, through)
	if err != nil {
		return nil, err
	}
	return slices.Insert(slices.Clone(kept), at, *notice), nil
}

func (m *Service) fitRequestHistory(ctx context.Context, sess *api.Session, history []api.Message, omission requestOmission, record *messageview.Continuation, cfg compaction.CompactionConfig, tokens int, shrinkTools bool) ([]api.Message, error) {
	rows := make([]api.Message, 0, len(history))
	prior := false
	for i, row := range history {
		if row.ID == historyCoverageNoticeID {
			prior = true
			for _, after := range history[i+1:] {
				if after.Ord > 0 {
					omission.throughDropped = max(omission.throughDropped, after.Ord-1)
					break
				}
			}
			continue
		}
		rows = append(rows, row)
	}
	recall := m != nil && m.recallAvailable != nil && m.recallAvailable(ctx, sess)
	if record != nil && record.ThroughOrd > 0 {
		record = messageview.CloneContinuation(record)
		record.Window = cfg.ModelContextWindow
		text, err := compaction.RenderContinuation(ctx, record, recall, max(192, tokens/3))
		if err != nil {
			return nil, err
		}
		kept := rows[:0:0]
		for _, row := range rows {
			if record.Level == "references" && row.CompactionCheckpoint {
				continue
			}
			if len(row.ContentParts) == 1 && row.ContentParts[0].Source == "compaction_summary" {
				row.Content = text
				row.ContentParts = append([]api.MessageContentPart(nil), row.ContentParts...)
				row.ContentParts[0].Content = text
				row.ContextPinned = true
			}
			kept = append(kept, row)
		}
		rows = kept
	} else {
		// An unvalidated summary establishes nothing; dropped rows get a named omission.
		kept := rows[:0:0]
		for _, row := range rows {
			if !row.CompactionCheckpoint && !(len(row.ContentParts) == 1 && row.ContentParts[0].Source == "compaction_summary") {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	lastOrd := int64(0)
	for _, row := range rows {
		lastOrd = max(lastOrd, row.Ord)
	}
	noticeTokens := 0
	if lastOrd > 0 {
		sized, err := omissionNotice(ctx, m.recallAvailable, sess, historyCoverageNoticeID, lastOrd, lastOrd)
		if err != nil {
			return nil, err
		}
		noticeTokens = compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{*sized}))
	}
	rows = promptassembly.MarkContextPins(sess, rows, min(8192, max(0, tokens/5)))
	reserve := fitReserve(tokens)
	canShrinkTools := shrinkTools && m != nil && m.store != nil && recall
	if canShrinkTools {
		// Sized below the whole allocation so the projection and tail hold across appends.
		toolTokens := max(1, tokens-noticeTokens-reserve)
		rows = extendBodyProjection(ctx, rows, projectRecordedToolBodies(ctx, rows, toolTokens))
		if retained := retainedToolTailMessages(rows, toolTokens); retained > 0 {
			cfg.KeepRecentMessages = retained
		}
	}
	contexts := compaction.ContextMessagesFromAPI(rows)
	fitted := compaction.DeterministicFitWithReserve(cfg, contexts, tokens, reserve)
	if canShrinkTools && compaction.EstimateMessagesTokens(fitted) > tokens {
		rows = projectRecordedToolBodies(ctx, rows)
		cfg.KeepRecentMessages = max(1, retainedToolTailMessages(rows, 0))
		contexts = compaction.ContextMessagesFromAPI(rows)
		fitted = compaction.DeterministicFitWithReserve(cfg, contexts, tokens, reserve)
	}
	firstDropped, through := omittedOrdinals(rows, fitted)
	if through > 0 {
		var err error
		if fitted, err = m.fitWithOmissionNotice(ctx, sess, cfg, contexts, rows, tokens, reserve, firstDropped, through); err != nil {
			return nil, err
		}
		kept := fitted[:0:0]
		for _, row := range fitted {
			if row.ID != historyCoverageNoticeID && row.ID != "host-context-trim-notice" {
				kept = append(kept, row)
			}
		}
		fitted = kept
		firstDropped, through = omittedOrdinals(rows, fitted)
	}
	return m.withOmissionNotice(ctx, sess, compaction.ContextMessagesToAPI(fitted, rows), omission, firstDropped, through, prior)
}

// fitWithOmissionNotice refits with the notice pinned; its cost can widen the
// omission once, and the final text names the interval actually left out.
func (m *Service) fitWithOmissionNotice(ctx context.Context, sess *api.Session, cfg compaction.CompactionConfig, contexts []compaction.ContextMessage, rows []api.Message, tokens, reserve int, from, through int64) ([]compaction.ContextMessage, error) {
	var fitted []compaction.ContextMessage
	for range 2 {
		notice, err := omissionNotice(ctx, m.recallAvailable, sess, historyCoverageNoticeID, from, through)
		if err != nil {
			return nil, err
		}
		reference := compaction.ContextMessage{ID: notice.ID, Role: string(notice.Role),
			ContextMessageProvenance: compaction.ContextMessageProvenance{
				Origin: notice.Origin, Authority: notice.Authority, TrustTier: notice.TrustTier,
			},
			Content: notice.Content, ContextPinned: true}
		fitted = compaction.DeterministicFitWithReserve(cfg, append([]compaction.ContextMessage{reference}, contexts...), tokens, reserve)
		nextFrom, nextThrough := omittedOrdinals(rows, fitted)
		if nextFrom == from && nextThrough == through {
			break
		}
		from, through = nextFrom, nextThrough
	}
	return fitted, nil
}

// omittedOrdinals spans the canonical rows a fit left out; zero means none.
func omittedOrdinals(original []api.Message, fitted []compaction.ContextMessage) (int64, int64) {
	bounds := indexDraftExchanges(original)
	kept := make(map[string]bool, len(fitted))
	for _, row := range fitted {
		kept[row.ID] = true
	}
	var from, through int64
	for _, row := range original {
		if row.Ord <= 0 || kept[row.ID] {
			continue
		}
		lo := bounds.minOrd(row)
		if from == 0 || lo < from {
			from = lo
		}
		through = max(through, bounds.maxOrdFor(row))
	}
	return from, through
}
