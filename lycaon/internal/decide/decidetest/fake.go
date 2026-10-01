// Package decidetest supplies a scripted decider for tests.
package decidetest

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/decide"
)

// Fake answers from scripted tables and records every call.
type Fake struct {
	mu sync.Mutex
	// Answers is returned by Decide for every state. Missing questions abstain
	// with zero confidence.
	Answers decide.Answers
	// Scores is returned by Rank when its length matches the candidates.
	Scores []float64
	// Err fails every call when set.
	Err error
	// Unavailable makes Available report false.
	Unavailable bool
	// Heads lists the heads Loaded reports; nil means every head loaded.
	Heads []decide.Head

	Decisions []DecideCall
	Ranks     []RankCall
}

// Loaded reports the scripted head set, or true for every head when none is scripted.
func (f *Fake) Loaded(head decide.Head) bool {
	if f == nil || f.Unavailable {
		return false
	}
	if f.Heads == nil {
		return true
	}
	for _, h := range f.Heads {
		if h == head {
			return true
		}
	}
	return false
}

// DecideCall records one Decide invocation.
type DecideCall struct {
	Head      decide.Head
	State     any
	Questions map[string]decide.Question
}

// RankCall records one Rank invocation.
type RankCall struct {
	Head       decide.Head
	Task       string
	Candidates []string
}

var fakeEngine = decide.Engine{Name: "fake", Model: "scripted", Device: "cpu"}

// Decide returns the scripted answers.
func (f *Fake) Decide(_ context.Context, head decide.Head, state any, questions map[string]decide.Question) (decide.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Decisions = append(f.Decisions, DecideCall{Head: head, State: state, Questions: questions})
	if f.Err != nil {
		return decide.Result{}, f.Err
	}
	out := make(decide.Answers, len(questions))
	for id, q := range questions {
		if a, ok := f.Answers[id]; ok {
			out[id] = a
			continue
		}
		out[id] = decide.Answer{Kind: q.Kind}
	}
	return decide.Result{Answers: out, Engine: fakeEngine}, nil
}

// Rank returns the scripted scores, or zeros when none fit.
func (f *Fake) Rank(_ context.Context, head decide.Head, task string, candidates []string) ([]float64, decide.Engine, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Ranks = append(f.Ranks, RankCall{Head: head, Task: task, Candidates: append([]string(nil), candidates...)})
	if f.Err != nil {
		return nil, decide.Engine{}, f.Err
	}
	if len(f.Scores) == len(candidates) {
		return append([]float64(nil), f.Scores...), fakeEngine, nil
	}
	return make([]float64, len(candidates)), fakeEngine, nil
}

// Available reports the scripted availability.
func (f *Fake) Available() bool { return f != nil && !f.Unavailable }

// LoadAll is the decider of a scripted run: every yes/no question and every
// multi-question option is a confident yes, so each loadable unit a turn could
// carry is loaded, and every choice and ranking abstains.
type LoadAll struct{}

var loadAllEngine = decide.Engine{Name: "fake", Model: "load-all", Device: "cpu"}

// Decide answers yes to every yes/no question and multi option and abstains
// from the rest.
func (LoadAll) Decide(_ context.Context, _ decide.Head, _ any, questions map[string]decide.Question) (decide.Result, error) {
	out := make(decide.Answers, len(questions))
	for id, q := range questions {
		switch q.Kind {
		case decide.KindNoul:
			out[id] = decide.Answer{Kind: q.Kind, Noul: 1, Confidence: 1}
		case decide.KindMulti:
			yes := make(map[string]float64, len(q.Options))
			for option := range q.Options {
				yes[option] = 1
			}
			out[id] = decide.Answer{Kind: q.Kind, Probabilities: yes, Confidence: 1}
		default:
			out[id] = decide.Answer{Kind: q.Kind}
		}
	}
	return decide.Result{Answers: out, Engine: loadAllEngine}, nil
}

// Rank scores nothing, so no skill is pre-read and request text loads only
// the schemas it names.
func (LoadAll) Rank(_ context.Context, _ decide.Head, _ string, candidates []string) ([]float64, decide.Engine, error) {
	return make([]float64, len(candidates)), loadAllEngine, nil
}

// Available is true.
func (LoadAll) Available() bool { return true }

// Loaded is true for every head.
func (LoadAll) Loaded(decide.Head) bool { return true }
