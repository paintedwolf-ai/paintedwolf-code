package structrewrite

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/lycaon/lycaon/internal/tsparse"
	"github.com/odvcencio/gotreesitter"
)

func byteOffset(n int) (uint32, bool) {
	if n < 0 || n > math.MaxUint32 {
		return 0, false
	}
	return uint32(n), true
}

// errEmptyPattern reports patterns with no usable syntax node.
var errEmptyPattern = errors.New("pattern has no parsable content")

// errPatternNotFaithful reports a parse that dropped pattern tokens.
var errPatternNotFaithful = errors.New("pattern could not be parsed as one complete node — write the whole shape (a function needs its parameters, result, and body)")

// metaVar describes a `$NAME` / `$$$NAME` placeholder extracted from a pattern.
type metaVar struct {
	name     string // canonical name without the sigil; "" for anonymous `$$$`
	ellipsis bool   // true for `$$$` (matches a sequence of sibling nodes)
	capture  bool   // false for `$_` / anonymous — matches without binding
}

// patternNode is one compiled pattern subtree.
type patternNode struct {
	kind     string // gotreesitter node type
	field    string // grammar field name in the parent ("" when the slot is unnamed)
	meta     *metaVar
	terminal bool
	isNamed  bool
	text     string // terminal source text (post-expando)
	children []*patternNode
}

// compiledPattern is a pattern ready to match against target trees.
type compiledPattern struct {
	root    *patternNode
	expando rune
}

// patternWrapper supplies a valid context for a syntax fragment.
type patternWrapper struct {
	prefix string
	suffix string
}

// patternWrappers supplies statement contexts for restrictive grammars.
var patternWrappers = map[string]patternWrapper{
	"go":   {prefix: "package p\nfunc _() {\n", suffix: "\n}\n"},
	"java": {prefix: "class C { void m() {\n", suffix: "\n} }\n"},
	"rust": {prefix: "fn _f() {\n", suffix: "\n}\n"},
	// The suffix closes declarations without entering the extraction range.
	"c":       {prefix: "void _f() {\n", suffix: ";\n}\n"},
	"cpp":     {prefix: "void _f() {\n", suffix: ";\n}\n"},
	"c_sharp": {prefix: "class C { void M() {\n", suffix: ";\n} }\n"},
	"php":     {prefix: "<?php\n", suffix: ";\n"},
	"hack":    {prefix: "<?hh\n", suffix: ";\n"},
	"apex":    {prefix: "class C { void m() {\n", suffix: ";\n} }\n"},
	// Call patterns carry their own terminator in this grammar.
	"dart":  {prefix: "void _f() {\n", suffix: "\n}\n"},
	"cairo": {prefix: "fn _f() {\n", suffix: ";\n}\n"},
	// Contract context admits statement patterns.
	"solidity": {prefix: "contract C { function _f() public {\n", suffix: ";\n} }\n"},
	// Module context requires an address.
	"move": {prefix: "module 0x1::m {\nfun _f() {\n", suffix: ";\n}\n}\n"},
	// Line-oriented instructions require a trailing newline.
	"dockerfile": {suffix: "\n"},
}

var patternDeclarationWrappers = map[string]patternWrapper{
	"move": {prefix: "module 0x1::m {\n", suffix: "\n}\n"},
}

func patternContexts(langName string) []patternWrapper {
	out := make([]patternWrapper, 0, 2)
	if wrapper, ok := patternWrappers[langName]; ok {
		out = append(out, wrapper)
	}
	if wrapper, ok := patternDeclarationWrappers[langName]; ok {
		out = append(out, wrapper)
	}
	return out
}

// preprocessPattern substitutes sigils that represent metavariables.
func preprocessPattern(query string, expando rune) string {
	if expando == '$' {
		return query
	}
	var b strings.Builder
	b.Grow(len(query))
	dollars := 0
	emit := func(next rune, hasNext bool) {
		if dollars == 0 {
			if hasNext {
				b.WriteRune(next)
			}
			return
		}
		needReplace := dollars == 3 || (hasNext && (next == '_' || (next >= 'A' && next <= 'Z')))
		sigil := '$'
		if needReplace {
			sigil = expando
		}
		for i := 0; i < dollars; i++ {
			b.WriteRune(sigil)
		}
		dollars = 0
		if hasNext {
			b.WriteRune(next)
		}
	}
	for _, c := range query {
		if c == '$' {
			dollars++
			continue
		}
		emit(c, true)
	}
	emit(0, false)
	return b.String()
}

// extractMetaVar decodes a metavariable identifier.
func extractMetaVar(text string, expando rune) (metaVar, bool) {
	rs := []rune(text)
	n := 0
	for n < len(rs) && rs[n] == expando {
		n++
	}
	if n != 1 && n != 3 {
		return metaVar{}, false
	}
	name := string(rs[n:])
	if name == "" {
		if n != 3 {
			return metaVar{}, false // a bare single sigil is not a metavar
		}
	} else if !isMetaName(name) {
		return metaVar{}, false
	}
	return metaVar{
		name:     name,
		ellipsis: n == 3,
		capture:  name != "" && name != "_",
	}, true
}

// extractQuotedMetaVar decodes a whole-string metavariable.
func extractQuotedMetaVar(text string, expando rune) (metaVar, bool) {
	rs := []rune(text)
	if len(rs) < 3 {
		return metaVar{}, false
	}
	q := rs[0]
	if q != '"' && q != '\'' && q != '`' {
		return metaVar{}, false
	}
	if rs[len(rs)-1] != q {
		return metaVar{}, false
	}
	return extractMetaVar(string(rs[1:len(rs)-1]), expando)
}

// isMetaName reports whether s is a valid metavariable name.
func isMetaName(s string) bool {
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'A' && r <= 'Z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return s != ""
}

// compilePattern accepts the first complete, token-faithful parse context.
func compilePattern(ctx context.Context, lang *gotreesitter.Language, langName, pattern string) (*compiledPattern, error) {
	anyParsed := false
	for _, expando := range expandoCandidates(langName) {
		cp, parsed, err := compileWithExpando(ctx, lang, langName, pattern, expando)
		if err != nil {
			return nil, err
		}
		if cp != nil {
			return cp, nil
		}
		anyParsed = anyParsed || parsed
	}
	if anyParsed {
		return nil, errPatternNotFaithful
	}
	return nil, errEmptyPattern
}

// compileWithExpando tries complete parse contexts for one placeholder rune.
func compileWithExpando(ctx context.Context, lang *gotreesitter.Language, langName, pattern string, expando rune) (*compiledPattern, bool, error) {
	processed := preprocessPattern(pattern, expando)
	parsed := false
	for _, wrapper := range append(patternContexts(langName), patternWrapper{}) {
		candidate, err := parsePattern(ctx, lang, processed, wrapper)
		if err != nil {
			return nil, false, err
		}
		if candidate == nil {
			continue
		}
		parsed = true
		root := candidate.lower(lang, expando, nil)
		if patternFaithful(root, processed) {
			return &compiledPattern{root: root, expando: expando}, true, nil
		}
	}
	cp, err := compileElided(ctx, lang, langName, processed, expando)
	return cp, parsed || cp != nil, err
}

// patternFaithful requires every non-whitespace pattern token in the compiled tree.
func patternFaithful(root *patternNode, processed string) bool {
	// A whitespace-bearing terminal is an unparsed fragment.
	if root != nil && root.terminal && strings.ContainsAny(root.text, " \t\n\r") {
		return false
	}
	var b strings.Builder
	appendPatternText(root, &b)
	return squeeze(b.String()) == squeeze(processed)
}

// squeeze drops all whitespace so two token sequences compare on content alone.
func squeeze(s string) string { return strings.Join(strings.Fields(s), "") }

func appendPatternText(n *patternNode, b *strings.Builder) {
	if n == nil {
		return
	}
	if n.meta != nil || n.terminal {
		b.WriteString(n.text)
		return
	}
	for _, c := range n.children {
		appendPatternText(c, b)
	}
}

type patternTree struct {
	tree   *gotreesitter.Tree
	node   *gotreesitter.Node
	source []byte
}

// parsePattern owns its tree until the caller converts the selected node.
func parsePattern(ctx context.Context, lang *gotreesitter.Language, processed string, wrapper patternWrapper) (result *patternTree, err error) {
	src := []byte(wrapper.prefix + processed + wrapper.suffix)
	tree, err := tsparse.Parse(ctx, lang, src, tsparse.Analysis)
	if err != nil {
		return nil, err
	}
	defer func() {
		if result == nil {
			tree.Release()
		}
	}()
	start, startOK := byteOffset(len(wrapper.prefix))
	end, endOK := byteOffset(len(wrapper.prefix) + len(processed))
	if !startOK || !endOK {
		return nil, nil
	}
	node := tree.RootNode()
	if node != nil && (wrapper.prefix != "" || wrapper.suffix != "") {
		node = node.NamedDescendantForByteRange(start, end)
	}
	node = singleMatcher(node, lang)
	if node == nil || node.HasError() || node.EndByte() == node.StartByte() || hasErrorAncestorWithin(node, start, end) {
		return nil, nil
	}
	return &patternTree{tree: tree, node: node, source: src}, nil
}

// hasErrorAncestorWithin detects recovery errors inside the pattern span.
func hasErrorAncestorWithin(n *gotreesitter.Node, lo, hi uint32) bool {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if p.IsError() && p.StartByte() >= lo && p.EndByte() <= hi {
			return true
		}
	}
	return false
}

// singleMatcher removes transparent single-child syntax wrappers.
func singleMatcher(root *gotreesitter.Node, lang *gotreesitter.Language) *gotreesitter.Node {
	inner := root
	for isSingleNode(inner, lang) {
		child := inner.Child(0)
		if child == nil {
			break
		}
		inner = child
	}
	return inner
}

func isSingleNode(n *gotreesitter.Node, lang *gotreesitter.Language) bool {
	if n == nil {
		return false
	}
	switch n.ChildCount() {
	case 1:
		return true
	case 2:
		c := n.Child(1)
		return c != nil && (c.IsMissing() || c.Type(lang) == "")
	default:
		return false
	}
}

// convertNode lowers a syntax node and restores elided sequence variables.
func convertNode(n *gotreesitter.Node, lang *gotreesitter.Language, src []byte, expando rune, holes *holeCursor) *patternNode {
	text := n.Text(src)
	// Trim identifier padding before decoding metavariables.
	trimmed := strings.TrimSpace(text)
	if mv, ok := extractMetaVar(trimmed, expando); ok {
		return &patternNode{kind: n.Type(lang), meta: &mv, text: trimmed}
	}
	if mv, ok := extractQuotedMetaVar(trimmed, expando); ok {
		return &patternNode{kind: n.Type(lang), meta: &mv, text: trimmed}
	}
	if n.ChildCount() == 0 {
		return &patternNode{
			kind:     n.Type(lang),
			terminal: true,
			isNamed:  n.IsNamed(),
			text:     text,
		}
	}
	kids := make([]*patternNode, 0, n.ChildCount())
	for i := 0; i < n.ChildCount(); i++ {
		c := n.Child(i)
		if c == nil || c.IsMissing() {
			continue
		}
		// Attach each elided sequence to its containing child list.
		kids = holes.drainThrough(kids, int(c.StartByte()))
		kid := convertNode(c, lang, src, expando, holes)
		kid.field = n.FieldNameForChild(i, lang)
		kids = append(kids, kid)
	}
	kids = holes.drainBefore(kids, int(n.EndByte()))
	return &patternNode{kind: n.Type(lang), children: kids}
}

func (p *patternTree) lower(lang *gotreesitter.Language, expando rune, holes *holeCursor) *patternNode {
	defer p.tree.Release()
	return convertNode(p.node, lang, p.source, expando, holes)
}
