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
	case Unknown:
	}
	return "?"
}

// Vector is one observed evaluation of a guard. Only vectors of one Group
// pair as a witness; Case names what produced the vector.
type Vector struct {
	Atoms    []Truth
	Decision bool
	Group    string
	Case     string
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

// Fixed is a host invariant holding one atom to one value. Source names the
// production code that establishes it.
type Fixed struct {
	Atom   string
	Value  Truth
	Source string
}

// Joint is a host invariant over several atoms: only the listed truth vectors,
// in Atoms order, can occur together. Source names the expression or
// production code that establishes it.
type Joint struct {
	Atoms   []string
	Allowed [][]Truth
	Source  string
}

func (j Joint) admits(values func(string) Truth) bool {
	got := make([]Truth, len(j.Atoms))
	for i, a := range j.Atoms {
		if got[i] = values(a); got[i] == Unknown {
			return true
		}
	}
	return slices.ContainsFunc(j.Allowed, func(v []Truth) bool { return slices.Equal(v, got) })
}

// Constraints are the host invariants that rule assignments out.
type Constraints struct {
	Implications []Implication
	Fixed        []Fixed
	Joints       []Joint
}

// Witness is a pair of evaluations that differ only in one input and in the
// decision.
type Witness struct {
	Input      string
	With, Then Vector
}

// Gap is an input with neither a witness pair nor a constraint ruling every
// pair out. Needs holds the first assignments of the other inputs, keyed by
// input, under which a pair would witness Input.
type Gap struct {
	Site    string
	Source  string
	Input   string
	Missing []string
	Needs   []map[string]Truth
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
	// Contradicted lists constraints an observation violates.
	Contradicted []string
}

// Analyze checks every input of every guard. Atoms with equal source are one
// input. Two evaluations witness an input when its value differs, every other
// input holds the same known value, and the decisions differ. Without a
// witness, an input is excluded only when no assignment satisfying the
// constraints and the guard's own logic admits a pair; otherwise it is a gap
// naming the assignments to observe.
func Analyze(guards []Guard, eval func(site string, atoms []Truth) (bool, error), cons Constraints) (Result, error) {
	res := Result{Witnesses: map[string][]Witness{}}
	for _, g := range guards {
		for _, v := range g.Vectors {
			for _, imp := range cons.Implications {
				if a, b := valueOf(g, v, imp.If), valueOf(g, v, imp.Then); a == True && b == False {
					res.Contradicted = append(res.Contradicted, fmt.Sprintf("%s (%s) contradicted at %s by %s", imp.If+" ⇒ "+imp.Then, imp.Source, g.Site, v.Case))
				}
			}
			for _, f := range cons.Fixed {
				if got := valueOf(g, v, f.Atom); got != Unknown && got != f.Value {
					res.Contradicted = append(res.Contradicted, fmt.Sprintf("%s = %s (%s) contradicted at %s by %s", f.Atom, f.Value, f.Source, g.Site, v.Case))
				}
			}
			for _, j := range cons.Joints {
				if !j.admits(func(a string) Truth { return valueOf(g, v, a) }) {
					res.Contradicted = append(res.Contradicted, fmt.Sprintf("joint %s (%s) contradicted at %s by %s", strings.Join(j.Atoms, ", "), j.Source, g.Site, v.Case))
				}
			}
		}
	}
	slices.Sort(res.Contradicted)
	res.Contradicted = slices.Compact(res.Contradicted)
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
			needed, needs, err := neededAssignments(g, in, inputs, eval, cons)
			if err != nil {
				return Result{}, err
			}
			if len(needed) == 0 {
				res.Exclusions = append(res.Exclusions, Exclusion{Site: g.Site, Input: in, Reason: constraintReason(in, inputs, cons)})
				continue
			}
			res.Gaps = append(res.Gaps, Gap{Site: g.Site, Source: g.Source, Input: in, Missing: needed, Needs: needs})
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
			if a.Decision == b.Decision || a.Group != b.Group {
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

// enumerationBound caps the inputs whose assignments neededAssignments walks.
const enumerationBound = 20

// maxListed caps the assignments a gap prints; maxNeeds caps those it keeps
// as structured targets.
const (
	maxListed = 4
	maxNeeds  = 16
)

// neededAssignments lists the assignments of the other inputs under which the
// guard's decision follows in and the constraints allow, printed as the pair
// of input vectors to observe and kept as structured targets. Flips of
// observed vectors come first, since one more observation next to an existing
// one closes the gap; beyond maxListed, the count of the rest is given.
func neededAssignments(g Guard, in string, inputs []string, eval func(string, []Truth) (bool, error), cons Constraints) ([]string, []map[string]Truth, error) {
	if len(inputs) > enumerationBound {
		return nil, nil, fmt.Errorf("%s: %d inputs exceed the enumeration bound", g.Site, len(inputs))
	}
	others := slices.DeleteFunc(slices.Clone(inputs), func(s string) bool { return s == in })
	var observed, rest []string
	var obsNeeds, restNeeds []map[string]Truth
	seen := map[string]bool{}
	consider := func(assign map[string]Truth, fromObservation bool) error {
		var decisions [2]bool
		for k, val := range []Truth{False, True} {
			assign[in] = val
			if !allowed(assign, cons) {
				return nil
			}
			d, err := eval(g.Site, atomVector(g.Atoms, assign))
			if err != nil {
				return err
			}
			decisions[k] = d
		}
		if decisions[0] == decisions[1] {
			return nil
		}
		text := formatAssignment(inputs, in, assign)
		if seen[text] {
			return nil
		}
		seen[text] = true
		need := make(map[string]Truth, len(others))
		for _, o := range others {
			need[o] = assign[o]
		}
		if fromObservation {
			observed, obsNeeds = append(observed, text), append(obsNeeds, need)
		} else {
			rest, restNeeds = append(rest, text), append(restNeeds, need)
		}
		return nil
	}
	for _, v := range g.Vectors {
		assign := map[string]Truth{}
		known := true
		for _, o := range others {
			if assign[o] = valueOf(g, v, o); assign[o] == Unknown {
				known = false
			}
		}
		if known {
			if err := consider(assign, true); err != nil {
				return nil, nil, err
			}
		}
	}
	for mask := 0; mask < 1<<len(others); mask++ {
		assign := map[string]Truth{}
		for i, o := range others {
			assign[o] = truthBit(mask, i)
		}
		if err := consider(assign, false); err != nil {
			return nil, nil, err
		}
	}
	all, needs := slices.Concat(observed, rest), slices.Concat(obsNeeds, restNeeds)
	if len(needs) > maxNeeds {
		needs = needs[:maxNeeds]
	}
	if len(all) > maxListed {
		return append(all[:maxListed:maxListed], fmt.Sprintf("%d more", len(all)-maxListed)), needs, nil
	}
	return all, needs, nil
}

func truthBit(mask, i int) Truth {
	if mask&(1<<i) != 0 {
		return True
	}
	return False
}

func allowed(assign map[string]Truth, cons Constraints) bool {
	for _, imp := range cons.Implications {
		a, aok := assign[imp.If]
		b, bok := assign[imp.Then]
		if aok && bok && a == True && b == False {
			return false
		}
	}
	for _, f := range cons.Fixed {
		if v, ok := assign[f.Atom]; ok && v != f.Value {
			return false
		}
	}
	for _, j := range cons.Joints {
		if !j.admits(func(a string) Truth { return assign[a] }) {
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

func constraintReason(in string, inputs []string, cons Constraints) string {
	var used []string
	for _, imp := range cons.Implications {
		if slices.Contains(inputs, imp.If) && slices.Contains(inputs, imp.Then) {
			used = append(used, imp.If+" ⇒ "+imp.Then+" ("+imp.Source+")")
		}
	}
	for _, f := range cons.Fixed {
		if slices.Contains(inputs, f.Atom) {
			used = append(used, f.Atom+" = "+f.Value.String()+" ("+f.Source+")")
		}
	}
	for _, j := range cons.Joints {
		if slices.Contains(j.Atoms, in) {
			used = append(used, "joint "+strings.Join(j.Atoms, ", ")+" ("+j.Source+")")
		}
	}
	if len(used) == 0 {
		return "the guard's logic masks " + in + " in every assignment"
	}
	return "masked under " + strings.Join(used, "; ")
}
