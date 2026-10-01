// Package decide models typed decisions a local decision model answers about
// host state. A question is declared with its answer type; an answer carries a
// distribution and confidence so callers can abstain. The package
// is engine-agnostic: bialy supplies the shipped engine, decidetest a scripted one.
package decide

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Kind is a typed answer shape.
type Kind string

const (
	// KindChoice picks one labelled option.
	KindChoice Kind = "choice"
	// KindScore places the state on an ordinal rubric.
	KindScore Kind = "score"
	// KindNoul answers a yes/no question with P(true).
	KindNoul Kind = "noul"
	// KindMulti answers every labelled option as its own yes/no.
	KindMulti Kind = "multi"
)

// Question is one typed question over a state.
type Question struct {
	Kind         Kind   `json:"type"`
	Instructions string `json:"instructions"`
	// Independent encodes multi-label options separately, without roster-dependent truncation.
	Independent bool `json:"independent,omitempty"`
	// Options describe each choice label.
	Options map[string]string `json:"options,omitempty"`
	// Levels describe each ordinal level, lowest first.
	Levels []string `json:"levels,omitempty"`
	// True and False describe the noul poles when the plain question is ambiguous.
	True  string `json:"true,omitempty"`
	False string `json:"false,omitempty"`
}

// Choice declares a labelled selection.
func Choice(instructions string, options map[string]string) Question {
	return Question{Kind: KindChoice, Instructions: instructions, Options: options}
}

// Multi declares a labelled set where any number of options may apply.
func Multi(instructions string, options map[string]string) Question {
	return Question{Kind: KindMulti, Instructions: instructions, Options: options}
}

// Score declares an ordinal rubric.
func Score(instructions string, levels ...string) Question {
	return Question{Kind: KindScore, Instructions: instructions, Levels: levels}
}

// Noul declares a yes/no question.
func Noul(instructions string) Question {
	return Question{Kind: KindNoul, Instructions: instructions}
}

// Answer is one typed answer with its probability distribution.
type Answer struct {
	Kind Kind `json:"type"`
	// Choice is the selected label; Probabilities covers every label. For a
	// multi answer each probability stands alone as P(option applies).
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Score is the expected level; Distribution covers every level.
	Score        float64   `json:"score,omitempty"`
	Distribution []float64 `json:"distribution,omitempty"`
	// Noul is P(true).
	Noul float64 `json:"noul,omitempty"`
	// Confidence is the engine's certainty score in [0,1].
	Confidence float64 `json:"confidence"`
}

// Answers maps question ids to answers.
type Answers map[string]Answer

// Head names a decision head over the shared backbone. Every head is trained
// on its own data and selected per request; the encoder is loaded once.
type Head string

const (
	// HeadTurnLoad answers turn decisions: which units a request needs and
	// the turn kind.
	HeadTurnLoad Head = "turn-load"
	// HeadGuideLoad answers the guides question when a release ships a
	// separate head for it; otherwise the turn-load head answers.
	HeadGuideLoad Head = "guide-load"
	// HeadUnitRank ranks host unit cards against a request: skills for the
	// roster and skills_read, loadable tool schemas for request_tools.
	HeadUnitRank Head = "unit-rank"
	// HeadCodeRank ranks code units: files, definitions, symbols, search hits.
	HeadCodeRank Head = "code-rank"
	// HeadWebRank ranks fetched web pages and search snippets.
	HeadWebRank Head = "web-rank"
)

// heads is the catalog of heads a shared backbone can serve.
var heads = []Head{HeadTurnLoad, HeadGuideLoad, HeadUnitRank, HeadCodeRank, HeadWebRank}

// Heads lists every head a shared backbone can serve, in catalog order.
func Heads() []Head { return append([]Head(nil), heads...) }

// Engine identifies the model that produced an answer.
type Engine struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	Device string `json:"device"`
	// Head lists the loaded heads as name=label pairs, or is empty for the
	// base model alone.
	Head string `json:"head,omitempty"`
}

// Loaded reports whether the engine serves head with trained weights.
func (e Engine) Loaded(head Head) bool {
	for entry := range strings.SplitSeq(e.Head, ",") {
		if name, _, ok := strings.Cut(strings.TrimSpace(entry), "="); ok && Head(name) == head {
			return true
		}
	}
	return false
}

// Result is one batched decision call.
type Result struct {
	Answers Answers
	Engine  Engine
	Elapsed time.Duration
}

// Decider answers typed questions and ranks candidates against a task.
type Decider interface {
	// Decide answers every question over one state in a single call of head.
	Decide(ctx context.Context, head Head, state any, questions map[string]Question) (Result, error)
	// Rank scores each candidate's relevance to task on the engine's rubric
	// with the named head.
	Rank(ctx context.Context, head Head, task string, candidates []string) ([]float64, Engine, error)
	// Available reports whether an engine can answer at all.
	Available() bool
	// Loaded reports whether the engine serves head with trained weights.
	Loaded(head Head) bool
}

var (
	// ErrUnavailable means no engine is configured or resolvable.
	ErrUnavailable = errors.New("decide: engine unavailable")
	// ErrDisabled means the engine was switched off by configuration.
	ErrDisabled = errors.New("decide: engine disabled")
	// ErrDeadline means the engine did not answer inside the caller's deadline.
	ErrDeadline = errors.New("decide: deadline exceeded")
	// ErrEngine wraps a fault the engine reported.
	ErrEngine = errors.New("decide: engine fault")
)

// Absent is the decider when no engine is configured. Every call abstains.
type Absent struct{}

// Decide reports the engine unavailable.
func (Absent) Decide(context.Context, Head, any, map[string]Question) (Result, error) {
	return Result{}, ErrUnavailable
}

// Rank reports the engine unavailable.
func (Absent) Rank(context.Context, Head, string, []string) ([]float64, Engine, error) {
	return nil, Engine{}, ErrUnavailable
}

// Available is false.
func (Absent) Available() bool { return false }

// Loaded is false.
func (Absent) Loaded(Head) bool { return false }
