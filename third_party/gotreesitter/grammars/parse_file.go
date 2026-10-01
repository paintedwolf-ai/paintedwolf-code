package grammars

import (
	"fmt"
	"sync"

	"github.com/odvcencio/gotreesitter"
)

var (
	poolsMu sync.RWMutex
	pools   = map[string]*gotreesitter.ParserPool{}
)

func getOrCreatePool(name string, lang *gotreesitter.Language) *gotreesitter.ParserPool {
	poolsMu.RLock()
	pp, ok := pools[name]
	poolsMu.RUnlock()
	if ok && pp.Language() == lang {
		return pp
	}

	poolsMu.Lock()
	defer poolsMu.Unlock()
	if pp, ok = pools[name]; ok && pp.Language() == lang {
		return pp
	}
	pp = gotreesitter.NewParserPool(lang)
	pools[name] = pp
	return pp
}

// ParseFile detects the language from filename, parses source, and returns
// a BoundTree. The caller must call Release() on the returned BoundTree.
func ParseFile(filename string, source []byte) (*gotreesitter.BoundTree, error) {
	entry := DetectLanguage(filename)
	if entry == nil {
		return nil, fmt.Errorf("unsupported file type: %s", filename)
	}

	lang := entry.Language()
	parser := gotreesitter.NewParser(lang)

	var tree *gotreesitter.Tree
	var err error
	if entry.TokenSourceFactory != nil {
		ts := entry.TokenSourceFactory(source, lang)
		tree, err = parser.ParseWithTokenSource(source, ts)
	} else {
		tree, err = parser.Parse(source)
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	return gotreesitter.Bind(tree), nil
}

// ParseFilePooled is like ParseFile but reuses a per-language ParserPool
// to avoid allocating a new parser on every call. It is safe for concurrent use.
// Parser safety stops return a partial tree without an error; use
// ParseFilePooledStrict when partial output is not valid.
// The caller must call Release() on the returned BoundTree.
func ParseFilePooled(filename string, source []byte) (*gotreesitter.BoundTree, error) {
	return parseFilePooled(filename, source, false)
}

// ParseFilePooledStrict is like ParseFilePooled, but rejects a partial tree when
// the parser stops before it accepts all input. It releases the partial tree and
// returns an error that wraps gotreesitter.ErrParseStoppedEarly.
func ParseFilePooledStrict(filename string, source []byte) (*gotreesitter.BoundTree, error) {
	return parseFilePooled(filename, source, true)
}

func parseFilePooled(filename string, source []byte, strict bool) (*gotreesitter.BoundTree, error) {
	entry := DetectLanguage(filename)
	if entry == nil {
		return nil, fmt.Errorf("unsupported file type: %s", filename)
	}

	lang := entry.Language()
	pp := getOrCreatePool(entry.Name, lang)

	var tree *gotreesitter.Tree
	var err error
	if entry.TokenSourceFactory != nil {
		ts := entry.TokenSourceFactory(source, lang)
		if strict {
			tree, err = pp.ParseWithTokenSourceStrict(source, ts)
		} else {
			tree, err = pp.ParseWithTokenSource(source, ts)
		}
	} else if strict {
		tree, err = pp.ParseStrict(source)
	} else {
		tree, err = pp.Parse(source)
	}
	if err != nil {
		if tree != nil {
			tree.Release()
		}
		return nil, fmt.Errorf("parse %s: %w", filename, err)
	}

	return gotreesitter.Bind(tree), nil
}
