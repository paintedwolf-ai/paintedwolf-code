package gotreesitter

// dispatchParse is a best-effort parse helper for highlighter/tagger flows.
// On parser error it intentionally returns an empty tree so editor features
// degrade gracefully instead of failing hard.
func dispatchParse(p *Parser, source []byte, oldTree *Tree, tsFactory func([]byte) TokenSource, lang *Language) *Tree {
	var tree *Tree
	var err error
	if tsFactory != nil {
		ts := tsFactory(source)
		if oldTree != nil {
			tree, err = p.ParseIncrementalWithTokenSource(source, oldTree, ts)
		} else {
			tree, err = p.ParseWithTokenSource(source, ts)
		}
	} else if oldTree != nil {
		tree, err = p.ParseIncremental(source, oldTree)
	} else {
		tree, err = p.Parse(source)
	}
	if err != nil {
		return NewTree(nil, source, lang)
	}
	return tree
}

// dispatchParseStrict preserves parser errors and reports early stops.
func dispatchParseStrict(p *Parser, source []byte, oldTree *Tree, tsFactory func([]byte) TokenSource) (*Tree, error) {
	if tsFactory != nil {
		ts := tsFactory(source)
		if oldTree != nil {
			return p.ParseIncrementalWithTokenSourceStrict(source, oldTree, ts)
		}
		return p.ParseWithTokenSourceStrict(source, ts)
	}
	if oldTree != nil {
		return p.ParseIncrementalStrict(source, oldTree)
	}
	return p.ParseStrict(source)
}

func dispatchParseUTF16(p *Parser, source []uint16, oldTree *Tree, tsFactory func([]byte) TokenSource, lang *Language) *Tree {
	var tree *Tree
	var err error
	if tsFactory != nil {
		factory := func(source []byte) (TokenSource, error) {
			return tsFactory(source), nil
		}
		if oldTree != nil {
			tree, err = p.ParseIncrementalUTF16WithTokenSourceFactory(source, oldTree, factory)
		} else {
			tree, err = p.ParseUTF16WithTokenSourceFactory(source, factory)
		}
	} else if oldTree != nil {
		tree, err = p.ParseIncrementalUTF16(source, oldTree)
	} else {
		tree, err = p.ParseUTF16(source)
	}
	if err != nil {
		utf8Source, sourceMap := encodeUTF16ToUTF8WithMap(source)
		tree = NewTree(nil, utf8Source, lang)
		attachUTF16Source(tree, source, sourceMap)
		return tree
	}
	return tree
}
