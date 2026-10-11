package closeoutassembly

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// EvaluateVerdictGrounding checks citations against coordinator and required-reviewer evidence.
func (m *Service) EvaluateVerdictGrounding(
	ctx context.Context,
	sessionID string,
	cited []api.CitationGroundingCitedEvidence,
	citedURLs []string,
	owedAgents []string,
) (guidance.VerdictGroundingEval, error) {
	if m == nil || m.store == nil {
		return guidance.VerdictGroundingEval{}, nil
	}
	var roots evidence.CitationRoots
	if sess, err := m.store.Get(ctx, sessionID); err == nil && sess != nil {
		roots, _ = m.CitationRoots(ctx, sess)
	}
	history, err := m.store.GetMessages(ctx, sessionID)
	if err != nil {
		return guidance.VerdictGroundingEval{}, err
	}
	ev, err := guidance.UnionCloseoutEvidence(ctx, m.evidence, sessionID, history)
	if err != nil {
		return guidance.VerdictGroundingEval{}, err
	}
	reviewers, err := m.ReviewerEvidence(ctx, sessionID, history, owedAgents)
	if err != nil {
		return guidance.VerdictGroundingEval{}, err
	}
	eval := guidance.EvaluateVerdictGrounding(roots, ev, cited, citedURLs, reviewers)
	if eval.Code != guidance.VerdictCitationsRequiredCode && eval.Code != guidance.VerdictReviewerUncitedCode {
		return eval, nil
	}
	forcingCode := eval.Code

	neededReviewers := reviewers
	if eval.Code == guidance.VerdictReviewerUncitedCode {
		neededReviewers = selectReviewerEvidence(reviewers, eval.UncitedReviewers)
	}
	boundEvidence, boundURLs, assembled := bindReviewerCloseoutEvidence(
		roots, ev, cited, citedURLs, neededReviewers, terminalWorkerSummaryCloseouts(history),
	)
	if assembled {
		bound := guidance.EvaluateVerdictGrounding(roots, ev, boundEvidence, boundURLs, reviewers)
		if bound.Code == "" && bound.Grounding != nil {
			bound.Grounding.HostAssembled = true
			bound.Grounding.HintCode = forcingCode
			return bound, nil
		}
		if bound.Code != guidance.VerdictCitationsRequiredCode && bound.Code != guidance.VerdictReviewerUncitedCode {
			return bound, nil
		}
		eval = bound
		neededReviewers = reviewers
		if eval.Code == guidance.VerdictReviewerUncitedCode {
			neededReviewers = selectReviewerEvidence(reviewers, eval.UncitedReviewers)
		}
	}

	boundEvidence, boundURLs, observed := bindObservedReviewerVerdictEvidence(
		boundEvidence, boundURLs, neededReviewers, ev.Ledger,
	)
	if !observed {
		return eval, nil
	}
	bound := guidance.EvaluateVerdictGrounding(roots, ev, boundEvidence, boundURLs, reviewers)
	if bound.Code != "" || bound.Grounding == nil {
		return bound, nil
	}
	bound.Grounding.HostAssembled = true
	bound.Grounding.Traced = false
	bound.Grounding.HintCode = forcingCode
	return bound, nil
}

// bindReviewerCloseoutEvidence reuses the validated citations already carried
// by completed reviewer envelopes, the same traced first tier the coordinator
// closeout assembler uses.
func bindReviewerCloseoutEvidence(
	roots evidence.CitationRoots,
	ev guidance.CloseoutEvidence,
	cited []api.CitationGroundingCitedEvidence,
	citedURLs []string,
	reviewers []guidance.ReviewerEvidence,
	legs []legCloseout,
) ([]api.CitationGroundingCitedEvidence, []string, bool) {
	wanted := make(map[string]struct{}, len(reviewers))
	for _, reviewer := range reviewers {
		if agent := strings.TrimSpace(reviewer.Agent); agent != "" {
			wanted[agent] = struct{}{}
		}
	}
	selected := make([]legCloseout, 0, len(legs))
	for _, leg := range legs {
		if len(wanted) > 0 {
			if _, ok := wanted[strings.TrimSpace(leg.title)]; !ok {
				continue
			}
		}
		selected = append(selected, leg)
	}
	validated, validatedURLs := validatedLegCitations(roots, "implement_synthesis", ev, selected, "")
	out := append([]api.CitationGroundingCitedEvidence(nil), cited...)
	urls := append([]string(nil), citedURLs...)
	seenEvidence := make(map[string]struct{}, len(out))
	for _, item := range out {
		seenEvidence[verdictCitationKey(item)] = struct{}{}
	}
	seenURLs := make(map[string]struct{}, len(urls))
	for _, raw := range urls {
		if raw = strings.TrimSpace(raw); raw != "" {
			seenURLs[raw] = struct{}{}
		}
	}
	assembled := false
	for _, item := range validated {
		wire := api.CitationGroundingCitedEvidence{
			Handle: item.Evidence, Path: item.Path, Line: item.Line, Excerpt: item.Excerpt,
		}
		key := verdictCitationKey(wire)
		if _, dup := seenEvidence[key]; dup {
			continue
		}
		seenEvidence[key] = struct{}{}
		out = append(out, wire)
		assembled = true
	}
	for _, raw := range validatedURLs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, dup := seenURLs[raw]; dup {
			continue
		}
		seenURLs[raw] = struct{}{}
		urls = append(urls, raw)
		assembled = true
	}
	return out, urls, assembled
}

// bindObservedReviewerVerdictEvidence preserves reviewer attribution when
// closeout has no usable citation.
func bindObservedReviewerVerdictEvidence(
	cited []api.CitationGroundingCitedEvidence,
	citedURLs []string,
	reviewers []guidance.ReviewerEvidence,
	union evidence.Ledger,
) ([]api.CitationGroundingCitedEvidence, []string, bool) {
	out := append([]api.CitationGroundingCitedEvidence(nil), cited...)
	urls := append([]string(nil), citedURLs...)
	seenEvidence := make(map[string]struct{}, len(out))
	for _, item := range out {
		seenEvidence[verdictCitationKey(item)] = struct{}{}
	}
	seenURLs := make(map[string]struct{}, len(urls))
	for _, raw := range urls {
		if raw = strings.TrimSpace(raw); raw != "" {
			seenURLs[raw] = struct{}{}
		}
	}

	assembled := false
	for _, reviewer := range reviewers {
		bound := false
		for i, ledger := range reviewer.Ledgers {
			legID := ""
			if i < len(reviewer.LegIDs) {
				legID = strings.TrimSpace(reviewer.LegIDs[i])
			}
			for _, handle := range evidence.HandlesSorted(ledger) {
				item := api.CitationGroundingCitedEvidence{Handle: namespacedLegHandle(legID, handle)}
				key := verdictCitationKey(item)
				if _, dup := seenEvidence[key]; dup {
					bound = true
					break
				}
				seenEvidence[key] = struct{}{}
				out = append(out, item)
				assembled = true
				bound = true
				break
			}
			if bound {
				break
			}
			for _, rawURL := range evidence.ObservedURLsSorted(ledger) {
				rawURL = strings.TrimSpace(rawURL)
				if rawURL == "" {
					continue
				}
				if _, dup := seenURLs[rawURL]; !dup {
					seenURLs[rawURL] = struct{}{}
					urls = append(urls, rawURL)
					assembled = true
				}
				bound = true
				break
			}
			if bound {
				break
			}
		}
	}

	if len(out) == 0 && len(urls) == 0 {
		if handles := evidence.HandlesSorted(union); len(handles) > 0 {
			out = append(out, api.CitationGroundingCitedEvidence{Handle: handles[0]})
			assembled = true
		} else if observedURLs := evidence.ObservedURLsSorted(union); len(observedURLs) > 0 {
			urls = append(urls, observedURLs[0])
			assembled = true
		}
	}
	return out, urls, assembled
}

func verdictCitationKey(item api.CitationGroundingCitedEvidence) string {
	return strings.Join([]string{
		strings.TrimSpace(item.Handle),
		strings.TrimSpace(item.Path),
		strconv.Itoa(item.Line),
		strings.TrimSpace(item.Excerpt),
	}, "\x00")
}

func selectReviewerEvidence(all []guidance.ReviewerEvidence, agents []string) []guidance.ReviewerEvidence {
	wanted := make(map[string]struct{}, len(agents))
	for _, agent := range agents {
		if agent = strings.TrimSpace(agent); agent != "" {
			wanted[agent] = struct{}{}
		}
	}
	out := make([]guidance.ReviewerEvidence, 0, len(wanted))
	for _, reviewer := range all {
		if _, ok := wanted[strings.TrimSpace(reviewer.Agent)]; ok {
			out = append(out, reviewer)
		}
	}
	return out
}

// reviewerEvidence resolves completed reviewers from the same dispatch scope as closeout.
func (m *Service) ReviewerEvidence(ctx context.Context, sessionID string, history []api.Message, owedAgents []string) ([]guidance.ReviewerEvidence, error) {
	var since time.Time
	if intent, ok := api.LastUserIntentMessage(history); ok {
		since = intent.CreatedAt
	}
	tasks, err := m.evidence.Tasks(ctx, sessionID, since)
	if err != nil {
		return nil, err
	}
	phase := ""
	if m.workflows != nil {
		run, err := m.workflows.Runs.ActiveBySession(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if run != nil && !runstate.IsAmbientRun(run) {
			phase = run.CurrentPhase
		}
	}
	out := make([]guidance.ReviewerEvidence, 0, len(owedAgents))
	for _, agent := range owedAgents {
		r := guidance.ReviewerEvidence{Agent: agent}
		for _, task := range tasks {
			if task.AgentType != agent || !api.WorkerReviewSucceeded(task) || task.ChildSessionID == "" || (phase != "" && task.WorkflowPhase != phase) {
				continue
			}
			leg := guidance.EvidenceLeg{ChildSessionID: task.ChildSessionID, LegID: task.LegID}
			ledger, err := m.store.LoadLedger(ctx, task.ChildSessionID)
			if err != nil {
				return nil, err
			}
			r.LegIDs = append(r.LegIDs, leg.Namespace())
			r.Ledgers = append(r.Ledgers, ledger)
		}
		out = append(out, r)
	}
	return out, nil
}
