package search

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SymbolLegCap bounds declarations per project on a complete search.
const SymbolLegCap = 200

// symbolNameMinRunes is the shortest term the symbol arm answers.
const symbolNameMinRunes = 2

// Symbol hits share the code family's scale: an exact declaration name ranks
// just above an exact file name, and weaker name matches step down while
// staying above plain content lines. Indexes are SymbolDeclaration.MatchRank.
var symbolRankScores = [...]float64{0.98, 0.97, 0.9, 0.85, 0.8, 0.7}

// symbolPositionStep keeps a project's declarations in their ranked order
// within one match rank.
const symbolPositionStep = 1e-5

// SymbolDeclaration is one declaration the symbol leg found.
type SymbolDeclaration struct {
	ProjectID string
	RootID    string
	Path      string
	Line      int
	Name      string
	Kind      string
	// Highlights are the code point ranges of Name the query matched.
	Highlights []TextRange
	// Signature is the declaration's source line.
	Signature string
	// MatchRank orders how Name answers the query, 0 strongest: the query's
	// own spelling, the whole name, a prefix, a word start, an abbreviation,
	// then a substring.
	MatchRank int
}

// NewSymbolHit projects a declaration as a search hit. position is the
// declaration's place in its project's ranked list.
func NewSymbolHit(d SymbolDeclaration, position int) Hit {
	rank := min(max(d.MatchRank, 0), len(symbolRankScores)-1)
	return Hit{
		ID:              stableHitID(HitKindSymbol, d.ProjectID, d.RootID, d.Path, d.Name, d.Kind, strconv.Itoa(d.Line)),
		HitKind:         HitKindSymbol,
		Source:          SourceCode,
		Score:           symbolRankScores[rank] - float64(position)*symbolPositionStep,
		ProjectID:       d.ProjectID,
		RootID:          d.RootID,
		Path:            d.Path,
		Line:            d.Line,
		Title:           d.Name,
		Context:         pathLineLabel(d.Path, d.Line),
		Snippet:         d.Signature,
		TitleHighlights: append([]TextRange(nil), d.Highlights...),
		SymbolKind:      d.Kind,
	}
}

// symbolName is the query's one positive term when it can name a declaration:
// a single term of at least two characters without whitespace, outside regex
// mode. Declaration names have no spaces, so a multi-term query has no arm.
func (st *compileState) symbolName() string {
	if st.ctx.Flags.Regex || len(st.ftsTerms) != 1 {
		return ""
	}
	term := strings.TrimSpace(st.ftsTerms[0])
	if utf8.RuneCountInString(term) < symbolNameMinRunes || strings.ContainsFunc(term, unicode.IsSpace) {
		return ""
	}
	return term
}

func (st *compileState) buildSymbolLeg(name string) (*SymbolPlanLeg, error) {
	roots, err := st.codePathRoots()
	if err != nil {
		return nil, err
	}
	return &SymbolPlanLeg{
		Query:       st.ast,
		Name:        name,
		Roots:       orderCodeRoots(roots, st.ctx.OriginProjectID),
		Cap:         st.ctx.Budget.symbolCap(),
		ExcludeDirs: st.lineExcludeDirs(append([]string(nil), st.ctx.DependencyPathPatterns...)),
		Flags:       st.ctx.Flags,
		Budget:      st.ctx.Budget,
	}, nil
}

// DiscoveryScope is the part of the leg's query that says where declarations
// may live: its top-level path filters, negated or not. Discovery ANDs them
// with each content pattern, which also admits hidden trees a path names, the
// way the code leg does; SymbolFilter then applies the whole query.
func (l *SymbolPlanLeg) DiscoveryScope() []Node {
	var scope []Node
	conjuncts := []Node{l.Query}
	if and, ok := l.Query.(AndExpr); ok {
		conjuncts = and.Exprs
	}
	for _, node := range conjuncts {
		target := node
		if not, ok := node.(NotExpr); ok {
			target = not.Expr
		}
		if filter, ok := target.(FilterExpr); ok && strings.EqualFold(strings.TrimSpace(filter.Field), "path") {
			scope = append(scope, node)
		}
	}
	return scope
}

// SymbolFilter admits the declarations a symbol leg's query allows.
type SymbolFilter struct {
	matcher codeQueryMatcher
	paths   pathGlobFilter
}

// CompileSymbolFilter compiles the leg's filters, negated terms, and path
// globs. The positive term already chose the declarations by name, so it
// admits every name here.
func CompileSymbolFilter(leg *SymbolPlanLeg) (SymbolFilter, error) {
	matcher, err := compileSymbolQuery(leg.Query, leg.Flags, false)
	if err != nil {
		return SymbolFilter{}, err
	}
	paths, err := compileCodePaths(leg.Query, leg.Flags)
	if err != nil {
		return SymbolFilter{}, err
	}
	return SymbolFilter{matcher: matcher, paths: paths}, nil
}

// Admits reports whether a declaration named name at path satisfies the query.
func (f SymbolFilter) Admits(path, name string) bool {
	return f.paths.allowsCode(path) && f.matcher.matches(codeCandidate{kind: HitKindSymbol, text: name, path: path})
}

func compileSymbolQuery(n Node, flags MatchFlags, negated bool) (codeQueryMatcher, error) {
	switch v := n.(type) {
	case AndExpr:
		children, err := compileSymbolChildren(v.Exprs, flags, negated)
		return codeAndMatcher{children: children}, err
	case OrExpr:
		children, err := compileSymbolChildren(v.Exprs, flags, negated)
		return codeOrMatcher{children: children}, err
	case NotExpr:
		child, err := compileSymbolQuery(v.Expr, flags, !negated)
		if err != nil {
			return nil, err
		}
		return codeNotMatcher{child: child}, nil
	case TextExpr:
		if !negated {
			return symbolNameAdmitted{}, nil
		}
		return compileCodeQuery(v, flags)
	default:
		return compileCodeQuery(n, flags)
	}
}

func compileSymbolChildren(nodes []Node, flags MatchFlags, negated bool) ([]codeQueryMatcher, error) {
	children := make([]codeQueryMatcher, 0, len(nodes))
	for _, node := range nodes {
		child, err := compileSymbolQuery(node, flags, negated)
		if err != nil {
			return nil, err
		}
		children = append(children, child)
	}
	return children, nil
}

// symbolNameAdmitted stands for the positive term the name ranking answered.
type symbolNameAdmitted struct{}

func (symbolNameAdmitted) matches(codeCandidate) bool { return true }
