package oarproducers

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	coordgrounding "github.com/lycaon/lycaon/internal/coordinator/grounding"
	"github.com/lycaon/lycaon/internal/grounding"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/chats"
	"github.com/lycaon/lycaon/internal/session/transcript"
	"github.com/lycaon/lycaon/internal/session/turnexecution"
	"github.com/lycaon/lycaon/internal/toolreach"
	"github.com/lycaon/lycaon/pkg/api"
)

// FiredStepOutcome pairs one observed oar.StepResolution from ResolveNextSteps
// with the compiled rule step metadata needed for its catalog InstructionID.
type FiredStepOutcome struct {
	StepPath      string
	InstructionID string
	Action        string
	Tool          string
	Program       string
	Otherwise     string
	HasGuard      bool
	GuardApplies  bool
	Reachable     bool
	Outcome       oar.StepResolutionOutcome
	SelectedCalls []toolreach.Call
	RenderedText  string
}

// ObserveIndirectProducerCall inspects the active runtime call stack to derive
// the module-relative SSA indirect call site for each supplied fact name.
func ObserveIndirectProducerCall(moduleRoot string, facts ...string) []string {
	var pcs [16]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	loc := ""
	for {
		fr, more := frames.Next()
		if fr.File != "" {
			rel, err := filepath.Rel(moduleRoot, fr.File)
			if err == nil {
				slash := filepath.ToSlash(rel)
				if strings.HasPrefix(slash, "internal/") {
					loc = fmt.Sprintf("%s:%d", slash, fr.Line)
					break
				}
			}
		}
		if !more {
			break
		}
	}
	if loc == "" {
		return nil
	}
	out := make([]string, 0, len(facts))
	for _, f := range facts {
		if strings.TrimSpace(f) != "" {
			out = append(out, fmt.Sprintf("%s@%s#indirect_function_value_call", f, loc))
		}
	}
	return out
}

// RefusalDeliveryKind classifies how a delivered guidance.Refusal was produced.
type RefusalDeliveryKind string

const (
	RefusalDeliveryOmitted    RefusalDeliveryKind = "omitted"
	RefusalDeliveryPolicyRule RefusalDeliveryKind = "policy_rule"
	RefusalDeliveryFallback   RefusalDeliveryKind = "fallback"
)

// DeliveredRefusalObservation captures the delivered refusal block and
// structured OAR contribution metadata at the refusal boundary.
type DeliveredRefusalObservation struct {
	Kind         RefusalDeliveryKind
	Code         string
	PolicyRule   string
	PolicyAnchor string
	Body         string
	Historical   string
	HostActions  string
	NextCalls    []toolreach.Call
}

// ObserveDeliveredRefusal inspects a delivered guidance.Refusal and extracts its
// contribution identity, structured next-calls, and delivered prose sections.
func ObserveDeliveredRefusal(ref *guidance.Refusal) DeliveredRefusalObservation {
	if ref == nil {
		return DeliveredRefusalObservation{Kind: RefusalDeliveryOmitted}
	}
	code := strings.TrimSpace(ref.Code())
	body := strings.TrimSpace(ref.Body)
	hist, actions := SplitDeliveredRefusalBody(body)
	fb := ref.Facts.FeedbackFor(code)
	var policyRule, policyAnchor string
	var nextCalls []toolreach.Call
	if fb.Details != nil {
		policyRule, _ = fb.Details[oar.PolicyRuleDetailKey].(string)
		policyAnchor, _ = fb.Details[oar.PolicyAnchorDetailKey].(string)
		if calls, ok := fb.Details[api.ToolFeedbackNextCalls].([]toolreach.Call); ok {
			nextCalls = slices.Clone(calls)
		}
	}
	policyRule = strings.TrimSpace(policyRule)
	policyAnchor = strings.TrimSpace(policyAnchor)
	kind := RefusalDeliveryPolicyRule
	if ref.Copy == nil || policyRule == "" {
		kind = RefusalDeliveryFallback
	}
	return DeliveredRefusalObservation{
		Kind:         kind,
		Code:         code,
		PolicyRule:   policyRule,
		PolicyAnchor: policyAnchor,
		Body:         body,
		Historical:   hist,
		HostActions:  actions,
		NextCalls:    nextCalls,
	}
}

// SplitDeliveredRefusalBody partitions a rendered refusal block into past-occurrence
// observations (Rejected/Cause) and host-authored guidance (State/Why/Fix/Next).
func SplitDeliveredRefusalBody(body string) (historical, hostActions string) {
	var histLines, actionLines []string
	section := ""
	for _, rawLine := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if trimmed == "" {
			continue
		}
		switch {
		case trimmed == ">>> Tool feedback":
			section = "historical"
		case strings.HasPrefix(trimmed, "Rejected:"):
			section = "historical"
			if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "Rejected:")); rest != "" {
				histLines = append(histLines, rest)
			}
		case strings.HasPrefix(trimmed, "Cause:"):
			section = "historical"
			if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "Cause:")); rest != "" {
				histLines = append(histLines, rest)
			}
		case strings.HasPrefix(trimmed, "State:"):
			section = "actions"
			if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "State:")); rest != "" {
				actionLines = append(actionLines, rest)
			}
		case strings.HasPrefix(trimmed, "Why:"):
			section = "actions"
			if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "Why:")); rest != "" {
				actionLines = append(actionLines, rest)
			}
		case strings.HasPrefix(trimmed, "Fix:"):
			section = "actions"
			if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "Fix:")); rest != "" {
				actionLines = append(actionLines, rest)
			}
		case strings.HasPrefix(trimmed, "Next:"):
			section = "actions"
			if rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "Next:")); rest != "" {
				actionLines = append(actionLines, rest)
			}
		case strings.HasPrefix(trimmed, "Code:"):
			section = "code"
		default:
			switch section {
			case "historical":
				histLines = append(histLines, trimmed)
			case "actions":
				actionLines = append(actionLines, trimmed)
			}
		}
	}
	return strings.Join(histLines, "\n"), strings.Join(actionLines, "\n")
}

// ScopedNextStepObserver captures ResolveNextSteps outputs only for rules in a
// given RuleSet so concurrent pipelines do not observe each other's evaluations.
type ScopedNextStepObserver struct {
	mu       sync.Mutex
	rulePtrs map[*oar.Rule]bool
	byQual   map[string]*oar.Rule
	observed map[*oar.Rule]oar.ResolvedSteps
}

// InstallScopedNextStepObserver registers a RuleSet-scoped observer on ActiveNextStepObservers.
func InstallScopedNextStepObserver(rules *oar.RuleSet) (*ScopedNextStepObserver, func()) {
	ptrs := map[*oar.Rule]bool{}
	byQual := map[string]*oar.Rule{}
	if rules != nil {
		for _, r := range rules.All() {
			if r != nil {
				ptrs[r] = true
				byQual[r.Qualified()] = r
			}
		}
	}
	obs := &ScopedNextStepObserver{
		rulePtrs: ptrs,
		byQual:   byQual,
		observed: make(map[*oar.Rule]oar.ResolvedSteps),
	}
	oar.ActiveNextStepObservers.Store(obs, oar.NextStepObserver(func(r *oar.Rule, resolved oar.ResolvedSteps) {
		if !obs.rulePtrs[r] {
			return
		}
		obs.mu.Lock()
		obs.observed[r] = resolved
		obs.mu.Unlock()
	}))
	return obs, func() { oar.ActiveNextStepObservers.Delete(obs) }
}

// TakeByQualified returns and clears the observed ResolvedSteps for qualifiedRuleID.
func (s *ScopedNextStepObserver) TakeByQualified(qualifiedRuleID string) (*oar.Rule, oar.ResolvedSteps, bool) {
	if s == nil || qualifiedRuleID == "" {
		return nil, oar.ResolvedSteps{}, false
	}
	r := s.byQual[qualifiedRuleID]
	if r == nil {
		return nil, oar.ResolvedSteps{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	res, ok := s.observed[r]
	if ok {
		delete(s.observed, r)
	}
	return r, res, ok
}

// Clear discards any pending step resolutions recorded on s.
func (s *ScopedNextStepObserver) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	clear(s.observed)
	s.mu.Unlock()
}

func extractRefusalNextCalls(ref *guidance.Refusal) []toolreach.Call {
	return ObserveDeliveredRefusal(ref).NextCalls
}

func extractDecisionNextCalls(d *oar.Decision) []toolreach.Call {
	if d == nil || d.Data == nil {
		return nil
	}
	calls, _ := d.Data[api.ToolFeedbackNextCalls].([]toolreach.Call)
	return slices.Clone(calls)
}

func (h *Harness) ruleByAnchorAndCode(anchor, code string) *oar.Rule {
	if h == nil || h.Rules == nil || code == "" {
		return nil
	}
	short := code
	if idx := strings.LastIndexByte(short, '/'); idx >= 0 {
		short = short[idx+1:]
	}
	for _, r := range h.Rules.All() {
		if r != nil && r.Anchor == anchor && (r.ID == short || r.Qualified() == code) {
			return r
		}
	}
	return nil
}

func buildFiredStepOutcomes(rule *oar.Rule, resolved oar.ResolvedSteps) []FiredStepOutcome {
	if rule == nil || rule.NextSteps == nil || len(resolved.Resolutions) == 0 {
		return nil
	}
	stepByPath := make(map[string]oar.NextStep, len(resolved.Resolutions))
	var indexSteps func(prefix string, steps []oar.NextStep)
	indexSteps = func(prefix string, steps []oar.NextStep) {
		for i, st := range steps {
			p := fmt.Sprintf("%s[%d]", prefix, i)
			stepByPath[p] = st
			indexSteps(p+".or", st.Or)
		}
	}
	indexSteps("steps", rule.NextSteps.Steps)

	out := make([]FiredStepOutcome, 0, len(resolved.Resolutions))
	for _, res := range resolved.Resolutions {
		st, ok := stepByPath[res.BranchPrefix]
		if !ok {
			continue
		}
		action := st.Tool
		prog := ""
		if len(st.Program) > 0 {
			prog = st.Program[0]
			action = "command:" + prog
		}
		if st.Tool == "request_tools" {
			if need, ok := st.Args["need"].(string); ok && need != "" {
				action = "request_tools(need=" + need + ")"
			}
		}
		instrID := ""
		if action != "" {
			instrID = fmt.Sprintf("oar:%s:%s|policy/%s#next-steps:%s||%s", rule.Anchor, rule.ID, rule.ID, res.BranchPrefix, action)
		}
		out = append(out, FiredStepOutcome{
			StepPath:      res.BranchPrefix,
			InstructionID: instrID,
			Action:        action,
			Tool:          st.Tool,
			Program:       prog,
			Otherwise:     strings.TrimSpace(st.Otherwise),
			HasGuard:      res.HasGuard,
			GuardApplies:  res.GuardApplies,
			Reachable:     res.Reachable,
			Outcome:       res.Outcome,
			SelectedCalls: slices.Clone(res.SelectedCalls),
			RenderedText:  res.RenderedText,
		})
	}
	return out
}

// installNextStepObserver records resolutions only for this harness's own rule
// pointers, so concurrently running harnesses cannot see each other's steps.
func (h *Harness) installNextStepObserver() func() {
	oar.ActiveNextStepObservers.Store(h, oar.NextStepObserver(func(r *oar.Rule, resolved oar.ResolvedSteps) {
		if !h.rulePtrs[r] {
			return
		}
		h.edgeMu.Lock()
		h.observedNextSteps[r] = resolved
		h.edgeMu.Unlock()
	}))
	return func() { oar.ActiveNextStepObservers.Delete(h) }
}

func (h *Harness) takeObservedNextSteps(rule *oar.Rule) (oar.ResolvedSteps, bool) {
	if rule == nil {
		return oar.ResolvedSteps{}, false
	}
	h.edgeMu.Lock()
	defer h.edgeMu.Unlock()
	res, ok := h.observedNextSteps[rule]
	if ok {
		delete(h.observedNextSteps, rule)
	}
	return res, ok
}

func (h *Harness) clearObservedNextSteps() {
	h.edgeMu.Lock()
	clear(h.observedNextSteps)
	h.edgeMu.Unlock()
}

type transcriptStoreMap struct {
	transcript.Transcript
	mu sync.Mutex
	m  map[string][]api.Message
}

func (t *transcriptStoreMap) GetMessages(_ context.Context, id string) ([]api.Message, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return slices.Clone(t.m[id]), nil
}

func (t *transcriptStoreMap) SetMessages(id string, msgs []api.Message) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.m[id] = slices.Clone(msgs)
}

type workerQueueMap struct {
	mu sync.Mutex
	m  map[string][]api.WorkerTask
}

var _ coordgrounding.WorkerQueue = (*workerQueueMap)(nil)

func (w *workerQueueMap) ListBySession(_ context.Context, _, sessionID string, _ ...api.WorkerStatus) ([]api.WorkerTask, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.m[sessionID]), nil
}

func (w *workerQueueMap) SetJobs(sessionID string, jobs []api.WorkerTask) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.m[sessionID] = slices.Clone(jobs)
}

func (h *Harness) wireAmbientGrounding() {
	host := &session.Host{
		Chats: chats.New(h.Sessions, nil, nil, nil, nil, chats.Lifecycle{}),
		Runner: &turnexecution.Service{
			Transcript: transcript.New(nil, h.Transcripts, nil, nil, nil),
		},
	}
	ambient := coordgrounding.NewAmbientGroundingCoordinator(h.WorkerQueue, nil, grounding.DefaultGroundingConfig(), nil, host)
	ambient.Pipeline = h.Pipeline
	ambient.Names = func(_ context.Context, _ string) (string, bool, error) {
		h.recordExercisedEdges(ObserveIndirectProducerCall(h.ModuleRoot, "paintedwolf.missing_summary_count")...)
		return "impl-1", true, nil
	}
	h.Ambient = ambient
}

func (h *Harness) resetExercisedEdges() {
	h.edgeMu.Lock()
	h.exercisedEdges = nil
	clear(h.observedNextSteps)
	h.edgeMu.Unlock()
}

func (h *Harness) recordExercisedEdges(edges ...string) {
	if len(edges) == 0 {
		return
	}
	h.edgeMu.Lock()
	defer h.edgeMu.Unlock()
	for _, e := range edges {
		if e != "" && !slices.Contains(h.exercisedEdges, e) {
			h.exercisedEdges = append(h.exercisedEdges, e)
		}
	}
}

func (h *Harness) snapshotExercisedEdges() []string {
	h.edgeMu.Lock()
	defer h.edgeMu.Unlock()
	return slices.Clone(h.exercisedEdges)
}
