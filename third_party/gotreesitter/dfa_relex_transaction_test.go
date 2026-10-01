package gotreesitter

import "testing"

func TestDFARelexFailureRestoresThroughOuterTransaction(t *testing.T) {
	lang := &Language{
		ExternalScanner: byteStateExternalScanner{},
		LexStates: []LexState{
			{
				Default: -1,
				EOF:     -1,
				Transitions: []LexTransition{
					{Lo: ' ', Hi: ' ', NextState: 0, Skip: true},
					{Lo: 'a', Hi: 'a', NextState: 1},
				},
			},
			{AcceptToken: 1, Default: -1, EOF: -1},
		},
		LexModes: []LexMode{{LexState: 0}},
	}
	payload := lang.ExternalScanner.Create()
	*payload.(*byte) = 9
	ts := &dfaTokenSource{
		lexer:              NewLexer(lang.LexStates, []byte(" a")),
		language:           lang,
		externalPayload:    payload,
		hasExternalScanner: true,
		extZeroPos:         5,
		extZeroState:       7,
		extZeroTried:       []bool{true, false},
		zeroWidthPos:       9,
		zeroWidthCount:     3,
	}
	ts.lexer.pos = 2
	ts.lexer.row = 4
	ts.lexer.col = 6
	tok := Token{
		Symbol:     1,
		StartByte:  0,
		EndByte:    1,
		StartPoint: Point{},
		EndPoint:   Point{Column: 1},
	}

	scratch := &parserScratch{
		relexSnapshotBuffer: make([]byte, externalScannerSerializationBufferSize),
	}
	for i := range scratch.relexSnapshotBuffer {
		scratch.relexSnapshotBuffer[i] = 0xff
	}
	snapshot, retained := scratch.snapshotDFARelexState(ts)
	if !retained || !scratch.relexSnapshotInUse {
		t.Fatal("outer re-lex snapshot did not retain parser scratch")
	}
	if got := cap(scratch.relexSnapshotBuffer); got != externalScannerSerializationBufferSize {
		t.Fatalf("retained buffer capacity = %d, want %d", got, externalScannerSerializationBufferSize)
	}
	for i, b := range scratch.relexSnapshotBuffer[:cap(scratch.relexSnapshotBuffer)] {
		if i == 0 {
			continue
		}
		if b != 0 {
			t.Fatalf("retained buffer byte %d = %d, want cleared before reuse", i, b)
		}
	}
	nested, nestedRetained := scratch.snapshotDFARelexState(ts)
	if nestedRetained {
		t.Fatal("nested re-lex snapshot reused the active outer buffer")
	}
	if &nested.externalPayload[0] == &snapshot.externalPayload[0] {
		t.Fatal("nested re-lex snapshot aliases the active outer buffer")
	}
	if got, ok := ts.relexFromTokenStartInTransaction(tok); ok {
		t.Fatalf("relexFromTokenStartInTransaction = (%+v, true), want false for skipped-start token", got)
	}
	if ts.lexer.pos == 2 && ts.lexer.row == 4 && ts.lexer.col == 6 {
		t.Fatal("failed probe restored itself; want the outer transaction to own rollback")
	}
	snapshot.restore(ts)
	scratch.releaseDFARelexSnapshot(retained)

	if ts.lexer.pos != 2 || ts.lexer.row != 4 || ts.lexer.col != 6 {
		t.Fatalf("lexer = pos %d row %d col %d, want restored 2/4/6", ts.lexer.pos, ts.lexer.row, ts.lexer.col)
	}
	if got := *ts.externalPayload.(*byte); got != 9 {
		t.Fatalf("external payload = %d, want restored state 9", got)
	}
	if ts.extZeroPos != 5 || ts.extZeroState != 7 || len(ts.extZeroTried) != 2 || !ts.extZeroTried[0] || ts.extZeroTried[1] {
		t.Fatalf("external zero-width guard = pos %d state %d tried %v, want 5/7/[true false]", ts.extZeroPos, ts.extZeroState, ts.extZeroTried)
	}
	if ts.zeroWidthPos != 9 || ts.zeroWidthCount != 3 {
		t.Fatalf("zero-width guard = pos %d count %d, want 9/3", ts.zeroWidthPos, ts.zeroWidthCount)
	}
	if scratch.relexSnapshotInUse {
		t.Fatal("outer re-lex snapshot remains active after rollback")
	}
	for i, b := range scratch.relexSnapshotBuffer[:cap(scratch.relexSnapshotBuffer)] {
		if b != 0 {
			t.Fatalf("released buffer byte %d = %d, want zero", i, b)
		}
	}
}

func TestParserScratchDropsOversizedDFARelexSnapshotBuffer(t *testing.T) {
	oversized := make([]byte, externalScannerSerializationBufferSize+1)
	for i := range oversized {
		oversized[i] = 0xff
	}
	scratch := &parserScratch{
		relexSnapshotBuffer: oversized,
		relexSnapshotInUse:  true,
	}
	scratch.releaseDFARelexSnapshot(true)
	if scratch.relexSnapshotBuffer != nil {
		t.Fatalf("oversized re-lex buffer capacity = %d, want dropped", cap(scratch.relexSnapshotBuffer))
	}
	if scratch.relexSnapshotInUse {
		t.Fatal("oversized re-lex buffer remains active after release")
	}
	for i, b := range oversized {
		if b != 0 {
			t.Fatalf("dropped buffer byte %d = %d, want zero", i, b)
		}
	}
}

func TestParserScratchReusesDFARelexSnapshotBufferAfterCommit(t *testing.T) {
	lang := &Language{ExternalScanner: byteStateExternalScanner{}}
	payload := lang.ExternalScanner.Create()
	*payload.(*byte) = 9
	ts := &dfaTokenSource{
		lexer:              NewLexer(nil, nil),
		language:           lang,
		externalPayload:    payload,
		hasExternalScanner: true,
	}
	scratch := &parserScratch{}

	first, firstRetained := scratch.snapshotDFARelexState(ts)
	if !firstRetained || len(first.externalPayload) != 1 {
		t.Fatalf("first snapshot = retained %t payload %v, want true/[9]", firstRetained, first.externalPayload)
	}
	firstBuffer := &first.externalPayload[0]
	*payload.(*byte) = 7
	scratch.releaseDFARelexSnapshot(firstRetained)
	if got := *payload.(*byte); got != 7 {
		t.Fatalf("committed scanner state = %d, want 7", got)
	}

	second, secondRetained := scratch.snapshotDFARelexState(ts)
	if !secondRetained || len(second.externalPayload) != 1 {
		t.Fatalf("second snapshot = retained %t payload %v, want true/[7]", secondRetained, second.externalPayload)
	}
	if &second.externalPayload[0] != firstBuffer {
		t.Fatal("completed outer transaction did not reuse its retained buffer")
	}
	*payload.(*byte) = 3
	second.restore(ts)
	scratch.releaseDFARelexSnapshot(secondRetained)
	if got := *payload.(*byte); got != 7 {
		t.Fatalf("rolled-back scanner state = %d, want 7", got)
	}
}

func TestDFATokenSourceRelexFailureRestoresProgressAndBookkeeping(t *testing.T) {
	lang := &Language{
		Name:            "python",
		SymbolNames:     []string{"end", "word"},
		ExternalScanner: byteStateExternalScanner{},
		LexStates: []LexState{
			{
				Default: -1,
				EOF:     -1,
				Transitions: []LexTransition{
					{Lo: ' ', Hi: ' ', NextState: 0, Skip: true},
					{Lo: 'a', Hi: 'z', NextState: 1},
				},
			},
			{
				AcceptToken: 1,
				Default:     -1,
				EOF:         -1,
			},
		},
		LexModes: []LexMode{{LexState: 0}},
	}
	externalPayload := lang.ExternalScanner.Create()
	*externalPayload.(*byte) = 9
	ts := &dfaTokenSource{
		lexer:                      NewLexer(lang.LexStates, []byte(" a")),
		language:                   lang,
		externalPayload:            externalPayload,
		hasExternalScanner:         true,
		usesExternalCheckpoints:    true,
		lastExternalTokenStartByte: 0,
		lastExternalTokenEndByte:   1,
		lastExternalTokenValid:     true,
		externalTokenStart:         []byte{2},
		externalTokenEnd:           []byte{9},
		extZeroPos:                 5,
		extZeroState:               7,
		extZeroTried:               []bool{true, false, true},
		zeroWidthPos:               9,
		zeroWidthCount:             3,
	}
	ts.lexer.pos = 2
	ts.lexer.row = 4
	ts.lexer.col = 6

	tok := Token{
		Symbol:     1,
		StartByte:  0,
		EndByte:    1,
		StartPoint: Point{},
		EndPoint:   Point{Column: 1},
	}
	if got, ok := ts.RelexFromTokenStart(tok); ok {
		t.Fatalf("RelexFromTokenStart = (%+v, true), want false for skipped-start token", got)
	}

	if ts.lexer.pos != 2 || ts.lexer.row != 4 || ts.lexer.col != 6 {
		t.Fatalf("lexer = pos %d row %d col %d, want 2/4/6", ts.lexer.pos, ts.lexer.row, ts.lexer.col)
	}
	if got := *ts.externalPayload.(*byte); got != 9 {
		t.Fatalf("external payload = %d, want restored post-token state 9", got)
	}
	if !ts.lastExternalTokenValid || ts.lastExternalTokenStartByte != 0 || ts.lastExternalTokenEndByte != 1 {
		t.Fatalf("last external token state = valid %t start %d end %d, want true/0/1",
			ts.lastExternalTokenValid, ts.lastExternalTokenStartByte, ts.lastExternalTokenEndByte)
	}
	if got, want := string(ts.externalTokenStart), string([]byte{2}); got != want {
		t.Fatalf("externalTokenStart = %v, want [2]", ts.externalTokenStart)
	}
	if got, want := string(ts.externalTokenEnd), string([]byte{9}); got != want {
		t.Fatalf("externalTokenEnd = %v, want [9]", ts.externalTokenEnd)
	}
	if ts.extZeroPos != 5 || ts.extZeroState != 7 {
		t.Fatalf("extZero = pos %d state %d, want 5/7", ts.extZeroPos, ts.extZeroState)
	}
	if len(ts.extZeroTried) != 3 || !ts.extZeroTried[0] || ts.extZeroTried[1] || !ts.extZeroTried[2] {
		t.Fatalf("extZeroTried = %v, want [true false true]", ts.extZeroTried)
	}
	if ts.zeroWidthPos != 9 || ts.zeroWidthCount != 3 {
		t.Fatalf("zeroWidth = pos %d count %d, want 9/3", ts.zeroWidthPos, ts.zeroWidthCount)
	}
}
