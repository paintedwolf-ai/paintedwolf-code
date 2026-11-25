// Package casegen constructs targeted cases for condition-ledger gaps. For a
// guard atom without an independent witness it searches the guard's producer
// inputs for two cases that hold the other atoms at a needed assignment and
// flip the atom, and runs each case through the producer so the production
// evaluator records both observations.
package casegen

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/independence"
)

// AnyFact in Knob.Facts marks an input that can move any fact, such as the
// invoked tool or the recipient's state; such inputs are tried after the
// inputs that name the atom's facts.
const AnyFact = "*"

// Knob is one typed producer input with a finite domain. Facts names the guard
// facts it drives; Values is its domain in a fixed order, the first being the
// baseline every case starts from.
type Knob struct {
	Name   string
	Facts  []string
	Values []any
	// Labels name the values in case IDs, parallel to Values; empty labels
	// print the value.
	Labels []string
}

func (k Knob) label(i int) string {
	if i < len(k.Labels) && k.Labels[i] != "" {
		return k.Labels[i]
	}
	return fmt.Sprintf("%v", k.Values[i])
}

func (k Knob) wildcard() bool { return slices.Contains(k.Facts, AnyFact) }

func (k Knob) drives(facts []string) bool {
	for _, f := range facts {
		if slices.Contains(k.Facts, f) {
			return true
		}
	}
	return false
}

// Setting picks a value index per knob name; an absent knob holds its
// baseline.
type Setting map[string]int

func (s Setting) with(name string, i int) Setting {
	out := maps.Clone(s)
	if out == nil {
		out = Setting{}
	}
	out[name] = i
	return out
}

// Value returns the value s picks for k.
func (s Setting) Value(k Knob) any { return k.Values[s[k.Name]] }

// Label prints the knobs s moves off their baseline, in knob order.
func Label(knobs []Knob, s Setting) string {
	var parts []string
	for _, k := range knobs {
		if i := s[k.Name]; i != 0 {
			parts = append(parts, k.Name+"="+k.label(i))
		}
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// Vector is one evaluation of a guard: each atom's truth by its text and the
// decision. Evaluated is false when the case never reached the guard.
type Vector struct {
	Truths    map[string]independence.Truth
	Decision  bool
	Evaluated bool
}

func (v Vector) String() string {
	if !v.Evaluated {
		return "not evaluated"
	}
	keys := slices.Sorted(maps.Keys(v.Truths))
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + v.Truths[k].String()
	}
	return fmt.Sprintf("{%s} -> %t", strings.Join(parts, ", "), v.Decision)
}

// Producer runs generated cases through the production producer and render
// path and records what the production evaluator observed.
type Producer interface {
	// Knobs lists the inputs the producer varies for site on recipient, or
	// none when no production path delivers site to recipient.
	Knobs(site, recipient string) []Knob
	// Run executes the case s under id and returns every evaluation of site
	// on recipient the evaluator recorded.
	Run(ctx context.Context, site, recipient, id string, knobs []Knob, s Setting) ([]Vector, error)
}

// Target is one guard input without an independent witness on Recipient.
// Needs are assignments of the other inputs under which the guard's
// decision follows Input; References names the facts an atom reads.
type Target struct {
	Site       string
	Recipient  string
	Input      string
	Needs      []map[string]independence.Truth
	References func(atom string) []string
}

// Case is one generated case and the evaluation it produced.
type Case struct {
	ID      string `json:"id"`
	Setting string `json:"setting"`
	Vector  string `json:"vector"`
}

// Outcome is the search result for one target: the witnessing pair, or why
// none was reached.
type Outcome struct {
	Site      string `json:"site"`
	Recipient string `json:"recipient"`
	Input     string `json:"input"`
	Found     bool   `json:"found"`
	With      *Case  `json:"with,omitempty"`
	Then      *Case  `json:"then,omitempty"`
	Runs      int    `json:"runs"`
	Reason    string `json:"reason,omitempty"`
}

// Budget bounds the producer runs per target and in total.
type Budget struct {
	PerTarget int
	Total     int
}

// maxNeedsTried caps the needed assignments one target searches;
// maxPaired caps the knobs whose values are changed two at a time.
const (
	maxNeedsTried = 4
	maxPaired     = 4
)

var errBudget = errors.New("case budget exhausted")

// Generate searches every target. Targets of one site and recipient share
// their runs, so a case reached for one input serves the others.
func Generate(ctx context.Context, p Producer, targets []Target, b Budget) ([]Outcome, error) {
	total := b.Total
	sessions := map[string]*search{}
	var out []Outcome
	for _, t := range targets {
		key := t.Site + "\x00" + t.Recipient
		s := sessions[key]
		if s == nil {
			s = &search{ctx: ctx, p: p, site: t.Site, recipient: t.Recipient, knobs: p.Knobs(t.Site, t.Recipient), memo: map[string][]Vector{}, total: &total}
			sessions[key] = s
		}
		o, err := s.target(t, b.PerTarget)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, nil
}

type search struct {
	ctx             context.Context
	p               Producer
	site, recipient string
	knobs           []Knob
	memo            map[string][]Vector
	total           *int
	runs, budget    int
	spent           int
	evaluated       bool
}

func (s *search) key(set Setting) string {
	var b strings.Builder
	for _, k := range s.knobs {
		fmt.Fprintf(&b, "%d,", set[k.Name])
	}
	return b.String()
}

// run executes set once and returns its evaluation nearest goal.
func (s *search) run(set Setting, goal map[string]independence.Truth) (Vector, error) {
	k := s.key(set)
	vs, ok := s.memo[k]
	if !ok {
		if s.runs >= s.budget || *s.total <= 0 {
			return Vector{}, errBudget
		}
		s.runs++
		s.spent++
		*s.total--
		id := "generated:" + s.recipient + ":" + s.site + Label(s.knobs, set)
		var err error
		if vs, err = s.p.Run(s.ctx, s.site, s.recipient, id, s.knobs, set); err != nil {
			return Vector{}, fmt.Errorf("%s: %w", id, err)
		}
		s.memo[k] = vs
	}
	best := Vector{}
	for _, v := range vs {
		s.evaluated = s.evaluated || v.Evaluated
		if score(v, goal) > score(best, goal) {
			best = v
		}
	}
	return best, nil
}

func (s *search) target(t Target, perTarget int) (Outcome, error) {
	o := Outcome{Site: t.Site, Recipient: t.Recipient, Input: t.Input}
	if len(s.knobs) == 0 {
		o.Reason = "no production path delivers this guard to " + t.Recipient
		return o, nil
	}
	if len(relevant(s.knobs, t.References(t.Input))) == 0 {
		o.Reason = fmt.Sprintf("no producer input drives %v", t.References(t.Input))
		return o, nil
	}
	if len(t.Needs) == 0 {
		o.Reason = "no assignment of the other atoms lets this atom decide the guard"
		return o, nil
	}
	if !s.evaluated && s.spent >= perTarget {
		o.Reason = fmt.Sprintf("the producer did not evaluate the guard in %d generated cases", s.spent)
		return o, nil
	}
	s.runs, s.budget = 0, perTarget
	nearest := ""
	for i, need := range t.Needs {
		if i == maxNeedsTried {
			break
		}
		with, then, near, err := s.pair(t, need)
		if nearest == "" {
			nearest = near
		}
		if errors.Is(err, errBudget) {
			break
		}
		if err != nil {
			return o, err
		}
		if with != nil {
			o.Found, o.With, o.Then = true, with, then
			break
		}
	}
	o.Runs = s.runs
	switch {
	case o.Found:
	case !s.evaluated:
		o.Reason = fmt.Sprintf("the producer did not evaluate the guard in %d generated cases", s.spent)
	default:
		o.Reason = fmt.Sprintf("%d generated cases reached no needed pair; nearest %s", s.runs, nearest)
	}
	return o, nil
}

// pair climbs to the needed assignment with Input true, then to the same
// assignment with Input false, and returns both cases when their decisions
// differ.
func (s *search) pair(t Target, need map[string]independence.Truth) (*Case, *Case, string, error) {
	goal := func(v independence.Truth) map[string]independence.Truth {
		g := maps.Clone(need)
		g[t.Input] = v
		return g
	}
	nearest := ""
	for _, order := range [][2]independence.Truth{{independence.True, independence.False}, {independence.False, independence.True}} {
		a, va, ok, err := s.climb(t, Setting{}, goal(order[0]))
		if err != nil {
			return nil, nil, cmp.Or(nearest, describe(va, goal(order[0]))), err
		}
		if !ok {
			nearest = cmp.Or(nearest, describe(va, goal(order[0])))
			continue
		}
		b, vb, ok, err := s.climb(t, a, goal(order[1]))
		if err != nil {
			return nil, nil, cmp.Or(nearest, describe(vb, goal(order[1]))), err
		}
		if !ok {
			nearest = cmp.Or(nearest, describe(vb, goal(order[1])))
			continue
		}
		if va.Decision != vb.Decision {
			return s.caseOf(a, va), s.caseOf(b, vb), "", nil
		}
	}
	return nil, nil, nearest, nil
}

func describe(v Vector, goal map[string]independence.Truth) string {
	keys := slices.Sorted(maps.Keys(goal))
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + goal[k].String()
	}
	return fmt.Sprintf("%s toward {%s}", v, strings.Join(parts, ", "))
}

func (s *search) caseOf(set Setting, v Vector) *Case {
	return &Case{ID: "generated:" + s.recipient + ":" + s.site + Label(s.knobs, set), Setting: Label(s.knobs, set), Vector: v.String()}
}

func score(v Vector, goal map[string]independence.Truth) int {
	if !v.Evaluated {
		return -1
	}
	n := 0
	for in, want := range goal {
		if v.Truths[in] == want {
			n++
		}
	}
	return n
}

// relevant lists the knobs that drive facts, those naming a fact first and
// wildcards last.
func relevant(knobs []Knob, facts []string) []Knob {
	var named, wild []Knob
	for _, k := range knobs {
		switch {
		case k.drives(facts):
			named = append(named, k)
		case k.wildcard():
			wild = append(wild, k)
		}
	}
	return append(named, wild...)
}

// moves lists the knobs whose values may move v toward goal: those driving a
// mismatched input, or every knob while the guard is not evaluated.
func (s *search) moves(t Target, v Vector, goal map[string]independence.Truth) []Knob {
	if !v.Evaluated {
		return s.knobs
	}
	var facts []string
	for in, want := range goal {
		if v.Truths[in] != want {
			facts = append(facts, t.References(in)...)
		}
	}
	return relevant(s.knobs, facts)
}

// climb changes one knob at a time, then two, keeping the first change that
// matches more of goal, until goal is matched or no change improves.
func (s *search) climb(t Target, start Setting, goal map[string]independence.Truth) (Setting, Vector, bool, error) {
	cur := start
	v, err := s.run(cur, goal)
	if err != nil {
		return cur, v, false, err
	}
	for score(v, goal) < len(goal) {
		knobs := s.moves(t, v, goal)
		next, nv, improved, err := s.improve(knobs, cur, v, goal)
		if err != nil {
			return cur, v, false, err
		}
		if !improved {
			return cur, v, false, nil
		}
		cur, v = next, nv
	}
	return cur, v, true, nil
}

func (s *search) improve(knobs []Knob, cur Setting, v Vector, goal map[string]independence.Truth) (Setting, Vector, bool, error) {
	base := score(v, goal)
	for _, k := range knobs {
		for i := range k.Values {
			if i == cur[k.Name] {
				continue
			}
			next := cur.with(k.Name, i)
			nv, err := s.run(next, goal)
			if err != nil {
				return cur, v, false, err
			}
			if score(nv, goal) > base {
				return next, nv, true, nil
			}
		}
	}
	paired := knobs[:min(len(knobs), maxPaired)]
	for a, ka := range paired {
		for _, kb := range paired[a+1:] {
			for i := range ka.Values {
				for j := range kb.Values {
					if i == cur[ka.Name] || j == cur[kb.Name] {
						continue
					}
					next := cur.with(ka.Name, i).with(kb.Name, j)
					nv, err := s.run(next, goal)
					if err != nil {
						return cur, v, false, err
					}
					if score(nv, goal) > base {
						return next, nv, true, nil
					}
				}
			}
		}
	}
	return cur, v, false, nil
}

// Needs enumerates up to limit assignments of the other inputs under which
// decide follows input, for a guard with no observation to start from.
func Needs(inputs []string, input string, decide func(map[string]bool) bool, limit int) []map[string]independence.Truth {
	others := slices.DeleteFunc(slices.Clone(inputs), func(s string) bool { return s == input })
	var out []map[string]independence.Truth
	for mask := 0; mask < 1<<len(others) && len(out) < limit; mask++ {
		values := map[string]bool{}
		need := map[string]independence.Truth{}
		for i, o := range others {
			values[o] = mask&(1<<i) != 0
			need[o] = independence.False
			if values[o] {
				need[o] = independence.True
			}
		}
		values[input] = true
		t := decide(values)
		values[input] = false
		if t != decide(values) {
			out = append(out, need)
		}
	}
	return out
}
