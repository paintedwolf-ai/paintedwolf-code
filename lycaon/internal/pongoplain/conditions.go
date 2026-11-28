package pongoplain

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/flosch/pongo2/v6"
)

// Truth is an atom's value in one evaluation; an atom that fails to evaluate
// is Unknown.
type Truth int8

const (
	Unknown Truth = iota
	False
	True
)

// ConditionVector is one observed combination of atom values for a condition
// and the decision the condition produced.
type ConditionVector struct {
	Atoms    []Truth
	Decision bool
	// Recomposes reports that every atom is known and the condition's
	// connectives over their truths give Decision.
	Recomposes bool
	// Partition is the recorder's group for the render, and Path the include
	// tags enclosing the arm, outermost first.
	Partition string
	Path      []string
	// Label names the case of the first evaluation that gave this vector.
	Label string
	Count int
}

// ConditionHit collects the distinct vectors observed at one if or elif arm.
type ConditionHit struct {
	Template string
	BranchID string
	Line     int
	Col      int
	Kind     BranchKind
	Source   string
	Atoms    []string
	Vectors  []*ConditionVector
}

// atomReplayKey marks a context that replays one atom, so tracked tags inside
// the replay record nothing. Like execStateKey it lexes as a number, so
// templates cannot reference it.
const atomReplayKey = "1"

// conditionArm holds one arm's source and, once observed, its compiled atoms.
type conditionArm struct {
	source           string
	once             sync.Once
	expr             *Expr
	atoms            []string
	tpls             []*pongo2.Template
	atomRoots        [][]string
	directSingle     bool
	directTrueResult bool
}

// tokenSource rebuilds a tag argument's expression text from its tokens.
func tokenSource(p *pongo2.Parser) string {
	var b strings.Builder
	var prev *pongo2.Token
	for i := 0; i < p.Count(); i++ {
		tok := p.Get(i)
		text := tok.Val
		if tok.Typ == pongo2.TokenString {
			text = quoteLiteral(tok.Val)
		}
		if prev != nil && spaced(prev, tok) {
			b.WriteByte(' ')
		}
		b.WriteString(text)
		prev = tok
	}
	return b.String()
}

func spaced(prev, next *pongo2.Token) bool {
	symbol := func(t *pongo2.Token, vals string) bool {
		return t.Typ == pongo2.TokenSymbol && len(t.Val) == 1 && strings.Contains(vals, t.Val)
	}
	switch {
	case symbol(next, ".|:)],"), symbol(prev, ".|:(["):
		return false
	case symbol(next, "(") && prev.Typ == pongo2.TokenIdentifier:
		return false
	}
	return true
}

func (arm *conditionArm) compile() {
	arm.once.Do(func() {
		expr, err := ParseExpression(arm.source)
		if err != nil {
			return
		}
		arm.expr = expr
		exprAtoms := expr.Atoms()
		for _, atom := range exprAtoms {
			src := atom.Source()
			arm.atoms = append(arm.atoms, src)
			var roots []string
			for _, id := range atom.Identifiers() {
				root, _, _ := strings.Cut(id, ".")
				if root != "" && !slices.Contains(roots, root) {
					roots = append(roots, root)
				}
			}
			arm.atomRoots = append(arm.atomRoots, roots)
		}
		if len(arm.atoms) == 1 {
			tVal := expr.Decide(map[string]bool{arm.atoms[0]: true})
			fVal := expr.Decide(map[string]bool{arm.atoms[0]: false})
			if tVal != fVal {
				arm.directSingle = true
				arm.directTrueResult = tVal
				return
			}
		}
		for _, src := range arm.atoms {
			tpl, err := Compile("{% if " + src + " %}1{% else %}0{% endif %}")
			if err != nil {
				tpl = nil
			}
			arm.tpls = append(arm.tpls, tpl)
		}
	})
}

func lookupExecContext(ctx *pongo2.ExecutionContext, key string) (any, bool) {
	if v, ok := ctx.Private[key]; ok {
		return v, true
	}
	if v, ok := ctx.Public[key]; ok {
		return v, true
	}
	if v, ok := ctx.Shared[key]; ok {
		return v, true
	}
	return nil, false
}

// armObservation is one evaluation of an arm: its atoms, their values, the
// partition and include path of the render, and the decision.
type armObservation struct {
	atoms      []string
	values     []Truth
	decision   bool
	recomposes bool
	partition  string
	path       []string
}

// observe evaluates each atom of arm in ctx's scope with the production
// evaluator and reports whether the atoms recompose decision. Atoms of an
// unparsable condition are not observed.
func (arm *conditionArm) observe(ctx *pongo2.ExecutionContext, decision bool, partition func(map[string]any) string) armObservation {
	arm.compile()
	obs := armObservation{decision: decision}
	if partition != nil {
		if len(ctx.Shared) == 0 && len(ctx.Private) == 0 {
			obs.partition = partition(ctx.Public)
		} else {
			scope := make(pongo2.Context, len(ctx.Shared)+len(ctx.Public)+len(ctx.Private))
			for _, layer := range []pongo2.Context{ctx.Shared, ctx.Public, ctx.Private} {
				for k, v := range layer {
					scope[k] = v
				}
			}
			obs.partition = partition(scope)
		}
	}
	if len(arm.atoms) == 0 {
		return obs
	}
	obs.atoms = arm.atoms
	if arm.directSingle {
		val := False
		if decision == arm.directTrueResult {
			val = True
		}
		obs.values = []Truth{val}
		obs.recomposes = true
		return obs
	}
	obs.values = make([]Truth, len(arm.tpls))
	for i, tpl := range arm.tpls {
		if tpl == nil {
			continue
		}
		roots := arm.atomRoots[i]
		atomScope := make(pongo2.Context, len(roots)+2)
		for _, root := range roots {
			if v, ok := lookupExecContext(ctx, root); ok {
				atomScope[root] = v
			}
		}
		atomScope[atomReplayKey] = true
		atomScope[execStateKey] = &executionState{ctx: context.Background()}
		var b strings.Builder
		if err := tpl.ExecuteWriterUnbuffered(atomScope, &b); err != nil {
			continue
		}
		switch b.String() {
		case "1":
			obs.values[i] = True
		case "0":
			obs.values[i] = False
		}
	}
	known := make(map[string]bool, len(obs.values))
	for i, v := range obs.values {
		if v == Unknown {
			return obs
		}
		known[arm.atoms[i]] = v == True
	}
	obs.recomposes = arm.expr.Decide(known) == decision
	return obs
}

func isAtomReplay(ctx *pongo2.ExecutionContext) bool {
	replay, _ := ctx.Public[atomReplayKey].(bool)
	return replay
}

func (r *BranchRecorder) recordCondition(site branchSite, source string, obs armObservation) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	if r.Conditions == nil {
		r.Conditions = make(map[string]*ConditionHit)
	}
	resolved := site.filename
	if alias := r.Aliases[site.filename]; alias != "" {
		resolved = alias
	}
	branchID := branchKey(resolved, site.line, site.col, site.kind)
	hit, ok := r.Conditions[branchID]
	if !ok {
		hit = &ConditionHit{Template: resolved, BranchID: branchID, Line: site.line, Col: site.col, Kind: site.kind, Source: source, Atoms: obs.atoms}
		r.Conditions[branchID] = hit
	}
	for _, v := range hit.Vectors {
		if v.Decision == obs.decision && v.Partition == obs.partition && slices.Equal(v.Atoms, obs.values) && slices.Equal(v.Path, obs.path) {
			v.Count++
			return
		}
	}
	v := &ConditionVector{
		Atoms: obs.values, Decision: obs.decision, Recomposes: obs.recomposes,
		Partition: obs.partition, Path: slices.Clone(obs.path), Count: 1,
	}
	if r.Label != nil {
		v.Label = r.Label()
	}
	hit.Vectors = append(hit.Vectors, v)
}
