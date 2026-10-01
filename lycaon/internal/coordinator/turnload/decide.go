package turnload

import (
	"context"
	"errors"
	"math"
	"slices"
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
	// Omitted maps each instruction unit the turn leaves out to its verdict.
	Omitted map[string]Verdict `json:"omitted,omitempty"`
	// Verdicts holds every candidate's raw answer, keyed by question id.
	Verdicts map[string]Verdict `json:"verdicts,omitempty"`
	// Candidates is the scored set, for the receipt.
	Candidates []Candidate   `json:"candidates,omitempty"`
	Engine     decide.Engine `json:"engine"`
	Elapsed    time.Duration `json:"-"`
	// Abstained is true when no engine answered; Reason says why.
	Abstained bool   `json:"abstained"`
	Reason    string `json:"reason,omitempty"`
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

// OmittedIDs returns the omitted units in sorted order.
func (d Decision) OmittedIDs() []string {
	out := make([]string, 0, len(d.Omitted))
	for id := range d.Omitted {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Question IDs group tool, guide and turn-kind answers.
const (
	toolsQuestionID  = "tools"
	guidesQuestionID = "guides"
	kindQuestionID   = "kind"
)

// Questions renders the turn question set for candidates. The tools and
// guides questions share one encoding: independent heads read every option
// on its own row, joint heads read a roster. The kind choice is a roster
// question and is asked only of joint heads.
func (s TurnSpec) Questions(candidates []Candidate) map[string]decide.Question {
	tools := map[string]string{}
	guides := map[string]string{}
	for _, c := range s.Tools.Candidates(candidates) {
		switch c.Kind {
		case KindTool:
			tools[c.ID] = c.Option(s.Tools.OptionWords)
		case KindGuide:
			guides[c.ID] = c.Option(s.Guides.OptionWords)
		}
	}
	out := make(map[string]decide.Question, 3)
	if len(tools) > 0 {
		question := decide.Multi(s.Tools.Question, tools)
		question.Independent = s.Tools.Independent
		out[toolsQuestionID] = question
	}
	if len(guides) > 0 {
		question := decide.Multi(s.Guides.Question, guides)
		question.Independent = s.Tools.Independent
		out[guidesQuestionID] = question
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

// Decide applies the catalog thresholds; engine faults load no tools and omit no guides.
func Decide(ctx context.Context, d decide.Decider, spec TurnSpec, state State, candidates []Candidate) Decision {
	candidates = spec.Tools.Candidates(candidates)
	out := Decision{Candidates: candidates}
	if d == nil || !d.Available() {
		out.Abstained, out.Reason = true, "engine unavailable"
		return out
	}
	if len(candidates) == 0 {
		out.Abstained, out.Reason = true, "nothing to decide"
		return out
	}
	callCtx, cancel := context.WithTimeout(ctx, spec.Deadline())
	defer cancel()
	res, err := ask(callCtx, d, state, spec.Questions(candidates))
	if err != nil {
		out.Abstained, out.Reason = true, abstainReason(err)
		return out
	}
	out.Engine, out.Elapsed = res.Engine, res.Elapsed
	out.Tools = map[string]float64{}
	out.Omitted = map[string]Verdict{}
	out.Verdicts = make(map[string]Verdict, len(candidates))
	if kind, ok := res.Answers[kindQuestionID]; ok && kind.Confidence >= spec.Kind.ConfidenceFloor && knownKind(kind.Choice) {
		out.Kind, out.KindConfidence = kind.Choice, kind.Confidence
	}
	for _, c := range candidates {
		var v Verdict
		var ok bool
		switch c.Kind {
		case KindTool:
			v, ok = verdictOf(res.Answers[toolsQuestionID], c.ID)
		case KindGuide:
			v, ok = verdictOf(res.Answers[guidesQuestionID], c.ID)
		}
		if !ok {
			continue
		}
		out.Verdicts[c.QuestionID()] = v
		switch c.Kind {
		case KindTool:
			vetoed := spec.Kind.VetoTools && out.Kind == KindAnswerOnly
			if !vetoed && v.P >= spec.Tools.LoadAt {
				out.Tools[c.ID] = v.P
			}
		case KindGuide:
			if slices.Contains(spec.Guides.Omittable, c.ID) && !c.Needed(state.Loaded) && v.P < spec.Guides.OmitBelow && v.Confidence >= spec.Guides.ConfidenceFloor {
				out.Omitted[c.ID] = v
			}
		}
	}
	return out
}

// ask puts the turn questions to the engine: the guides question to the guide
// head when the release ships one, everything else to the turn-load head.
func ask(ctx context.Context, d decide.Decider, state State, questions map[string]decide.Question) (decide.Result, error) {
	guides, split := questions[guidesQuestionID]
	if !split || !d.Loaded(decide.HeadGuideLoad) {
		return d.Decide(ctx, decide.HeadTurnLoad, state, questions)
	}
	rest := make(map[string]decide.Question, len(questions)-1)
	for id, q := range questions {
		if id != guidesQuestionID {
			rest[id] = q
		}
	}
	res, err := d.Decide(ctx, decide.HeadTurnLoad, state, rest)
	if err != nil {
		return decide.Result{}, err
	}
	guided, err := d.Decide(ctx, decide.HeadGuideLoad, state, map[string]decide.Question{guidesQuestionID: guides})
	if err != nil {
		return decide.Result{}, err
	}
	if res.Answers == nil {
		res.Answers = decide.Answers{}
	}
	res.Answers[guidesQuestionID] = guided.Answers[guidesQuestionID]
	res.Elapsed += guided.Elapsed
	return res, nil
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
