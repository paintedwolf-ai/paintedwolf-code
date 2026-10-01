package gotreesitter

import (
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
	"unsafe"
)

func newReplayAdvancePage() *[syntheticRootReplayAdvancePageFrames]syntheticRootReplayFrame {
	return new([syntheticRootReplayAdvancePageFrames]syntheticRootReplayFrame)
}

func newReplayClosePage() *[syntheticRootReplayClosePageFrames]syntheticRootReplayFrame {
	return new([syntheticRootReplayClosePageFrames]syntheticRootReplayFrame)
}

func seedReplayPageZero(s *parserScratch) {
	s.advancePages = append(s.advancePages, newReplayAdvancePage())
	s.closePages = append(s.closePages, newReplayClosePage())
}

func replayPageFrames() []syntheticRootReplayFrame {
	frames := make([]syntheticRootReplayFrame, syntheticRootReplayMaxFrontier)
	for i := range frames {
		frames[i].top = uint32(i + 1)
	}
	return frames
}

func exerciseReplayPages(b *resultRootBuild, frames []syntheticRootReplayFrame) {
	for i := 0; i < syntheticRootReplayAdvancePageFrames/syntheticRootReplayMaxFrontier+1; i++ {
		b.syntheticRootReplayStoreAdvance(
			syntheticRootReplayAdvanceKey{top: uint32(i + 1), lookahead: Symbol(1)},
			frames,
		)
		b.syntheticRootReplayStoreClose(
			syntheticRootReplayCloseKey{top: uint32(i + 1), lookahead: Symbol(1)},
			frames,
		)
	}
}

func TestParserScratchReplayPagesResetDeterministically(t *testing.T) {
	s := &parserScratch{
		advancePages:    make([]*[syntheticRootReplayAdvancePageFrames]syntheticRootReplayFrame, 2, 4),
		closePages:      make([]*[syntheticRootReplayClosePageFrames]syntheticRootReplayFrame, 2, 4),
		advancePageUsed: 7,
		closePageUsed:   9,
		advanceFrames:   7,
		closeFrames:     9,
	}
	page0Advance := newReplayAdvancePage()
	page0Close := newReplayClosePage()
	page1Advance := newReplayAdvancePage()
	page1Close := newReplayClosePage()
	s.advancePages[0] = page0Advance
	s.advancePages[1] = page1Advance
	s.closePages[0] = page0Close
	s.closePages[1] = page1Close
	advanceBacking := s.advancePages[:cap(s.advancePages)]
	closeBacking := s.closePages[:cap(s.closePages)]

	s.resetReplayPages()

	if len(s.advancePages) != 1 || cap(s.advancePages) != 1 ||
		len(s.closePages) != 1 || cap(s.closePages) != 1 {
		t.Fatalf("retained page shape: advance %d/%d close %d/%d", len(s.advancePages), cap(s.advancePages), len(s.closePages), cap(s.closePages))
	}
	if s.advancePages[0] != page0Advance || s.closePages[0] != page0Close {
		t.Fatal("page zero was not retained")
	}
	if s.advancePageUsed != 0 || s.closePageUsed != 0 || s.advanceFrames != 0 || s.closeFrames != 0 {
		t.Fatal("replay page counters were not reset")
	}
	if advanceBacking[1] != nil || advanceBacking[2] != nil || advanceBacking[3] != nil ||
		closeBacking[1] != nil || closeBacking[2] != nil || closeBacking[3] != nil {
		t.Fatal("hidden replay page pointers were retained")
	}
	if page1Advance == nil || page1Close == nil {
		t.Fatal("test did not seed excess pages")
	}
}

func TestParserScratchReplayPageFrameShape(t *testing.T) {
	frameType := reflect.TypeOf(syntheticRootReplayFrame{})
	for i := 0; i < frameType.NumField(); i++ {
		switch frameType.Field(i).Type.Kind() {
		case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice, reflect.UnsafePointer:
			t.Fatalf("replay frame field %q contains pointer data", frameType.Field(i).Name)
		}
	}
	if got := unsafe.Sizeof(syntheticRootReplayFrame{}); got != unsafe.Sizeof(uint32(0)) {
		t.Fatalf("frame size %d", got)
	}
	if unsafe.Sizeof([syntheticRootReplayAdvancePageFrames]syntheticRootReplayFrame{}) != syntheticRootReplayAdvancePageBytes {
		t.Fatal("advance page accounting drift")
	}
	if unsafe.Sizeof([syntheticRootReplayClosePageFrames]syntheticRootReplayFrame{}) != syntheticRootReplayClosePageBytes {
		t.Fatal("close page accounting drift")
	}
}

func TestParserScratchReplayPoolCheckoutShape(t *testing.T) {
	s := acquireParserScratch()
	seedReplayPageZero(s)
	s.advancePages = append(s.advancePages, newReplayAdvancePage())
	s.closePages = append(s.closePages, newReplayClosePage())
	s.advancePageUsed = 17
	s.closePageUsed = 19
	s.advanceFrames = 17
	s.closeFrames = 19
	releaseParserScratch(s, false)

	// sync.Pool may return a different scratch object or no retained page.
	// Check only the release contract, never pointer identity.
	reacquired := acquireParserScratch()
	if len(reacquired.advancePages) > 1 || cap(reacquired.advancePages) > 1 ||
		len(reacquired.closePages) > 1 || cap(reacquired.closePages) > 1 {
		t.Fatalf("reacquired replay page shape: advance %d/%d close %d/%d", len(reacquired.advancePages), cap(reacquired.advancePages), len(reacquired.closePages), cap(reacquired.closePages))
	}
	if reacquired.advancePageUsed != 0 || reacquired.closePageUsed != 0 ||
		reacquired.advanceFrames != 0 || reacquired.closeFrames != 0 {
		t.Fatal("reacquired scratch retained replay counters")
	}
	releaseParserScratch(reacquired, false)
}

func TestParserScratchReplayBudgetBaselineExemptsRetainedPages(t *testing.T) {
	s := &parserScratch{}
	seedReplayPageZero(s)
	baselineBytes := s.allocatedBytes()
	pageBytes := int64(unsafe.Sizeof([syntheticRootReplayAdvancePageFrames]syntheticRootReplayFrame{}))
	s.setBudget(pageBytes)

	if got := s.budgetBaselineBytes; got != baselineBytes {
		t.Fatalf("budget baseline = %d, want seeded-page bytes %d", got, baselineBytes)
	}
	if got := s.allocatedBytes(); got != baselineBytes {
		t.Fatalf("seeded-page allocation = %d, want baseline %d", got, baselineBytes)
	}
	if s.budgetExhausted() {
		t.Fatal("retained page baseline incorrectly exhausted the budget")
	}

	s.advancePages = append(s.advancePages, newReplayAdvancePage())
	if got := s.allocatedBytes() - baselineBytes; got != pageBytes {
		t.Fatalf("new advance page growth = %d, want %d", got, pageBytes)
	}
	if !s.budgetExhausted() {
		t.Fatal("exact new-page growth did not exhaust the budget")
	}
}

func TestSyntheticRootReplayAdvanceTokenCacheHitIsAppendIsolated(t *testing.T) {
	s := &parserScratch{}
	p := newRootFrameReplayParser(newRootFrameReplayGapLanguage())
	p.budgetScratch = s
	b := newResultRootBuild(p, []byte("\n"), nil, nil, nil, nil)
	initial := b.syntheticRootReplayPush(syntheticRootReplayFrame{}, b.lang.InitialState)
	frame := b.syntheticRootReplayPush(initial, 3)
	var frameScratch [2][syntheticRootReplayMaxFrontier]syntheticRootReplayFrame
	var setScratch [2]syntheticRootReplayFrameSetScratch
	key := syntheticRootReplayAdvanceKey{top: frame.top, lookahead: Symbol(2)}

	first := b.syntheticRootReplayAdvanceToken(
		[]syntheticRootReplayFrame{frame},
		key.lookahead,
		frameScratch[0][:0],
		frameScratch[1][:0],
		&setScratch[0],
		&setScratch[1],
	)
	if len(first) == 0 {
		t.Fatal("production transition returned no frames")
	}
	span, ok := b.replayAdvanceMemo[key]
	if !ok || span.count() != len(first) {
		t.Fatalf("advance cache span = %v, want count %d", span, len(first))
	}
	pageCount := len(b.replayAdvancePages)
	frameCount := b.replayAdvanceFrames

	second := b.syntheticRootReplayAdvanceToken(
		[]syntheticRootReplayFrame{frame},
		key.lookahead,
		frameScratch[0][:0],
		frameScratch[1][:0],
		&setScratch[0],
		&setScratch[1],
	)
	if len(second) != len(first) || cap(second) != len(second) {
		t.Fatalf("cached transition shape = %d/%d, want %d/%d", len(second), cap(second), len(first), len(first))
	}
	want := append([]syntheticRootReplayFrame(nil), second...)
	appended := append(second, syntheticRootReplayFrame{top: ^uint32(0)})
	if len(appended) != len(second)+1 {
		t.Fatalf("append-isolated result length = %d, want %d", len(appended), len(second)+1)
	}
	for i, wantFrame := range want {
		if second[i] != wantFrame {
			t.Fatalf("cached result changed at %d: got %+v want %+v", i, second[i], wantFrame)
		}
	}

	third := b.syntheticRootReplayAdvanceToken(
		[]syntheticRootReplayFrame{frame},
		key.lookahead,
		frameScratch[0][:0],
		frameScratch[1][:0],
		&setScratch[0],
		&setScratch[1],
	)
	if len(third) != len(want) || cap(third) != len(third) {
		t.Fatalf("later cached transition shape = %d/%d, want %d/%d", len(third), cap(third), len(want), len(want))
	}
	for i, wantFrame := range want {
		if third[i] != wantFrame {
			t.Fatalf("later cached result changed at %d: got %+v want %+v", i, third[i], wantFrame)
		}
	}
	if len(b.replayAdvancePages) != pageCount || b.replayAdvanceFrames != frameCount || len(b.replayAdvanceMemo) != 1 {
		t.Fatalf("cache hit changed storage: pages=%d/%d frames=%d/%d memo=%d", len(b.replayAdvancePages), pageCount, b.replayAdvanceFrames, frameCount, len(b.replayAdvanceMemo))
	}
}

func TestParserScratchReplayProductionCountersMatchBeforeRelease(t *testing.T) {
	s := acquireParserScratch()
	p := newRootFrameReplayParser(newRootFrameReplayGapLanguage())
	p.budgetScratch = s
	b := newResultRootBuild(p, []byte("source"), nil, nil, nil, nil)
	exerciseReplayPages(&b, replayPageFrames())
	if len(s.advancePages) < 2 || len(s.closePages) < 2 || s.advanceFrames == 0 || s.closeFrames == 0 {
		t.Fatalf("production replay pages were not exercised: advance=%d/%d close=%d/%d", len(s.advancePages), s.advanceFrames, len(s.closePages), s.closeFrames)
	}
	if s.advancePageUsed != b.replayAdvancePageUsed || s.advanceFrames != b.replayAdvanceFrames ||
		s.closePageUsed != b.replayStack.closePageUsed || s.closeFrames != b.replayStack.closeFrames {
		t.Fatalf("scratch and builder counters diverged: scratch=%d/%d/%d/%d builder=%d/%d/%d/%d", s.advancePageUsed, s.advanceFrames, s.closePageUsed, s.closeFrames, b.replayAdvancePageUsed, b.replayAdvanceFrames, b.replayStack.closePageUsed, b.replayStack.closeFrames)
	}
	// Drop the builder value before release. Do not inspect either object after
	// releaseParserScratch; the pool contract is checked by the next test.
	b = resultRootBuild{}
	releaseParserScratch(s, false)
}

func TestParserScratchReplayBuilderStateDoesNotEnterPagePool(t *testing.T) {
	s := acquireParserScratch()
	p := newRootFrameReplayParser(newRootFrameReplayGapLanguage())
	p.budgetScratch = s
	source := []byte("source")
	node := &Node{}
	links := []*Node{node}
	b := &resultRootBuild{}
	*b = newResultRootBuild(p, source, nil, nil, nil, &links)
	b.replayStack.nodes = append(b.replayStack.nodes, syntheticRootReplayStackNode{state: 3})
	b.replayStack.intern = map[syntheticRootReplayStackKey]uint32{{state: 3}: 1}
	b.replayStack.closeMemo = map[syntheticRootReplayCloseKey]syntheticRootReplayCloseSpan{{top: 1}: 0}
	b.replayAdvanceMemo = map[syntheticRootReplayAdvanceKey]syntheticRootReplayAdvanceSpan{{top: 1}: 0}
	b.replayGapLexMemo = map[syntheticRootReplayGapLexKey]syntheticRootReplayGapToken{{state: 3}: {symbol: 1}}
	exerciseReplayPages(b, replayPageFrames())
	if len(b.replayStack.nodes) == 0 || len(b.replayStack.intern) == 0 || len(b.replayStack.closeMemo) == 0 ||
		len(b.replayAdvanceMemo) == 0 || len(b.replayGapLexMemo) == 0 || len(b.source) == 0 || len(links) != 1 || links[0] == nil {
		t.Fatal("builder-owned replay state was not populated")
	}
	s.nodeLinks = append(s.nodeLinks, node)
	if len(s.advancePages) < 2 || len(s.closePages) < 2 {
		t.Fatal("page pool did not receive production replay pages")
	}

	// A finalizer on the builder proves that the page pool does not retain the
	// builder, its maps, its source slice, or its link slice after release.
	builderFinalized := make(chan struct{})
	nodeFinalized := make(chan struct{})
	runtime.SetFinalizer(node, func(*Node) { close(nodeFinalized) })
	runtime.SetFinalizer(b, func(*resultRootBuild) { close(builderFinalized) })
	runtime.KeepAlive(node)
	b = nil
	links = nil
	node = nil
	source = nil
	releaseParserScratch(s, false)
	builderDone, nodeDone := false, false
	for i := 0; i < 20; i++ {
		runtime.GC()
		select {
		case <-builderFinalized:
			builderDone = true
		case <-nodeFinalized:
			nodeDone = true
		case <-time.After(10 * time.Millisecond):
		}
		if builderDone && nodeDone {
			return
		}
	}
	t.Fatalf("page pool retained builder-owned replay state: builderFinalized=%t nodeFinalized=%t", builderDone, nodeDone)
}

func TestParserScratchReplayTwoGrammarReuse(t *testing.T) {
	s := &parserScratch{}
	seedReplayPageZero(s)
	firstParser := newRootFrameReplayParser(newRootFrameReplayGapLanguage())
	firstParser.budgetScratch = s
	firstBuilder := newResultRootBuild(firstParser, []byte("\n"), nil, nil, nil, nil)
	exerciseReplayPages(&firstBuilder, replayPageFrames())
	page0Advance := s.advancePages[0]
	page0Close := s.closePages[0]
	if len(s.advancePages) < 2 || len(s.closePages) < 2 {
		t.Fatal("first grammar did not exercise replay page growth")
	}

	s.resetReplayPages()
	secondParser := newRootFrameReplayParser(newRootFrameReplayReduceLanguage())
	secondParser.budgetScratch = s
	secondBuilder := newResultRootBuild(secondParser, nil, nil, nil, nil, nil)
	exerciseReplayPages(&secondBuilder, replayPageFrames())
	if s.advancePages[0] != page0Advance || s.closePages[0] != page0Close {
		t.Fatal("second grammar did not reuse retained page zero")
	}
	if len(s.advancePages) < 2 || len(s.closePages) < 2 || s.advanceFrames == 0 || s.closeFrames == 0 {
		t.Fatal("second grammar did not exercise replay page growth")
	}
}

func TestParserScratchReplayConcurrentProductionPages(t *testing.T) {
	const workers = 4
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for worker := 0; worker < workers; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			lang := newRootFrameReplayGapLanguage()
			if worker&1 != 0 {
				lang = newRootFrameReplayReduceLanguage()
			}
			s := &parserScratch{}
			p := newRootFrameReplayParser(lang)
			p.budgetScratch = s
			b := newResultRootBuild(p, []byte("source"), nil, nil, nil, nil)
			exerciseReplayPages(&b, replayPageFrames())
			if len(s.advancePages) < 2 || len(s.closePages) < 2 || s.advanceFrames == 0 || s.closeFrames == 0 {
				errs <- "concurrent worker did not exercise both replay page stores"
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

type nestedBudgetScratchTokenSource struct {
	parser          *Parser
	delegate        TokenSource
	called          bool
	sawOuterScratch bool
	restoredScratch bool
}

func (s *nestedBudgetScratchTokenSource) Next() Token {
	if !s.called {
		s.called = true
		outerReduce := s.parser.reduceScratch
		outerMerge := s.parser.mergeScratch
		outerBudget := s.parser.budgetScratch
		outerGoCompatFrames := s.parser.goCompatFrames
		s.sawOuterScratch = outerReduce != nil && outerMerge != nil && outerBudget != nil && outerGoCompatFrames != nil
		tree, err := s.parser.Parse([]byte("1+2"))
		if err != nil {
			panic(err)
		}
		if tree != nil {
			tree.Release()
		}
		s.restoredScratch = s.parser.reduceScratch == outerReduce &&
			s.parser.mergeScratch == outerMerge &&
			s.parser.budgetScratch == outerBudget &&
			s.parser.goCompatFrames == outerGoCompatFrames
	}
	return s.delegate.Next()
}

func TestParserScratchReplayNestedBudgetScratchRestoresProductionScope(t *testing.T) {
	lang := buildArithmeticLanguage()
	p := NewParser(lang)
	sentinel := &parserScratch{}
	p.budgetScratch = sentinel
	source := []byte("1+2")
	delegate := newDFATokenSourceDirect(
		NewLexer(lang.LexStates, source),
		lang,
		p.lookupActionIndex,
		p.hasKeywordState,
		p.externalValidByState,
		p.externalValidMaskByState,
	)
	ts := &nestedBudgetScratchTokenSource{parser: p, delegate: delegate}
	tree := p.parseInternal(source, ts, nil, nil, arenaClassFull, nil, 0, 0, 0, false)
	if tree == nil || tree.RootNode() == nil {
		t.Fatal("nested-scope parse returned no tree")
	}
	tree.Release()
	if !ts.sawOuterScratch {
		t.Fatal("outer parse did not install all parser scratch pointers")
	}
	if !ts.restoredScratch {
		t.Fatal("nested parse did not restore the active outer parser scratch")
	}
	if p.budgetScratch != sentinel {
		t.Fatal("nested parse did not restore the outer budget scratch")
	}
	p.budgetScratch = nil
}
