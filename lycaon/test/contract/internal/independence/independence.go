// Package independence decides, for each atom of a compound guard, whether
// observed evaluations show the atom independently changing the guard's
// decision.
package independence

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Truth is an atom's value in one evaluation.
type Truth int8

const (
	Unknown Truth = iota
	False
	True
)

func (t Truth) String() string {
	switch t {
	case False:
		return "F"
	case True:
		return "T"
	}
	return "?"
}

// Vector is one observed evaluation of a guard.
type Vector struct {
	Atoms    []Truth
	Decision bool
}

// Guard is one evaluated condition site with every vector observed there.
type Guard struct {
	Site    string
	Source  string
	Atoms   []string
	Vectors []Vector
}

// Implication is a host invariant: whenever atom If is true, atom Then is
// true. Source names the production code that establishes it.
type Implication struct {
	If, Then string
	Source   string
}

// Witness is a pair of evaluations that differ only in one input and in the
// decision.
type Witness struct {
	Input      string
	With, Then Vector
}

// Gap is an input with neither a witness pair nor a constraint ruling every
// pair out.
type Gap struct {
	Site    string
	Source  string
	Input   string
	Missing []string
}

func (g Gap) String() string {
	return fmt.Sprintf("%s: %q has no independent witness for %s (needs %s)", g.Site, g.Source, g.Input, strings.Join(g.Missing, " or "))
}

// Exclusion records an input whose every independence pair a constraint
// rules out.
type Exclusion struct {
	Site   string
	Input  string
	Reason string
}

// Result is the analysis of all guards.
type Result struct {
	Witnesses  map[string][]Witness
	Exclusions []Exclusion
	Gaps       []Gap
	// Contradicted lists implications an observation violates.
	Contradicted []string
}

// Analyze checks every input of every guard. Atoms with equal source are one
// input. Two evaluations witness an input when its value differs, every other
// input holds the same known value, and the decisions differ. Without a
// witness, an input is excluded only when no assignment satisfying the
// implications and the guard's own logic admits a pair; otherwise it is a gap
// naming the assignments to observe.
func Analyze(guards []Guard, eval func(site string, atoms []Truth) (bool, error), implications []Implication) (Result, error) {
	res := Result{Witnesses: map[string][]Witness{}}
	for _, imp := range implications {
		for _, g := range guards {
			for _, v := range g.Vectors {
				if a, b := valueOf(g, v, imp.If), valueOf(g, v, imp.Then); a == True && b == False {
					res.Contradicted = append(res.Contradicted, fmt.Sprintf("%s (%s) contradicted at %s", imp.If+" ⇒ "+imp.Then, imp.Source, g.Site))
				}
			}
		}
	}
	for _, g := range guards {
		inputs := distinctInputs(g.Atoms)
		if len(inputs) < 2 {
			continue
		}
		for _, in := range inputs {
			if w, ok := findWitness(g, in, inputs); ok {
				res.Witnesses[g.Site] = append(res.Witnesses[g.Site], w)
				continue
			}
			needed, err := neededAssignments(g, in, inputs, eval, implications)
			if err != nil {
				return Result{}, err
			}
			if len(needed) == 0 {
				res.Exclusions = append(res.Exclusions, Exclusion{Site: g.Site, Input: in, Reason: constraintReason(in, inputs, implications)})
				continue
			}
			res.Gaps = append(res.Gaps, Gap{Site: g.Site, Source: g.Source, Input: in, Missing: needed})
		}
	}
	sort.Slice(res.Gaps, func(i, j int) bool {
		if res.Gaps[i].Site != res.Gaps[j].Site {
			return res.Gaps[i].Site < res.Gaps[j].Site
		}
		return res.Gaps[i].Input < res.Gaps[j].Input
	})
	return res, nil
}

func distinctInputs(atoms []string) []string {
	var out []string
	for _, a := range atoms {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func valueOf(g Guard, v Vector, input string) Truth {
	got := Unknown
	for i, a := range g.Atoms {
		if a != input || i >= len(v.Atoms) {
			continue
		}
		if got != Unknown && v.Atoms[i] != got {
			return Unknown
		}
		got = v.Atoms[i]
	}
	return got
}

func findWitness(g Guard, in string, inputs []string) (Witness, bool) {
	for i, a := range g.Vectors {
		for _, b := range g.Vectors[i+1:] {
			if a.Decision == b.Decision {
				continue
			}
			va, vb := valueOf(g, a, in), valueOf(g, b, in)
			if va == Unknown || vb == Unknown || va == vb {
				continue
			}
			fixed := true
			for _, other := range inputs {
				if other == in {
					continue
				}
				oa, ob := valueOf(g, a, other), valueOf(g, b, other)
				if oa == Unknown || oa != ob {
					fixed = false
					break
				}
			}
			if fixed {
				return Witness{Input: in, With: a, Then: b}, true
			}
		}
	}
	return Witness{}, false
}

// neededAssignments enumerates assignments of the other inputs under which the
// guard's decision follows in, keeping those the implications allow. Each is
// printed as the pair of input vectors to observe.
func neededAssignments(g Guard, in string, inputs []string, eval func(string, []Truth) (bool, error), implications []Implication) ([]string, error) {
	if len(inputs) > 12 {
		return nil, fmt.Errorf("%s: %d inputs exceed the enumeration bound", g.Site, len(inputs))
	}
	var out []string
	others := slices.DeleteFunc(slices.Clone(inputs), func(s string) bool { return s == in })
	for mask := 0; mask < 1<<len(others); mask++ {
		assign := map[string]Truth{}
		for i, o := range others {
			assign[o] = truthBit(mask, i)
		}
		var decisions [2]bool
		admissible := true
		for k, val := range []Truth{False, True} {
			assign[in] = val
			if !allowed(assign, implications) {
				admissible = false
				break
			}
			d, err := eval(g.Site, atomVector(g.Atoms, assign))
			if err != nil {
				return nil, err
			}
			decisions[k] = d
		}
		if !admissible || decisions[0] == decisions[1] {
			continue
		}
		out = append(out, formatAssignment(inputs, in, assign))
	}
	return out, nil
}

func truthBit(mask, i int) Truth {
	if mask&(1<<i) != 0 {
		return True
	}
	return False
}

func allowed(assign map[string]Truth, implications []Implication) bool {
	for _, imp := range implications {
		a, aok := assign[imp.If]
		b, bok := assign[imp.Then]
		if aok && bok && a == True && b == False {
			return false
		}
	}
	return true
}

func atomVector(atoms []string, assign map[string]Truth) []Truth {
	out := make([]Truth, len(atoms))
	for i, a := range atoms {
		out[i] = assign[a]
	}
	return out
}

func formatAssignment(inputs []string, in string, assign map[string]Truth) string {
	parts := make([]string, 0, len(inputs))
	for _, name := range inputs {
		if name == in {
			parts = append(parts, name+"=T/F")
			continue
		}
		parts = append(parts, name+"="+assign[name].String())
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func constraintReason(in string, inputs []string, implications []Implication) string {
	var used []string
	for _, imp := range implications {
		if slices.Contains(inputs, imp.If) && slices.Contains(inputs, imp.Then) {
			used = append(used, imp.If+" ⇒ "+imp.Then+" ("+imp.Source+")")
		}
	}
	if len(used) == 0 {
		return "the guard's logic masks " + in + " in every assignment"
	}
	return "masked under " + strings.Join(used, "; ")
}
