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

func extractRefusalNextCalls(ref *guidance.Refusal) []toolreach.Call {
	if ref == nil {
		return nil
	}
	fb := ref.Facts.FeedbackFor(ref.Code())
	if fb.Details == nil {
		return nil
	}
	calls, _ := fb.Details[api.ToolFeedbackNextCalls].([]toolreach.Call)
	return slices.Clone(calls)
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
