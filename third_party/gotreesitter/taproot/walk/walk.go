// Package walk is the grammar-free core of taproot: load a tree-sitter Language
// from a pre-generated blob and navigate the CST with a Walker. It depends only
// on the gotreesitter runtime — NOT on grammargen or the grammars registry — so
// a DSL that embeds a generated grammar blob can parse and highlight without
// linking the ~200-grammar registry (~22MB). The grammargen-backed fallback
// helpers (build-from-DSL) stay in the parent taproot package.
package walk

import (
	"fmt"
	"strings"
	"sync"

	gts "github.com/odvcencio/gotreesitter"
)

// ── Language cache ────────────────────────────────────────────────────────────

type langEntry struct {
	lang *gts.Language
	err  error
}

var (
	cacheMu sync.Mutex
	cache   = map[string]langEntry{}
)

// LanguageFromBlob loads and caches (once per name) the tree-sitter Language
// from a pre-generated grammar blob. Grammar-free: there is no grammargen DSL
// fallback (use taproot.LanguageFromBlob for that).
func LanguageFromBlob(name string, blob []byte) (*gts.Language, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()

	if e, ok := cache[name]; ok {
		return e.lang, e.err
	}

	var lang *gts.Language
	var err error
	if len(blob) > 0 {
		lang, err = gts.LoadLanguage(blob)
	} else {
		err = fmt.Errorf("no grammar blob for %q", name)
	}
	if lang == nil && err == nil {
		err = fmt.Errorf("failed to load grammar blob for %q", name)
	}
	cache[name] = langEntry{lang: lang, err: err}
	return lang, err
}

// ── Walker ────────────────────────────────────────────────────────────────────

// Walker bundles a parse's language and source bytes with CST cursor helpers.
type Walker struct {
	Lang *gts.Language
	Src  []byte
}

// NewWalker constructs a Walker for the given language and source.
func NewWalker(lang *gts.Language, src []byte) *Walker {
	return &Walker{Lang: lang, Src: src}
}

// Type returns the grammar type name of n.
func (w *Walker) Type(n *gts.Node) string {
	if n == nil {
		return ""
	}
	return n.Type(w.Lang)
}

// Text returns the source text spanned by n.
func (w *Walker) Text(n *gts.Node) string {
	if n == nil {
		return ""
	}
	return n.Text(w.Src)
}

// Field returns the child of n that is bound to the named field, or nil.
func (w *Walker) Field(n *gts.Node, field string) *gts.Node {
	if n == nil {
		return nil
	}
	return n.ChildByFieldName(field, w.Lang)
}

// ChildByType returns the first direct child of n with grammar type typ, or nil.
func (w *Walker) ChildByType(n *gts.Node, typ string) *gts.Node {
	if n == nil {
		return nil
	}
	for i := 0; i < n.ChildCount(); i++ {
		c := n.Child(i)
		if w.Type(c) == typ {
			return c
		}
	}
	return nil
}

// Pos returns the 1-based line and column where n begins.
func (w *Walker) Pos(n *gts.Node) (line, col int) {
	if n == nil {
		return 1, 1
	}
	pt := n.StartPoint()
	return int(pt.Row) + 1, int(pt.Column) + 1
}

// SyntaxError walks the subtree rooted at root to find the best error or
// missing leaf and returns a formatted error.
func (w *Walker) SyntaxError(root *gts.Node) error {
	var best *gts.Node
	bestMissing := false

	var walk func(n *gts.Node)
	walk = func(n *gts.Node) {
		bad := n.Type(w.Lang) == "ERROR" || n.IsError() || n.IsMissing()
		childBad := false
		for i := 0; i < n.ChildCount(); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			if c.Type(w.Lang) == "ERROR" || c.IsError() || c.IsMissing() {
				childBad = true
			}
			walk(c)
		}
		if bad && !childBad { // leaf error/missing node
			miss := n.IsMissing()
			switch {
			case best == nil:
			case miss && !bestMissing:
			case miss == bestMissing && n.StartByte() > best.StartByte():
			default:
				return
			}
			best, bestMissing = n, miss
		}
	}
	walk(root)

	if best == nil {
		return fmt.Errorf("syntax error")
	}
	pt := best.StartPoint()
	line, col := int(pt.Row)+1, int(pt.Column)+1
	if best.IsMissing() {
		return fmt.Errorf("%d:%d: syntax error: expected %s", line, col, best.Type(w.Lang))
	}
	near := strings.TrimSpace(w.Text(best))
	if len(near) > 30 {
		near = near[:30] + "…"
	}
	return fmt.Errorf("%d:%d: syntax error near %q", line, col, near)
}

// ── Parse ─────────────────────────────────────────────────────────────────────

// ParseWithLanguage parses src with lang, returning the root node, a Walker, and
// a syntax error if the tree has error/missing nodes.
func ParseWithLanguage(lang *gts.Language, src []byte) (*gts.Node, *Walker, error) {
	tree, err := gts.NewParser(lang).Parse(src)
	if err != nil {
		return nil, nil, fmt.Errorf("parse: %w", err)
	}
	root := tree.RootNode()
	w := NewWalker(lang, src)
	if root.HasError() {
		return root, w, w.SyntaxError(root)
	}
	return root, w, nil
}

// ParseFromBlob loads the language from a blob (grammar-free) then parses src.
func ParseFromBlob(name string, blob []byte, src []byte) (*gts.Node, *Walker, error) {
	lang, err := LanguageFromBlob(name, blob)
	if err != nil {
		return nil, nil, fmt.Errorf("load language %q: %w", name, err)
	}
	return ParseWithLanguage(lang, src)
}
