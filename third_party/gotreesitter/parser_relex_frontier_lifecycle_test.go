package gotreesitter

import "testing"

type frontierRefreshScanner struct{ calls []uint8 }

func (*frontierRefreshScanner) Create() any                    { return nil }
func (*frontierRefreshScanner) Destroy(any)                    {}
func (*frontierRefreshScanner) Serialize(any, []byte) int      { return 0 }
func (*frontierRefreshScanner) Deserialize(any, []byte)        {}
func (*frontierRefreshScanner) SupportsIncrementalReuse() bool { return true }
func (s *frontierRefreshScanner) Scan(_ any, lexer *ExternalLexer, valid []bool) bool {
	var mask uint8
	for i, ok := range valid {
		if ok {
			mask |= 1 << i
		}
	}
	s.calls = append(s.calls, mask)
	if mask&1 != 0 {
		lexer.SetResultSymbol(3)
		lexer.MarkEnd()
		return true
	}
	if mask&2 != 0 && lexer.Lookahead() == 'x' {
		lexer.Advance(false)
		lexer.MarkEnd()
		lexer.SetResultSymbol(4)
		return true
	}
	return false
}

// The next token is a sibling-only external. It proves that the parser keeps
// the successful ASI re-lex isolated for dispatch, then restores the live GLR
// frontier before the following token-source read.
func TestSingleStateRelexRefreshesFrontierBeforeNextToken(t *testing.T) {
	scanner := &frontierRefreshScanner{}
	lang := &Language{
		Name: "javascript", SymbolCount: 7, TokenCount: 5, ExternalTokenCount: 2,
		StateCount: 7, ProductionIDCount: 2,
		SymbolNames:     []string{"end", "a", "x", "_automatic_semicolon", "witness", "left", "right"},
		SymbolMetadata:  make([]SymbolMetadata, 7),
		ExternalSymbols: []Symbol{3, 4},
		ExternalScanner: scanner,
		LexStates: []LexState{
			{Default: -1, EOF: -1, Transitions: []LexTransition{{Lo: 'a', Hi: 'a', NextState: 1}, {Lo: 'x', Hi: 'x', NextState: 2}}},
			{AcceptToken: 1, Default: -1, EOF: -1},
			{AcceptToken: 2, Default: -1, EOF: -1},
		},
		LexModes: make([]LexMode, 7),
		ParseActions: []ParseActionEntry{
			{},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 1}, {Type: ParseActionShift, State: 2}}},
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: 5, ChildCount: 1, ProductionID: 0}}},
			{Actions: []ParseAction{{Type: ParseActionReduce, Symbol: 6, ChildCount: 1, ProductionID: 1}}},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 3}}},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 4}}},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 5}}},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 6}}},
			{Actions: []ParseAction{{Type: ParseActionAccept}}},
		},
		ParseTable: [][]uint16{
			{0, 1, 0, 0, 0, 4, 5},
			{0, 0, 2, 0, 0, 0, 0},
			{0, 0, 3, 0, 0, 0, 0},
			{0, 0, 0, 6, 0, 0, 0},
			{0, 0, 0, 0, 0, 0, 0},
			{0, 0, 0, 0, 7, 0, 0},
			{8, 0, 0, 0, 0, 0, 0},
		},
	}
	p := NewParser(lang)
	valid := make([][]uint16, lang.StateCount)
	valid[3], valid[4] = []uint16{0}, []uint16{1}
	ts := newDFATokenSourceDirect(NewLexer(lang.LexStates, []byte("ax")), lang, p.lookupActionIndex, nil, valid, nil)
	if ts.usesExternalCheckpoints {
		t.Fatal("stateless scanner unexpectedly enabled checkpoints")
	}
	if !ts.CanRelexFromTokenStart(Token{StartByte: 0, EndByte: 1}) {
		t.Fatal("generic relex rejected a stateless external scanner")
	}
	tree := p.parseInternal([]byte("ax"), ts, nil, nil, arenaClassFull, nil, 0, 0, 0, false)
	if tree == nil {
		t.Fatal("parse returned nil tree")
	}
	defer tree.Release()
	if got := tree.ParseStopReason(); got != ParseStopAccepted {
		t.Fatalf("stop reason = %s, want %s; scanner calls = %v", got, ParseStopAccepted, scanner.calls)
	}
	for i := 0; i+1 < len(scanner.calls); i++ {
		if scanner.calls[i] == 1 && scanner.calls[i+1] == 2 {
			return
		}
	}
	t.Fatalf("scanner calls = %v, want isolated ASI (1) then refreshed sibling witness (2)", scanner.calls)
}
