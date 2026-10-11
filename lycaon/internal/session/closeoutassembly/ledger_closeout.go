package closeoutassembly

import (
	"context"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/pkg/api"
)

const hostEmptyCloseoutSynthesis = "No grounded findings this batch."

// Assemble preserves the answer and attaches recorded references,
// or builds a report from recorded worker results when no answer is available.
func (m *Service) Assemble(
	ctx context.Context,
	root, surfaceID string,
	forcedBy []string,
	draftedContent string,
	retryCount int,
) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
	root = strings.TrimSpace(root)
	if m == nil || m.store == nil || root == "" {
		return guidance.CoordinatorCompletionReport{}, nil
	}
	var roots evidence.CitationRoots
	if sess, err := m.store.Get(ctx, root); err == nil && sess != nil {
		roots, _ = m.CitationRoots(ctx, sess)
	}
	history, err := m.store.GetMessages(ctx, root)
	if err != nil {
		history = nil
	}
	ev, err := guidance.UnionCloseoutEvidence(ctx, m.evidence, root, history)
	if err != nil {
		ev = guidance.CloseoutEvidence{Ledger: evidence.InitLedger()}
	}
	legs := m.terminalLegCloseouts(ctx, root, history)
	return assembleGroundedLedgerCloseout(roots, surfaceID, ev, legs, draftedContent, forcedBy, retryCount)
}

type legCloseout struct {
	key   string
	legID string
	title string
	brief string
	cited []api.CitationGroundingCitedEvidence
	urls  []string
}

func (m *Service) terminalLegCloseouts(ctx context.Context, root string, history []api.Message) []legCloseout {
	out := terminalWorkerSummaryCloseouts(history)
	seen := make(map[string]struct{}, len(out))
	for _, leg := range out {
		seen[leg.key] = struct{}{}
	}
	if m.delegations == nil {
		return out
	}
	delegationID, ok := m.delegations.DelegationBySessionID(root)
	if !ok {
		return out
	}
	legs, err := m.delegations.ListLegs(ctx, delegationID)
	if err != nil {
		return out
	}
	for _, leg := range legs {
		if leg.Status != api.LegStatusComplete || leg.Result == nil {
			continue
		}
		key := strings.TrimSpace(leg.WorkerID)
		if key == "" {
			key = strings.TrimSpace(leg.ID)
		}
		if _, dup := seen[key]; dup {
			continue
		}
		brief := strings.TrimSpace(leg.Result.Summary)
		if brief == "" {
			continue
		}
		item := legCloseout{
			key:   key,
			legID: strings.TrimSpace(leg.ID),
			title: strings.TrimSpace(leg.Title),
			brief: brief,
		}
		if leg.Result.Grounding != nil {
			item.cited = append([]api.CitationGroundingCitedEvidence(nil), leg.Result.Grounding.CitedEvidence...)
			item.urls = append([]string(nil), leg.Result.Grounding.CitedURLs...)
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func terminalWorkerSummaryCloseouts(history []api.Message) []legCloseout {
	since := api.UserIntentBoundary(history)
	seen := map[string]struct{}{}
	var out []legCloseout
	for i := since; i < len(history); i++ {
		msg := history[i]
		if msg.WorkerSummary == nil || !api.WorkerSummaryLegSucceeded(msg.WorkerSummary.Status) {
			continue
		}
		env, ok := workeroutcomes.ParseMessageWorkerEnvelope(msg)
		if !ok || workeroutcomes.EnvelopeLegStatus(env) != "complete" {
			continue
		}
		key := strings.TrimSpace(msg.WorkerSummary.WorkerID)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		brief := strings.TrimSpace(env.Report.Brief)
		if brief == "" {
			brief = strings.TrimSpace(env.Summary)
		}
		if brief == "" {
			brief = strings.TrimSpace(env.Body)
		}
		item := legCloseout{
			key:   key,
			legID: strings.TrimSpace(msg.WorkerSummary.LegID),
			title: strings.TrimSpace(msg.WorkerSummary.AgentType),
			brief: brief,
		}
		if item.legID == "" {
			item.legID = strings.TrimSpace(msg.WorkerSummary.ChildSessionID)
		}
		if grounding := msg.WorkerSummary.Grounding; grounding != nil {
			item.cited = append([]api.CitationGroundingCitedEvidence(nil), grounding.CitedEvidence...)
			item.urls = append([]string(nil), grounding.CitedURLs...)
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	return out
}

func assembleGroundedLedgerCloseout(
	roots evidence.CitationRoots,
	surfaceID string,
	ev guidance.CloseoutEvidence,
	legs []legCloseout,
	draftedContent string,
	forcedBy []string,
	retryCount int,
) (guidance.CoordinatorCompletionReport, *api.CitationGrounding) {
	// Every field the host could read from the draft survives; the emitter
	// records what it could not.
	synthesis := guidance.UsableCloseoutSynthesis(draftedContent)
	var report guidance.CoordinatorCompletionReport
	if synthesis != "" {
		read, _ := guidance.ReadCloseoutReport(draftedContent, "")
		draft := read.Report
		draft.Synthesis = synthesis
		if !guidance.TypedCitationChannelsEmpty(len(draft.CitedEvidence), len(draft.CitedURLs)) {
			return guidance.AssembleRetainedCloseout(roots, surfaceID, ev, draft, firstForcedByCode(forcedBy), retryCount)
		}
		report = draft
	}
	if synthesis == "" {
		synthesis = legCloseoutSynthesis(legs)
	}
	if synthesis == "" && len(ev.Handles) == 0 {
		return guidance.CoordinatorCompletionReport{Synthesis: hostEmptyCloseoutSynthesis}, nil
	}
	if synthesis == "" {
		synthesis = hostEmptyCloseoutSynthesis
	}

	report.Synthesis = synthesis
	report.CitedEvidence, report.CitedURLs = validatedLegCitations(roots, surfaceID, ev, legs, synthesis)
	if !guidance.TypedCitationChannelsEmpty(len(report.CitedEvidence), len(report.CitedURLs)) {
		eval := guidance.EvaluateCloseoutCitations(roots, surfaceID, report, ev)
		if eval.Code == "" && !eval.CitationUnverifiable {
			report.Normalize()
			grounding := guidance.BuildCloseoutCitationGrounding(roots, surfaceID, report, ev, eval)
			if grounding != nil {
				grounding.HostAssembled = true
				grounding.HintCode = firstForcedByCode(forcedBy)
				grounding.RetryCount = retryCount
				return report, grounding
			}
		}
	}

	report.CitedEvidence = nil
	report.CitedURLs = nil
	report = guidance.BindObservedSample(report, ev.Ledger, roots)
	report.Normalize()
	grounding := guidance.BuildObservedAutobindGrounding(roots, ev.Ledger, report)
	grounding.HintCode = firstForcedByCode(forcedBy)
	grounding.RetryCount = retryCount
	return report, grounding
}

func validatedLegCitations(
	roots evidence.CitationRoots,
	surfaceID string,
	ev guidance.CloseoutEvidence,
	legs []legCloseout,
	synthesis string,
) ([]guidance.CoordinatorCitedEvidence, []string) {
	seenEvidence := map[string]struct{}{}
	seenURLs := map[string]struct{}{}
	var cited []guidance.CoordinatorCitedEvidence
	var urls []string
	for _, leg := range legs {
		for _, wire := range leg.cited {
			candidate := coordinatorCitationFromLeg(leg.legID, wire)
			if strings.TrimSpace(candidate.Evidence) == "" && strings.TrimSpace(candidate.Path) == "" {
				continue
			}
			key := candidate.Evidence + "\x00" + candidate.Path + "\x00" + strconv.Itoa(candidate.Line) + "\x00" + strings.TrimSpace(candidate.Excerpt)
			if _, dup := seenEvidence[key]; dup {
				continue
			}
			probe := guidance.CoordinatorCompletionReport{Synthesis: synthesis, CitedEvidence: []guidance.CoordinatorCitedEvidence{candidate}}
			eval := guidance.EvaluateCloseoutCitations(roots, surfaceID, probe, ev)
			if eval.Code != "" || eval.CitationUnverifiable {
				continue
			}
			seenEvidence[key] = struct{}{}
			cited = append(cited, candidate)
		}
		for _, rawURL := range leg.urls {
			u := strings.TrimSpace(rawURL)
			if u == "" || !ev.URLSeen(u) {
				continue
			}
			if _, dup := seenURLs[u]; dup {
				continue
			}
			seenURLs[u] = struct{}{}
			urls = append(urls, u)
		}
	}
	return cited, urls
}

func coordinatorCitationFromLeg(legID string, wire api.CitationGroundingCitedEvidence) guidance.CoordinatorCitedEvidence {
	handle := strings.TrimSpace(wire.Handle)
	if handle != "" {
		handle = namespacedLegHandle(legID, handle)
	}
	return guidance.CoordinatorCitedEvidence{
		Evidence: handle,
		Path:     strings.TrimSpace(wire.Path),
		Line:     wire.Line,
		Excerpt:  strings.TrimSpace(wire.Excerpt),
	}
}

func namespacedLegHandle(legID, handle string) string {
	handle = strings.TrimSpace(handle)
	legID = strings.TrimSpace(legID)
	if legID == "" {
		return handle
	}
	return legID + ":" + handle
}

func legCloseoutSynthesis(legs []legCloseout) string {
	var lines []string
	seen := map[string]struct{}{}
	for _, leg := range legs {
		brief := strings.TrimSpace(leg.brief)
		if brief == "" {
			continue
		}
		line := brief
		if title := strings.TrimSpace(leg.title); title != "" {
			line = title + ": " + brief
		}
		if _, dup := seen[line]; dup {
			continue
		}
		seen[line] = struct{}{}
		lines = append(lines, "- "+line)
	}
	return strings.Join(lines, "\n")
}

func firstForcedByCode(forcedBy []string) string {
	for _, code := range forcedBy {
		if code = strings.TrimSpace(code); code != "" {
			return code
		}
	}
	return ""
}
