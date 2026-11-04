package history

import (
	"context"
	"math"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/pkg/api"
)

const requestOmissionNoticeID = "host-request-history-reference"

type RequestBudgets interface {
	RequestBudget(context.Context, string, modelcall.CompletionRequest) modelcall.RequestBudget
}

// FitRequest reserves the live frame before allocating history and output.
func (m *Service) FitRequest(ctx context.Context, sess *api.Session, history []api.Message, req modelcall.CompletionRequest, budgets RequestBudgets) (modelcall.CompletionRequest, error) {
	cfg := m.compactionConfig(ctx, sess)
	provider, model := m.Model(ctx, sess)
	if model != "" {
		req.Model = model
	} else {
		model = req.Model
	}
	projection := projectRequest(req.Messages, history)
	projected := m.ApplyView(ctx, sess, projection.history)
	req.Messages = projection.replace(projected)
	projection = projectRequest(req.Messages, projected)
	measure := func(candidate modelcall.CompletionRequest) modelcall.RequestBudget {
		budget := modelcall.RequestBudget{Window: cfg.ModelContextWindow, Input: modelcall.EstimateRequestInput(candidate), Output: modelcall.OrchestrationMaxTokens, Headroom: min(1024, max(0, cfg.ModelContextWindow/8))}
		if budgets != nil {
			budget = budgets.RequestBudget(ctx, provider, candidate)
		}
		if calibration := m.Calibration(ctx, sess); calibration.Calibrated() {
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
	record := m.requestContinuation(ctx, sess)
	for step := 0; step < 12; step++ {
		if step == 11 {
			capacity = 1
		}
		shrinkTools := capacity <= 1 || hasOversizedToolBody(projection.history)
		rows, err := m.fitRequestHistory(ctx, sess, projection.history, record, cfg, capacity, shrinkTools)
		if err != nil {
			return req, err
		}
		candidate := req
		candidate.Messages = projection.replace(rows)
		budget = measure(candidate)
		if budget.Input <= ceiling {
			if req.MaxTokens <= 0 && (budget.Output <= 0 || budget.Input+budget.Output+budget.Headroom > budget.Window) {
				candidate.MaxTokens = budget.Window - budget.Headroom - budget.Input
			}
			return candidate, nil
		}
		if capacity <= 1 {
			available := budget.Window - budget.Headroom - budget.Input
			if req.MaxTokens <= 0 && budget.Input <= inputLimit && available >= max(128, budget.MinimumOutput) {
				candidate.MaxTokens = available
				return candidate, nil
			}
			break
		}
		capacity = max(1, min(capacity-1, capacity*3/4))
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

// requestContinuation returns validated summary coverage only; omissions are named separately.
func (m *Service) requestContinuation(ctx context.Context, sess *api.Session) *messageview.Continuation {
	if m == nil || m.store == nil || sess == nil {
		return nil
	}
	view, ok, err := m.store.GetCompactionView(ctx, sess.ID)
	if err != nil || !ok || view.Continuation == nil {
		return nil
	}
	if current, err := m.store.CompactionViewCurrent(ctx, sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq); err != nil || !current {
		return nil
	}
	return messageview.CloneContinuation(view.Continuation)
}

func (m *Service) fitRequestHistory(ctx context.Context, sess *api.Session, history []api.Message, record *messageview.Continuation, cfg compaction.CompactionConfig, tokens int, shrinkTools bool) ([]api.Message, error) {
	rows := append([]api.Message(nil), history...)
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
		sized, err := m.omissionNotice(ctx, sess, requestOmissionNoticeID, lastOrd, lastOrd)
		if err != nil {
			return nil, err
		}
		noticeTokens = compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI([]api.Message{*sized}))
	}
	rows = promptassembly.MarkContextPins(sess, rows, min(8192, max(0, tokens/5)))
	canShrinkTools := shrinkTools && m != nil && m.store != nil && recall
	if canShrinkTools {
		toolTokens := max(1, tokens-noticeTokens)
		rows = projectRecordedToolBodies(ctx, rows, toolTokens)
		if retained := retainedToolTailMessages(rows, toolTokens); retained > 0 {
			cfg.KeepRecentMessages = retained
		}
	}
	contexts := compaction.ContextMessagesFromAPI(rows)
	fitted := compaction.DeterministicFit(cfg, contexts, tokens)
	if canShrinkTools && compaction.EstimateMessagesTokens(fitted) > tokens {
		rows = projectRecordedToolBodies(ctx, rows)
		cfg.KeepRecentMessages = max(1, retainedToolTailMessages(rows, 0))
		contexts = compaction.ContextMessagesFromAPI(rows)
		fitted = compaction.DeterministicFit(cfg, contexts, tokens)
	}
	if from, through := omittedOrdinals(rows, fitted); through > 0 {
		var err error
		if fitted, err = m.fitWithOmissionNotice(ctx, sess, cfg, contexts, rows, tokens, from, through); err != nil {
			return nil, err
		}
		if shrinkTools {
			kept := fitted[:0:0]
			for _, row := range fitted {
				if row.ID != "host-context-trim-notice" {
					kept = append(kept, row)
				}
			}
			fitted = kept
		}
	}
	return compaction.ContextMessagesToAPI(fitted, rows), nil
}

// fitWithOmissionNotice refits with the notice pinned; its cost can widen the
// omission once, and the final text names the interval actually left out.
func (m *Service) fitWithOmissionNotice(ctx context.Context, sess *api.Session, cfg compaction.CompactionConfig, contexts []compaction.ContextMessage, rows []api.Message, tokens int, from, through int64) ([]compaction.ContextMessage, error) {
	var fitted []compaction.ContextMessage
	for range 2 {
		notice, err := m.omissionNotice(ctx, sess, requestOmissionNoticeID, from, through)
		if err != nil {
			return nil, err
		}
		reference := compaction.ContextMessage{ID: notice.ID, Role: string(notice.Role), Origin: notice.Origin,
			Authority: notice.Authority, TrustTier: notice.TrustTier, Content: notice.Content, ContextPinned: true}
		fitted = compaction.DeterministicFit(cfg, append([]compaction.ContextMessage{reference}, contexts...), tokens)
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
	kept := make(map[string]bool, len(fitted))
	for _, row := range fitted {
		kept[row.ID] = true
	}
	var from, through int64
	for _, row := range original {
		if row.Ord <= 0 || kept[row.ID] {
			continue
		}
		if from == 0 {
			from = row.Ord
		}
		through = max(through, row.Ord)
	}
	return from, through
}
