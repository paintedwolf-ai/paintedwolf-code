// Package taproot is the common front-end harness shared by M31 DSLs that use
// the gotreesitter runtime. It provides grammar generation + caching, a CST
// Walker, and one-stop Parse helpers.
//
// The grammar-free core (Walker, blob-only language load, parse-from-blob) lives
// in the taproot/walk subpackage, which depends only on the gotreesitter runtime
// — no grammargen, no grammars registry. A DSL that embeds a pre-generated
// grammar blob should import taproot/walk so it doesn't link the ~200-grammar
// registry. The functions here add the grammargen DSL fallback (build-from-source
// when no blob is present) and re-export walk's Walker for backward compatibility.
//
// The Walker error-leaf finder is lifted from elio/parse/tree.go.
package taproot

import (
	"fmt"
	"sync"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammargen"
	"github.com/odvcencio/gotreesitter/taproot/walk"
)

// Walker is the CST cursor helper. It is re-exported from taproot/walk so
// existing taproot.Walker / *taproot.Walker references keep working; new code
// that only needs the grammar-free path should import taproot/walk directly.
type Walker = walk.Walker

// NewWalker constructs a Walker for the given language and source.
func NewWalker(lang *gts.Language, src []byte) *walk.Walker {
	return walk.NewWalker(lang, src)
}

// ── grammargen DSL fallback cache ─────────────────────────────────────────────

type langEntry struct {
	lang *gts.Language
	err  error
}

var (
	genMu    sync.Mutex
	genCache = map[string]langEntry{}
)

// Language generates and caches (once per name) the tree-sitter Language for a
// grammar. build is called only on a cache miss.
func Language(name string, build func() *grammargen.Grammar) (*gts.Language, error) {
	return language(name, nil, build)
}

// LanguageFromBlob loads and caches (once per name) the tree-sitter Language
// from blob when possible, falling back to build when blob is empty or corrupt.
// The blob path is grammar-free (delegated to taproot/walk); only the fallback
// pulls grammargen.
func LanguageFromBlob(name string, blob []byte, build func() *grammargen.Grammar) (*gts.Language, error) {
	return language(name, blob, build)
}

func language(name string, blob []byte, build func() *grammargen.Grammar) (*gts.Language, error) {
	if len(blob) > 0 {
		if lang, err := walk.LanguageFromBlob(name, blob); err == nil && lang != nil {
			return lang, nil
		}
	}

	genMu.Lock()
	defer genMu.Unlock()

	if e, ok := genCache[name]; ok {
		return e.lang, e.err
	}

	var lang *gts.Language
	var err error
	if build == nil {
		err = fmt.Errorf("no grammar blob or build function for %q", name)
	} else {
		lang, _, err = grammargen.GenerateLanguageAndBlob(build())
	}
	genCache[name] = langEntry{lang: lang, err: err}
	return lang, err
}

// ── Parse ─────────────────────────────────────────────────────────────────────

// Parse runs the full common DSL parse flow: obtain (or generate+cache) the
// Language for name, parse src, and return (root, walker, syntaxErr).
func Parse(name string, build func() *grammargen.Grammar, src []byte) (*gts.Node, *walk.Walker, error) {
	lang, err := Language(name, build)
	if err != nil {
		return nil, nil, fmt.Errorf("generate language %q: %w", name, err)
	}
	return walk.ParseWithLanguage(lang, src)
}

// ParseFromBlob is the blob-aware variant of Parse. It obtains the language via
// LanguageFromBlob, then follows the same parse/error flow as Parse.
func ParseFromBlob(name string, blob []byte, build func() *grammargen.Grammar, src []byte) (*gts.Node, *walk.Walker, error) {
	lang, err := LanguageFromBlob(name, blob, build)
	if err != nil {
		return nil, nil, fmt.Errorf("load language %q: %w", name, err)
	}
	return walk.ParseWithLanguage(lang, src)
}
