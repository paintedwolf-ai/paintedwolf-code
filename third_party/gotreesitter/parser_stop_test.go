package gotreesitter

import (
	"sync/atomic"
	"testing"
	"time"
)

type goDotTokenSource struct {
	cancelOnEOF *uint32
	eofDelay    time.Duration
	idx         int
}

func (s *goDotTokenSource) Next() Token {
	switch s.idx {
	case 0:
		s.idx++
		return Token{Symbol: 1, StartByte: 0, EndByte: 1}
	default:
		if s.eofDelay > 0 {
			time.Sleep(s.eofDelay)
		}
		if s.cancelOnEOF != nil {
			atomic.StoreUint32(s.cancelOnEOF, 1)
		}
		s.idx++
		return Token{Symbol: 0, StartByte: 1, EndByte: 1}
	}
}

func buildGoDotLeafLanguage() *Language {
	return &Language{
		Name:               "go",
		SymbolCount:        3,
		TokenCount:         2,
		ExternalTokenCount: 0,
		StateCount:         2,
		LargeStateCount:    0,
		FieldCount:         0,
		ProductionIDCount:  1,
		SymbolNames:        []string{"EOF", "dot", "."},
		SymbolMetadata: []SymbolMetadata{
			{Name: "EOF", Visible: false, Named: false},
			{Name: "dot", Visible: true, Named: true},
			{Name: ".", Visible: true, Named: false},
		},
		FieldNames: []string{""},
		ParseActions: []ParseActionEntry{
			{Actions: nil},
			{Actions: []ParseAction{{Type: ParseActionShift, State: 1}}},
			{Actions: []ParseAction{{Type: ParseActionAccept}}},
		},
		ParseTable: [][]uint16{
			{0, 1, 0},
			{2, 0, 0},
		},
		LexModes: []LexMode{
			{LexState: 0},
			{LexState: 0},
		},
	}
}

func TestParserCancellationDuringFinalizationSkipsGoCompatibility(t *testing.T) {
	lang := buildGoDotLeafLanguage()
	parser := NewParser(lang)
	var cancelled uint32
	parser.SetCancellationFlag(&cancelled)

	tree, err := parser.ParseWithTokenSource([]byte("."), &goDotTokenSource{cancelOnEOF: &cancelled})
	if err != nil {
		t.Fatalf("ParseWithTokenSource() error = %v", err)
	}
	defer tree.Release()
	if got, want := tree.ParseStopReason(), ParseStopCancelled; got != want {
		t.Fatalf("ParseStopReason() = %q, want %q", got, want)
	}
	if !tree.ParseStoppedEarly() {
		t.Fatal("ParseStoppedEarly() = false, want true")
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("RootNode() = nil")
	}
	if got := root.ChildCount(); got != 0 {
		t.Fatalf("root.ChildCount() = %d, want 0; Go compatibility dot normalization should not run after cancellation", got)
	}
}

func TestParserTimeoutDuringFinalizationSkipsGoCompatibility(t *testing.T) {
	lang := buildGoDotLeafLanguage()
	parser := NewParser(lang)
	parser.SetTimeoutMicros(100)

	tree, err := parser.ParseWithTokenSource([]byte("."), &goDotTokenSource{eofDelay: 2 * time.Millisecond})
	if err != nil {
		t.Fatalf("ParseWithTokenSource() error = %v", err)
	}
	defer tree.Release()
	if got, want := tree.ParseStopReason(), ParseStopTimeout; got != want {
		t.Fatalf("ParseStopReason() = %q, want %q", got, want)
	}
	if !tree.ParseStoppedEarly() {
		t.Fatal("ParseStoppedEarly() = false, want true")
	}
	root := tree.RootNode()
	if root == nil {
		t.Fatal("RootNode() = nil")
	}
	if got := root.ChildCount(); got != 0 {
		t.Fatalf("root.ChildCount() = %d, want 0; Go compatibility dot normalization should not run after timeout", got)
	}
}

func TestParseWithTokenSourceFactoryTimeoutIncludesFactoryWork(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	parser.SetTimeoutMicros(100)

	tree, err := parser.ParseWithTokenSourceFactory([]byte("1"), func([]byte) (TokenSource, error) {
		time.Sleep(2 * time.Millisecond)
		return &slowArithmeticTokenSource{
			tokens: []Token{
				{Symbol: 1, StartByte: 0, EndByte: 1},
				{Symbol: 0, StartByte: 1, EndByte: 1},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("ParseWithTokenSourceFactory() error = %v", err)
	}
	defer tree.Release()
	if got, want := tree.ParseStopReason(), ParseStopTimeout; got != want {
		t.Fatalf("ParseStopReason() = %q, want %q", got, want)
	}
	if !tree.ParseStoppedEarly() {
		t.Fatal("ParseStoppedEarly() = false, want true")
	}
}

func TestParseIncrementalWithTokenSourceFactoryTimeoutIncludesFactoryWork(t *testing.T) {
	lang := buildArithmeticLanguage()
	parser := NewParser(lang)
	oldTree := mustParse(t, parser, []byte("1"))
	defer oldTree.Release()

	parser.SetTimeoutMicros(100)
	tree, err := parser.ParseIncrementalWithTokenSourceFactory([]byte("2"), oldTree, func([]byte) (TokenSource, error) {
		time.Sleep(2 * time.Millisecond)
		return &slowArithmeticTokenSource{
			tokens: []Token{
				{Symbol: 1, StartByte: 0, EndByte: 1},
				{Symbol: 0, StartByte: 1, EndByte: 1},
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("ParseIncrementalWithTokenSourceFactory() error = %v", err)
	}
	defer tree.Release()
	if got, want := tree.ParseStopReason(), ParseStopTimeout; got != want {
		t.Fatalf("ParseStopReason() = %q, want %q", got, want)
	}
	if !tree.ParseStoppedEarly() {
		t.Fatal("ParseStoppedEarly() = false, want true")
	}
}

func TestNormalizeGoCompatibilityStopsWhenCancelled(t *testing.T) {
	lang := buildGoDotLeafLanguage()
	arena := newNodeArena(arenaClassFull)
	children := make([]*Node, 256)
	for i := range children {
		children[i] = newLeafNodeInArena(arena, 1, true, 0, 1, Point{}, Point{Column: 1})
	}
	root := newParentNodeInArena(arena, 1, true, children, nil, 0)
	var cancelled uint32 = 1
	parser := &Parser{language: lang, cancellationFlag: &cancelled}

	normalizeGoCompatibilityWithParser(root, []byte("."), lang, parser)

	for i, child := range children {
		if got := child.ChildCount(); got != 0 {
			t.Fatalf("child %d ChildCount() = %d, want 0 after cancellation", i, got)
		}
	}
}

func TestReturnedTreeNormalizationMarksAcceptedTreeStoppedOnTimeout(t *testing.T) {
	lang := buildGoDotLeafLanguage()
	arena := newNodeArena(arenaClassFull)
	root := newLeafNodeInArena(arena, 1, true, 0, 1, Point{}, Point{Column: 1})
	tree := newTreeWithArenas(root, []byte("."), lang, arena, nil)
	tree.setParseRuntime(ParseRuntime{StopReason: ParseStopAccepted})
	defer tree.Release()

	parser := NewParser(lang)
	parser.SetTimeoutMicros(100)
	operationBudget := parser.beginParseOperationBudget()
	defer parser.endParseOperationBudget(operationBudget)
	time.Sleep(2 * time.Millisecond)
	parser.normalizeReturnedTreeForParse(tree, tree.Source())

	if got, want := tree.ParseStopReason(), ParseStopTimeout; got != want {
		t.Fatalf("ParseStopReason() = %q, want %q", got, want)
	}
	if !tree.ParseStoppedEarly() {
		t.Fatal("ParseStoppedEarly() = false, want true")
	}
	if got := root.ChildCount(); got != 0 {
		t.Fatalf("root.ChildCount() = %d, want 0 after returned-tree timeout", got)
	}
}
