package gotreesitter

import (
	"crypto/sha256"
	"testing"
)

type c26qLegacyCheckpointPayload struct{}

type c26qLegacyCheckpointScanner struct{}

func (c26qLegacyCheckpointScanner) Create() any { return &c26qLegacyCheckpointPayload{} }
func (c26qLegacyCheckpointScanner) Destroy(any) {}
func (c26qLegacyCheckpointScanner) Serialize(payload any, buf []byte) int {
	_ = payload
	return copy(buf, []byte{1, 2, 3})
}
func (c26qLegacyCheckpointScanner) Deserialize(any, []byte) {}
func (c26qLegacyCheckpointScanner) Scan(any, *ExternalLexer, []bool) bool {
	return false
}
func (c26qLegacyCheckpointScanner) UsesExternalScannerCheckpoints() bool { return true }

func TestC26qLoadLanguageRecordsExactBlobIdentity(t *testing.T) {
	encoded, err := EncodeLanguageBlob(&Language{Name: "c26q-blob-identity"})
	if err != nil {
		t.Fatalf("EncodeLanguageBlob: %v", err)
	}
	loaded, err := LoadLanguage(encoded)
	if err != nil {
		t.Fatalf("LoadLanguage: %v", err)
	}
	want := sha256.Sum256(encoded)
	got, ok := loaded.GrammarBlobSHA256()
	if !ok || got != want {
		t.Fatalf("loaded grammar identity = (%x, %t), want (%x, true)", got, ok, want)
	}
}

func TestC26qArenaIdentityAccountingResetAndPoolReuse(t *testing.T) {
	scanner := newC26lCheckpointScanner()
	lang := &Language{ExternalScanner: scanner}
	arena := newNodeArena(arenaClassFull)
	arena.setBudget(1)
	before := arena.allocatedBytes
	if !arena.setExternalScannerCheckpointIdentityForLanguage(lang) {
		t.Fatal("arena rejected a complete identity")
	}
	wantBytes := int64(len(scanner.scannerID) + len(scanner.grammarID))
	if got := arena.allocatedBytes - before; got != wantBytes {
		t.Fatalf("identity allocation = %d, want %d", got, wantBytes)
	}
	if !arena.budgetExhausted() {
		t.Fatal("identity allocation did not count against the arena budget")
	}

	node := newLeafNodeInArena(arena, 1, true, 0, 3, Point{}, Point{Column: 3})
	node.preGotoState = 7
	if !arena.recordExternalScannerLeafCheckpoint(node, []byte{1, 2, 3}, []byte{1, 2, 3}) {
		t.Fatal("source checkpoint was not recorded")
	}
	cloneArena := newNodeArena(arenaClassIncremental)
	clone := cloneNodeInArena(cloneArena, node)
	if clone == nil || !cloneArena.externalScannerCheckpointIdentity.matches(
		ExternalScannerCheckpointIdentity{Scanner: scanner.scannerID, Grammar: scanner.grammarID},
	) {
		t.Fatal("node clone did not preserve checkpoint identity")
	}
	cloneArena.Release()

	arena.reset()
	if arena.externalScannerCheckpointIdentity.valid || arena.externalScannerCheckpointIdentity.bytesAllocated() != 0 {
		t.Fatal("arena reset retained checkpoint identity")
	}
	arena.Release()

	pooled := acquireNodeArena(arenaClassFull)
	defer pooled.Release()
	if pooled.externalScannerCheckpointIdentity.valid || pooled.externalScannerCheckpointIdentity.bytesAllocated() != 0 {
		t.Fatal("pooled arena exposed a stale checkpoint identity")
	}
}

func TestC26qArenaIdentityAccountingConflictDelta(t *testing.T) {
	first := newC26lCheckpointScanner()
	second := newC26lCheckpointScanner()
	second.scannerID = []byte("scanner-drift")
	firstLanguage := &Language{ExternalScanner: first}
	secondLanguage := &Language{ExternalScanner: second}
	arena := newNodeArena(arenaClassIncremental)
	defer arena.Release()
	baseline := arena.allocatedBytes
	if !arena.setExternalScannerCheckpointIdentityForLanguage(firstLanguage) {
		t.Fatal("first identity was rejected")
	}
	wantBytes := int64(len(first.scannerID) + len(first.grammarID))
	if got := arena.allocatedBytes - baseline; got != wantBytes {
		t.Fatalf("first identity allocation = %d, want %d", got, wantBytes)
	}
	if arena.setExternalScannerCheckpointIdentityForLanguage(secondLanguage) {
		t.Fatal("conflicting identity was accepted")
	}
	if got := arena.allocatedBytes - baseline; got != 0 {
		t.Fatalf("conflicting identity allocation = %d, want 0", got)
	}
	if !arena.externalScannerCheckpointIdentity.conflict {
		t.Fatal("conflicting identity did not mark the arena")
	}
}

func TestC26qArenaIdentityAccountingInheritedConflictDelta(t *testing.T) {
	first := newC26lCheckpointScanner()
	second := newC26lCheckpointScanner()
	second.scannerID = []byte("scanner-inherited-drift")
	firstLanguage := &Language{ExternalScanner: first}
	secondLanguage := &Language{ExternalScanner: second}

	source := newNodeArena(arenaClassFull)
	defer source.Release()
	if !source.setExternalScannerCheckpointIdentityForLanguage(firstLanguage) {
		t.Fatal("source identity was rejected")
	}

	inherited := newNodeArena(arenaClassIncremental)
	defer inherited.Release()
	baseline := inherited.allocatedBytes
	if !inherited.inheritExternalScannerCheckpointIdentity(source) {
		t.Fatal("matching inherited identity was rejected")
	}
	wantBytes := int64(len(first.scannerID) + len(first.grammarID))
	if got := inherited.allocatedBytes - baseline; got != wantBytes {
		t.Fatalf("inherited identity allocation = %d, want %d", got, wantBytes)
	}

	driftSource := newNodeArena(arenaClassFull)
	defer driftSource.Release()
	if !driftSource.setExternalScannerCheckpointIdentityForLanguage(secondLanguage) {
		t.Fatal("drift source identity was rejected")
	}
	if inherited.inheritExternalScannerCheckpointIdentity(driftSource) {
		t.Fatal("inherited identity drift was accepted")
	}
	if got := inherited.allocatedBytes - baseline; got != 0 {
		t.Fatalf("inherited conflict allocation = %d, want 0", got)
	}
	if !inherited.externalScannerCheckpointIdentity.conflict {
		t.Fatal("inherited identity drift did not mark the arena")
	}
}

func TestC26qTreeCopyPreservesArenaCheckpointIdentity(t *testing.T) {
	scanner := newC26lCheckpointScanner()
	lang := &Language{ExternalScanner: scanner}
	arena := newNodeArena(arenaClassIncremental)
	if !arena.setExternalScannerCheckpointIdentityForLanguage(lang) {
		t.Fatal("arena rejected a complete identity")
	}
	root := newLeafNodeInArena(arena, 1, true, 0, 1, Point{}, Point{Column: 1})
	tree := newTreeWithArenas(root, []byte("x"), lang, arena, nil)
	copyTree := tree.Copy()
	if copyTree == nil || copyTree.arena == nil || !copyTree.arena.externalScannerCheckpointIdentity.matches(
		ExternalScannerCheckpointIdentity{Scanner: scanner.scannerID, Grammar: scanner.grammarID},
	) {
		t.Fatal("Tree.Copy did not preserve checkpoint identity")
	}
	copyTree.Release()
	tree.Release()
}

func TestC26qReuseRejectsMissingAndMismatchedIdentity(t *testing.T) {
	oldScanner := newC26lCheckpointScanner()
	oldLanguage := &Language{ExternalScanner: oldScanner}
	oldArena := newNodeArena(arenaClassFull)
	defer oldArena.Release()
	if !oldArena.setExternalScannerCheckpointIdentityForLanguage(oldLanguage) {
		t.Fatal("old arena rejected a complete identity")
	}
	node := newLeafNodeInArena(oldArena, 1, true, 0, 3, Point{}, Point{Column: 3})
	node.preGotoState = 9
	if !oldArena.recordExternalScannerLeafCheckpoint(node, []byte{1, 2, 3}, []byte{1, 2, 3}) {
		t.Fatal("old checkpoint was not recorded")
	}

	matching := &dfaTokenSource{
		language:        oldLanguage,
		externalPayload: oldScanner.Create(),
	}
	if _, ok := canReuseNodeWithExternalScannerCheckpoint(matching, 9, node); !ok {
		t.Fatal("matching scanner and grammar identity was rejected")
	}

	drifted := newC26lCheckpointScanner()
	drifted.grammarID = []byte("grammar-drift")
	driftedLanguage := &Language{ExternalScanner: drifted}
	driftedSource := &dfaTokenSource{
		language:        driftedLanguage,
		externalPayload: drifted.Create(),
	}
	if _, ok := canReuseNodeWithExternalScannerCheckpoint(driftedSource, 9, node); ok {
		t.Fatal("grammar identity drift was admitted with equal raw bytes")
	}

	scannerDrift := newC26lCheckpointScanner()
	scannerDrift.scannerID = []byte("scanner-drift")
	scannerDriftLanguage := &Language{ExternalScanner: scannerDrift}
	scannerDriftSource := &dfaTokenSource{
		language:        scannerDriftLanguage,
		externalPayload: scannerDrift.Create(),
	}
	if _, ok := canReuseNodeWithExternalScannerCheckpoint(scannerDriftSource, 9, node); ok {
		t.Fatal("scanner identity drift was admitted with equal raw bytes")
	}

	missingArena := newNodeArena(arenaClassFull)
	defer missingArena.Release()
	missingNode := newLeafNodeInArena(missingArena, 1, true, 0, 3, Point{}, Point{Column: 3})
	missingNode.preGotoState = 9
	if !missingArena.recordExternalScannerLeafCheckpoint(missingNode, []byte{1, 2, 3}, []byte{1, 2, 3}) {
		t.Fatal("missing-identity checkpoint was not recorded")
	}
	missingSource := &dfaTokenSource{
		language:        oldLanguage,
		externalPayload: oldScanner.Create(),
	}
	if _, ok := canReuseNodeWithExternalScannerCheckpoint(missingSource, 9, missingNode); ok {
		t.Fatal("identity-bearing scanner reused an old arena without identity")
	}

	legacySource := &dfaTokenSource{
		language:        &Language{ExternalScanner: c26qLegacyCheckpointScanner{}},
		externalPayload: (&c26qLegacyCheckpointScanner{}).Create(),
	}
	if _, ok := canReuseNodeWithExternalScannerCheckpoint(legacySource, 9, node); !ok {
		t.Fatal("legacy scanner lost raw checkpoint reuse behavior")
	}
}
