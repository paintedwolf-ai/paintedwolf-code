package history

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/llm/failure"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/messageview"
	"github.com/lycaon/lycaon/internal/session/promptassembly"
	"github.com/lycaon/lycaon/pkg/api"
)

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
			transcript := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(candidate.Messages))
			observed := calibration.ProjectBilled(transcript, 0)
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
	if budget.Window <= 0 || budget.Input+budget.Output+budget.Headroom <= budget.Window {
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
	ceiling := budget.Window - budget.Headroom - reserve
	if req.MaxTokens <= 0 && budget.Input <= ceiling {
		req.MaxTokens = budget.Window - budget.Headroom - budget.Input
		return req, nil
	}
	frame := req
	frame.Messages = projection.frame
	frameBudget := measure(frame)
	if req.MaxTokens <= 0 && ceiling-frameBudget.Input < 512 {
		if availableAfterFrame := budget.Window - budget.Headroom - frameBudget.Input; availableAfterFrame > max(128, budget.MinimumOutput) {
			reserve = max(max(128, budget.MinimumOutput), min(reserve, availableAfterFrame/2))
			ceiling = budget.Window - budget.Headroom - reserve
		}
	}
	fullHistory := compaction.EstimateMessagesTokens(compaction.ContextMessagesFromAPI(projection.history))
	capacity := max(1, (ceiling-frameBudget.Input)*fullHistory/max(1, budget.Input-frameBudget.Input))
	record := m.requestContinuation(ctx, sess, projection.history)
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
			if req.MaxTokens <= 0 && available >= max(128, budget.MinimumOutput) {
				candidate.MaxTokens = available
				return candidate, nil
			}
			break
		}
		capacity = max(1, min(capacity-1, capacity*3/4))
	}
	return req, &failure.ProviderContextTooSmallError{ProviderID: provider, Model: model,
		PromptTokens: budget.Input, CompletionReserve: reserve, MaxContext: budget.Window}
}

func (m *Service) requestContinuation(ctx context.Context, sess *api.Session, rows []api.Message) *messageview.Continuation {
	record := &messageview.Continuation{Format: messageview.ContinuationFormat, Reason: "references_only"}
	if m.store != nil {
		view, ok, err := m.store.GetCompactionView(ctx, sess.ID)
		if err == nil && ok && view.Continuation != nil {
			if current, err := m.store.CompactionViewCurrent(ctx, sess.ID, view.CoveredThroughOrd, view.CoveredThroughID, view.SourceSeq); err == nil && current {
				record = messageview.CloneContinuation(view.Continuation)
			}
		}
	}
	current, _ := api.LastUserIntentMessage(rows)
	for _, row := range rows {
		if row.ID != current.ID {
			record.ThroughOrd = max(record.ThroughOrd, row.Ord)
		}
	}
	return record
}

func (m *Service) fitRequestHistory(ctx context.Context, sess *api.Session, history []api.Message, record *messageview.Continuation, cfg compaction.CompactionConfig, tokens int, shrinkTools bool) ([]api.Message, error) {
	rows := append([]api.Message(nil), history...)
	if shrinkTools && m.store != nil && m.recallAvailable != nil && m.recallAvailable(ctx, sess) {
		rows = projectRecordedToolBodies(ctx, rows)
	}
	found, text := false, ""
	if record.ThroughOrd > 0 {
		record = messageview.CloneContinuation(record)
		record.Window = cfg.ModelContextWindow
		recall := m.recallAvailable != nil && m.recallAvailable(ctx, sess)
		var err error
		text, err = compaction.RenderContinuation(ctx, record, recall, max(192, tokens/3))
		if err != nil {
			return nil, err
		}
		kept := rows[:0:0]
		for _, row := range rows {
			if record.Level == "references" && row.CompactionCheckpoint {
				continue
			}
			if len(row.ContentParts) == 1 && row.ContentParts[0].Source == "compaction_summary" || row.ID == "host-history-coverage" {
				row.Content = text
				row.ContentParts = append([]api.MessageContentPart(nil), row.ContentParts...)
				if len(row.ContentParts) == 1 {
					row.ContentParts[0].Content = text
				}
				row.ContextPinned = true
				found = true
			}
			kept = append(kept, row)
		}
		rows = kept
	}
	rows = promptassembly.MarkContextPins(sess, rows, min(8192, max(0, tokens/5)))
	contexts := compaction.ContextMessagesFromAPI(rows)
	fitted := compaction.DeterministicFit(cfg, contexts, tokens)
	if !found && text != "" && requestHistoryDropped(rows, fitted) {
		reference := compaction.ContextMessage{ID: "host-request-history-reference", Role: "system",
			Origin: api.MessageOriginHost, Authority: api.ContentAuthoritySystem, TrustTier: api.ContentTrustTierTrusted,
			Content: text, ContextPinned: true}
		fitted = compaction.DeterministicFit(cfg, append([]compaction.ContextMessage{reference}, contexts...), tokens)
	}
	if shrinkTools && text != "" {
		kept := fitted[:0:0]
		for _, row := range fitted {
			if row.ID != "host-context-trim-notice" {
				kept = append(kept, row)
			}
		}
		fitted = kept
	}
	return compaction.ContextMessagesToAPI(fitted, rows), nil
}

func requestHistoryDropped(original []api.Message, fitted []compaction.ContextMessage) bool {
	kept := make(map[string]bool, len(fitted))
	for _, row := range fitted {
		kept[row.ID] = true
	}
	for _, row := range original {
		if !kept[row.ID] {
			return true
		}
	}
	return false
}
