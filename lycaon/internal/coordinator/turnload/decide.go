package turnload

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/lycaon/lycaon/internal/decide"
)

// Verdict is one candidate's answer.
type Verdict struct {
	// P is the engine's P(true) that the request needs the unit.
	P float64 `json:"p"`
	// Confidence is max(P, 1-P) for this binary answer.
	Confidence float64 `json:"confidence"`
}

// Decision is what the turn decision established for one turn.
type Decision struct {
	// Kind is the confident turn kind, or empty when the engine abstained.
	Kind           string  `json:"kind,omitempty"`
	KindConfidence float64 `json:"kind_confidence,omitempty"`
	// Tools maps each tool the turn loads to its P(true).
	Tools map[string]float64 `json:"tools,omitempty"`
	// Verdicts holds every candidate's raw answer, keyed by tool name.
	Verdicts map[string]Verdict `json:"verdicts,omitempty"`
	// Candidates is the scored set, for the receipt.
	Candidates []Candidate   `json:"candidates,omitempty"`
	Engine     decide.Engine `json:"engine"`
	Elapsed    time.Duration `json:"-"`
	// Abstained is true when no engine answered; Reason says why.
	Abstained bool           `json:"abstained"`
	Reason    string         `json:"reason,omitempty"`
	Failure   RankingFailure `json:"failure,omitempty"`
}

// ToolIDs returns the loaded tools in sorted order.
func (d Decision) ToolIDs() []string {
	out := make([]string, 0, len(d.Tools))
	for id := range d.Tools {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Question IDs group tool and turn-kind answers.
const (
	toolsQuestionID = "tools"
	kindQuestionID  = "kind"
)

// Questions renders the turn question set for candidates. Independent heads
// read every tool on its own row, joint heads read a roster. The kind choice
// is a roster question and is asked only of joint heads.
func (s TurnSpec) Questions(candidates []Candidate) map[string]decide.Question {
	tools := map[string]string{}
	for _, c := range s.Tools.Candidates(candidates) {
		tools[c.ID] = c.Option(s.Tools.OptionWords)
	}
	out := make(map[string]decide.Question, 2)
	if len(tools) > 0 {
		question := decide.Multi(s.Tools.Question, tools)
		question.Independent = s.Tools.Independent
		out[toolsQuestionID] = question
	}
	if !s.Tools.Independent {
		out[kindQuestionID] = decide.Choice(s.Kind.Instructions, s.Kind.Options)
	}
	return out
}

// verdictOf reads one option's answer out of a multi answer: its probability
// and the certainty of that probability.
func verdictOf(a decide.Answer, id string) (Verdict, bool) {
	if a.Kind != decide.KindMulti {
		return Verdict{}, false
	}
	p, ok := a.Probabilities[id]
	if !ok {
		return Verdict{}, false
	}
	return Verdict{P: p, Confidence: math.Max(p, 1-p)}, true
}

// Decide applies catalog thresholds and records typed selection failures.
func Decide(ctx context.Context, d decide.Decider, spec TurnSpec, state State, candidates []Candidate) Decision {
	candidates = spec.Tools.Candidates(candidates)
	out := Decision{Candidates: candidates}
	if d == nil || !d.Available() {
		out.Abstained, out.Reason, out.Failure = true, "engine unavailable", RankingUnavailable
		return out
	}
	if len(candidates) == 0 {
		out.Abstained, out.Reason = true, "nothing to decide"
		return out
	}
	callCtx, cancel := context.WithTimeout(ctx, spec.Deadline())
	defer cancel()
	res, err := d.Decide(callCtx, decide.HeadTurnLoad, state, spec.Questions(candidates))
	if err != nil {
		out.Abstained, out.Reason, out.Failure = true, abstainReason(err), rankingFailure(err)
		return out
	}
	out.Engine, out.Elapsed = res.Engine, res.Elapsed
	out.Tools = map[string]float64{}
	out.Verdicts = make(map[string]Verdict, len(candidates))
	if kind, ok := res.Answers[kindQuestionID]; ok && kind.Confidence >= spec.Kind.ConfidenceFloor && knownKind(kind.Choice) {
		out.Kind, out.KindConfidence = kind.Choice, kind.Confidence
	}
	vetoed := spec.Kind.VetoTools && out.Kind == KindAnswerOnly
	for _, c := range candidates {
		v, ok := verdictOf(res.Answers[toolsQuestionID], c.ID)
		if !ok {
			continue
		}
		out.Verdicts[c.ID] = v
		if !vetoed && v.P >= spec.Tools.LoadAt {
			out.Tools[c.ID] = v.P
		}
	}
	return out
}

func abstainReason(err error) string {
	switch {
	case errors.Is(err, decide.ErrDisabled):
		return "engine disabled"
	case errors.Is(err, decide.ErrUnavailable):
		return "engine unavailable"
	case errors.Is(err, decide.ErrDeadline), errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	default:
		return "engine fault: " + err.Error()
	}
}
