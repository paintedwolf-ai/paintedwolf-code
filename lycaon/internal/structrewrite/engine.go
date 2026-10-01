package structrewrite

import (
	"context"
	"sort"

	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
)

// PatternError identifies the pattern-compilation phase and retains its cause.
type PatternError struct{ Cause error }

func (e *PatternError) Error() string { return e.Cause.Error() }
func (e *PatternError) Unwrap() error { return e.Cause }

// Request is a single structural search or rewrite over one source buffer.
type Request struct {
	LangName string // explicit grammar name (preferred); falls back to Filename
	Filename string // used for grammar detection when LangName is empty
	Source   []byte
	Pattern  string // pattern code with $VAR / $$$VAR placeholders
	Fix      string // replacement template; empty => search only
}

// Match is one structural match with its location and captured bindings.
type Match struct {
	StartByte int               `json:"start_byte"`
	EndByte   int               `json:"end_byte"`
	StartRow  int               `json:"start_row"` // 0-based
	StartCol  int               `json:"start_col"` // 0-based
	EndRow    int               `json:"end_row"`
	EndCol    int               `json:"end_col"`
	Text      string            `json:"text"`
	Bindings  map[string]string `json:"bindings,omitempty"`
}

// Result is the outcome of Run.
type Result struct {
	Language  string  `json:"language"`
	Matches   []Match `json:"matches"`
	Rewritten []byte  `json:"-"`       // nil when Fix is empty
	Changed   bool    `json:"changed"` // true when a rewrite altered the source
}

// Run searches the source and applies Fix when provided.
func Run(ctx context.Context, req Request) (res *Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			res, err = nil, sourceAnalysisRecovered("structural match", req.LangName, len(req.Source), r)
		}
	}()
	return execute(ctx, req)
}

func execute(ctx context.Context, req Request) (*Result, error) {
	lang, langName, err := resolveLanguage(req.LangName, req.Filename)
	if err != nil {
		return nil, err
	}
	cp, err := compilePattern(ctx, lang, langName, req.Pattern)
	if err != nil {
		return nil, &PatternError{Cause: err}
	}
	tree, err := tsparse.Parse(ctx, lang, req.Source, tsparse.Analysis)
	if err != nil {
		return nil, err
	}
	defer tree.Release()

	matches := search(cp.root, tree.RootNode(), lang, req.Source)
	res := &Result{Language: langName, Matches: matches}
	if req.Fix == "" {
		return res, nil
	}
	rewritten, changed := applyRewrite(req.Source, matches, req.Fix)
	res.Rewritten = rewritten
	res.Changed = changed
	return res, nil
}

// search walks the target tree in pre-order, attempting a match at every node.
func search(root *patternNode, treeRoot *gotreesitter.Node, lang *gotreesitter.Language, src []byte) []Match {
	var out []Match
	var walk func(n *gotreesitter.Node)
	walk = func(n *gotreesitter.Node) {
		if n == nil {
			return
		}
		env := newMatchEnv(lang, src)
		if matchNode(root, n, env) {
			out = append(out, makeMatch(n, env, src))
		}
		for i := 0; i < n.ChildCount(); i++ {
			walk(n.Child(i))
		}
	}
	walk(treeRoot)
	return out
}

func makeMatch(n *gotreesitter.Node, env *matchEnv, src []byte) Match {
	start, end := n.StartPoint(), n.EndPoint()
	m := Match{
		StartByte: int(n.StartByte()),
		EndByte:   int(n.EndByte()),
		StartRow:  int(start.Row),
		StartCol:  int(start.Column),
		EndRow:    int(end.Row),
		EndCol:    int(end.Column),
		Text:      n.Text(src),
	}
	if len(env.single) > 0 || len(env.multi) > 0 {
		m.Bindings = map[string]string{}
		for name, b := range env.single {
			m.Bindings[name] = b.text
		}
		for name, nodes := range env.multi {
			m.Bindings[name] = renderNodes(nodes, src)
		}
	}
	return m
}

// renderNodes preserves separators from an ellipsis capture's source span.
func renderNodes(nodes []*gotreesitter.Node, src []byte) string {
	if len(nodes) == 0 {
		return ""
	}
	start := int(nodes[0].StartByte())
	end := int(nodes[len(nodes)-1].EndByte())
	return string(src[start:end])
}

// applyRewrite replaces non-overlapping matches, keeping outer matches.
func applyRewrite(src []byte, matches []Match, fix string) ([]byte, bool) {
	applied := nonOverlapping(matches)
	if len(applied) == 0 {
		return append([]byte(nil), src...), false
	}
	var out []byte
	cursor := 0
	for _, m := range applied {
		out = append(out, src[cursor:m.StartByte]...)
		out = append(out, renderFix(fix, m)...)
		cursor = m.EndByte
	}
	out = append(out, src[cursor:]...)
	return out, true
}

// nonOverlapping keeps the outer match when spans overlap.
func nonOverlapping(matches []Match) []Match {
	sorted := append([]Match(nil), matches...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].StartByte != sorted[j].StartByte {
			return sorted[i].StartByte < sorted[j].StartByte
		}
		return sorted[i].EndByte > sorted[j].EndByte
	})
	out := make([]Match, 0, len(sorted))
	lastEnd := -1
	for _, m := range sorted {
		if m.StartByte < lastEnd {
			continue
		}
		out = append(out, m)
		lastEnd = m.EndByte
	}
	return out
}
