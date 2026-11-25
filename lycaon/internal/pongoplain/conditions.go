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
	Count      int
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
	source string
	once   sync.Once
	expr   *Expr
	atoms  []string
	tpls   []*pongo2.Template
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
		for _, atom := range expr.Atoms() {
			src := atom.Source()
			tpl, err := Compile("{% if " + src + " %}1{% else %}0{% endif %}")
			if err != nil {
				tpl = nil
			}
			arm.atoms = append(arm.atoms, src)
			arm.tpls = append(arm.tpls, tpl)
		}
	})
}

// observe evaluates each atom of arm in ctx's scope with the production
// evaluator and reports whether the atoms recompose decision. Atoms of an
// unparsable condition are not observed.
func (arm *conditionArm) observe(ctx *pongo2.ExecutionContext, decision bool) ([]string, []Truth, bool) {
	arm.compile()
	if len(arm.atoms) == 0 {
		return nil, nil, false
	}
	scope := pongo2.Context{}
	for _, layer := range []pongo2.Context{ctx.Shared, ctx.Public, ctx.Private} {
		for k, v := range layer {
			scope[k] = v
		}
	}
	scope[atomReplayKey] = true
	scope[execStateKey] = &executionState{ctx: context.Background()}
	values := make([]Truth, len(arm.tpls))
	for i, tpl := range arm.tpls {
		if tpl == nil {
			continue
		}
		var b strings.Builder
		if err := tpl.ExecuteWriterUnbuffered(scope, &b); err != nil {
			continue
		}
		switch b.String() {
		case "1":
			values[i] = True
		case "0":
			values[i] = False
		}
	}
	known := make(map[string]bool, len(values))
	for i, v := range values {
		if v == Unknown {
			return arm.atoms, values, false
		}
		known[arm.atoms[i]] = v == True
	}
	return arm.atoms, values, arm.expr.Decide(known) == decision
}

func isAtomReplay(ctx *pongo2.ExecutionContext) bool {
	replay, _ := ctx.Public[atomReplayKey].(bool)
	return replay
}

func (r *BranchRecorder) recordCondition(site branchSite, source string, atoms []string, values []Truth, decision, recomposes bool) {
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
		hit = &ConditionHit{Template: resolved, BranchID: branchID, Line: site.line, Col: site.col, Kind: site.kind, Source: source, Atoms: atoms}
		r.Conditions[branchID] = hit
	}
	for _, v := range hit.Vectors {
		if v.Decision == decision && slices.Equal(v.Atoms, values) {
			v.Count++
			return
		}
	}
	hit.Vectors = append(hit.Vectors, &ConditionVector{Atoms: values, Decision: decision, Recomposes: recomposes, Count: 1})
}
