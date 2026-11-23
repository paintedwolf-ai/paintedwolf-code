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

// FiredStepOutcome records whether one compiled OAR next-steps entry emitted a
// structured recovery call or rendered its declared otherwise fallback copy.
type FiredStepOutcome struct {
	StepPath         string
	InstructionID    string
	Action           string
	Tool             string
	Program          string
	Otherwise        string
	EmittedCall      bool
	EmittedOtherwise bool
	MatchedCalls     []toolreach.Call
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
	if h == nil || h.Rules == nil {
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

func resolveRuleStepOutcomes(rule *oar.Rule, fixCopy string, nextCalls []toolreach.Call) []FiredStepOutcome {
	if rule == nil || rule.NextSteps == nil {
		return nil
	}
	var out []FiredStepOutcome
	var walk func(prefix string, steps []oar.NextStep)
	walk = func(prefix string, steps []oar.NextStep) {
		for i, st := range steps {
			stepPath := fmt.Sprintf("%s[%d]", prefix, i)
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
			if action != "" {
				matched := matchStepCalls(st, prog, nextCalls)
				otherwise := strings.TrimSpace(st.Otherwise)
				emittedOtherwise := len(matched) == 0 && otherwise != "" && !strings.Contains(otherwise, "{{") && strings.Contains(fixCopy, otherwise)
				instrID := fmt.Sprintf("oar:%s:%s|policy/%s#next-steps:%s||%s", rule.Anchor, rule.ID, rule.ID, stepPath, action)
				out = append(out, FiredStepOutcome{
					StepPath:         stepPath,
					InstructionID:    instrID,
					Action:           action,
					Tool:             st.Tool,
					Program:          prog,
					Otherwise:        otherwise,
					EmittedCall:      len(matched) > 0,
					EmittedOtherwise: emittedOtherwise,
					MatchedCalls:     matched,
				})
			}
			walk(stepPath+".or", st.Or)
		}
	}
	walk("steps", rule.NextSteps.Steps)
	return out
}

func matchStepCalls(st oar.NextStep, prog string, calls []toolreach.Call) []toolreach.Call {
	var out []toolreach.Call
	for _, c := range calls {
		if prog != "" {
			if c.Tool == "command" && c.Program == prog {
				out = append(out, c)
			}
			continue
		}
		if c.Tool != st.Tool {
			continue
		}
		if st.Tool == "request_tools" {
			if need, ok := st.Args["need"].(string); ok && need != "" && fmt.Sprint(c.Args["need"]) != need {
				continue
			}
		}
		argsMatch := true
		for k, rawVal := range st.Args {
			sVal, isStr := rawVal.(string)
			if isStr && !strings.Contains(sVal, "{{") {
				if fmt.Sprint(c.Args[k]) != sVal {
					argsMatch = false
					break
				}
			}
		}
		if argsMatch {
			out = append(out, c)
		}
	}
	return out
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
