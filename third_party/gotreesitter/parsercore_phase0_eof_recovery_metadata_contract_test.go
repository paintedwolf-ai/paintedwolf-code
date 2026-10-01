//go:build !gts_no_parsercorephase0 && gts_eof_recovery_admission_contract

package gotreesitter

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	core "github.com/odvcencio/gotreesitter/internal/parsercorephase0"
)

type g4EOFRoute uint8

const (
	g4EOFRoutePublicTree g4EOFRoute = iota
	g4EOFRouteSelectedStore
	g4EOFRouteLivePublication
)

const (
	g4EOFActivationNone     = "none"
	g4EOFActivationMetadata = "metadata"
	g4EOFActivationLegacy   = "legacy"
	g4EOFEventNormal        = "normal"
	g4EOFEventRecover       = "recover_eof"
)

const (
	g4EOFStateEmpty             = "empty"
	g4EOFStateProduced          = "produced"
	g4EOFStatePreApplyValidated = "pre_apply_validated"
	g4EOFStateAcceptApplied     = "accept_applied"
	g4EOFStateRecoveryDropped   = "recovery_dropped"
	g4EOFStateCompleted         = "completed"
	g4EOFStateSchedulerReturned = "scheduler_returned"
	g4EOFStateConsumed          = "consumed"
	g4EOFStateInvalid           = "invalid"
)

const (
	g4EOFFaultAfterProduce = "after_produce"
	g4EOFFaultAfterApply   = "after_apply"
	g4EOFFaultAfterDrop    = "after_drop"
	g4EOFSourceChunkBytes  = 4096
	g4EOFChildGroupSize    = 64
)

type g4EOFEvent struct {
	Ordinal           int
	Kind              string
	Cost              uint32
	DynamicPrecedence int64
}

type g4EOFWork struct {
	Polls                      uint64
	SourceChunks               uint64
	ChildGroups                uint64
	PathsVisited               uint64
	LinksVisited               uint64
	PayloadRecordsVisited      uint64
	MaxDepth                   uint64
	BytesInspected             uint64
	MaxSourceChunk             uint64
	MaxChildGroup              uint64
	CheckedArithmetic          uint64
	PublicationAttempts        uint64
	ParserConstructions        uint64
	TreeConstructions          uint64
	SelectedStoreConstructions uint64
	Overflow                   bool
}

type g4EOFReceipt struct {
	Active              bool
	Valid               bool
	State               string
	Seal                [32]byte
	TransitionSeals     [7][32]byte
	Transitions         [7]string
	DeclineReason       string
	CoreGeneration      uint64
	ElectionIndex       int
	Token               Token
	SourceLength        uint32
	SourceSHA256        [32]byte
	NormalHead          core.Head
	RecoveryHead        core.Head
	NormalCreationSeq   uint64
	RecoveryCreationSeq uint64
	NormalLineage       uint16
	RecoveryLineage     uint16
	NormalFingerprint   [32]byte
	RecoveryFingerprint [32]byte
	NormalPayloads      uint32
	RecoveryPayloads    uint32
	NormalOccurrences   uint32
	RecoveryOccurrences uint32
	NormalFrontier      uint32
	RecoveryFrontier    uint32
	Events              [2]g4EOFEvent
	SelectedEvent       int
	MetadataOnly        bool
	ConsumptionCount    uint64
	ConstructionRoute   g4EOFRoute
	ObservedErrorCost   uint32
	Work                g4EOFWork
}

type g4EOFDecision struct {
	Activation               string
	Approved                 bool
	Decline                  bool
	SelectedEvent            int
	PublishedHead            core.Head
	CarriedThroughAcceptance bool
	RunnerGateApproved       bool
	MetadataOnly             bool
	ReceiptConsumed          bool
}

type g4EOFCell struct {
	state  core.StateID
	symbol core.Symbol
}

type g4EOFTable struct {
	cells            map[g4EOFCell][]core.Action
	gotos            map[g4EOFCell]core.StateID
	reductionAliases []core.Symbol
	policy           core.SelectedStorePolicy
}

func (t *g4EOFTable) Actions(state core.StateID, symbol core.Symbol) (core.ActionRow, error) {
	return core.NewActionRow(t.cells[g4EOFCell{state: state, symbol: symbol}], false), nil
}

func (t *g4EOFTable) Goto(state core.StateID, symbol core.Symbol) (core.StateID, error) {
	return t.gotos[g4EOFCell{state: state, symbol: symbol}], nil
}

func (*g4EOFTable) ProductionFields(uint16, int) ([]core.FieldMapEntry, error) {
	return nil, nil
}

func (t *g4EOFTable) ProductionAliases(productionID uint16, childCount int) ([]core.Symbol, error) {
	if productionID != 1 || len(t.reductionAliases) != childCount {
		return nil, nil
	}
	return t.reductionAliases, nil
}

func (t *g4EOFTable) SelectedStorePolicy() (core.SelectedStorePolicy, error) {
	return t.policy, nil
}

type g4EOFFixtureSpec struct {
	source                    []byte
	recoveryRoots             int
	recoveryPaths             int
	maxDerivations            uint64
	recoveryDynamicPrecedence int16
	recoveryAlias             core.Symbol
	invisibleTop              bool
	extraTop                  bool
	errorTop                  bool
	openS3Region              bool
	metadataActivation        bool
	legacyActivation          bool
}

type g4EOFFixture struct {
	compact   *core.Core
	scheduler *diagnosticParserCoreGenericScheduler
	runner    *parserCoreFreshFullRunner
	language  *Language
	source    []byte
}

func newG4EOFFixture(t *testing.T, spec g4EOFFixtureSpec) *g4EOFFixture {
	t.Helper()
	if len(spec.source) == 0 {
		spec.source = []byte("GET /path\n")
	}
	if spec.recoveryRoots == 0 {
		spec.recoveryRoots = 1
	}
	if spec.recoveryPaths == 0 {
		spec.recoveryPaths = 1
	}
	if spec.maxDerivations == 0 {
		spec.maxDerivations = 8
	}
	table := &g4EOFTable{
		cells: make(map[g4EOFCell][]core.Action),
		gotos: make(map[g4EOFCell]core.StateID),
	}
	if spec.recoveryAlias != 0 {
		table.reductionAliases = make([]core.Symbol, spec.recoveryRoots)
		for index := range table.reductionAliases {
			table.reductionAliases[index] = spec.recoveryAlias
		}
	}
	const (
		normalSeedState    core.StateID = 1
		normalAcceptState  core.StateID = 100
		recoveryFinalState core.StateID = 200
		normalSymbol       core.Symbol  = 2
		reductionLookahead core.Symbol  = 90
		reductionSymbol    core.Symbol  = 50
	)
	table.cells[g4EOFCell{state: normalSeedState, symbol: normalSymbol}] = []core.Action{{
		Type: core.ActionShift, State: normalAcceptState,
	}}
	table.cells[g4EOFCell{state: normalAcceptState, symbol: 0}] = []core.Action{{Type: core.ActionAccept}}
	const symbolSlots = 256
	selectedSymbols := make([]core.SelectedSymbolPolicy, symbolSlots)
	selectedSymbols[normalSymbol] = core.SelectedSymbolPolicy{Visible: true, Named: true}
	for rootIndex := 0; rootIndex < spec.recoveryRoots; rootIndex++ {
		symbol := core.Symbol(10 + rootIndex)
		if int(symbol) < len(selectedSymbols) {
			selectedSymbols[symbol] = core.SelectedSymbolPolicy{Visible: !spec.invisibleTop, Named: true}
		}
	}
	selectedSymbols[reductionSymbol] = core.SelectedSymbolPolicy{Visible: !spec.invisibleTop, Named: true}
	policy, err := core.NewSelectedStorePolicy(
		selectedSymbols,
		make([]core.SelectedUnaryRule, symbolSlots*symbolSlots),
		normalSymbol,
	)
	if err != nil {
		t.Fatal(err)
	}
	table.policy = policy

	compact, err := core.New(table, core.Limits{
		MaxNodes:            1024,
		MaxLinks:            2048,
		MaxSubtrees:         1024,
		MaxChildren:         2048,
		MaxMetadata:         1024,
		MaxLinksPerBoundary: 8,
		MaxPopPaths:         256,
		MaxDerivations:      spec.maxDerivations,
	})
	if err != nil {
		t.Fatal(err)
	}
	normalSeed, err := compact.Seed(normalSeedState, 0)
	if err != nil {
		t.Fatal(err)
	}
	normalHead, err := compact.Shift(normalSeed, normalSymbol, 0, core.Token{
		Symbol: normalSymbol, StartByte: 0, EndByte: uint32(len(spec.source)),
	}, core.ForkOrder{})
	if err != nil {
		t.Fatal(err)
	}

	var recoveryHead core.Head
	for pathIndex := 0; pathIndex < spec.recoveryPaths; pathIndex++ {
		seedState := core.StateID(10 + pathIndex)
		head, seedErr := compact.Seed(seedState, 0)
		if seedErr != nil {
			t.Fatal(seedErr)
		}
		state := seedState
		for rootIndex := 0; rootIndex < spec.recoveryRoots; rootIndex++ {
			symbol := core.Symbol(10 + rootIndex)
			if spec.errorTop && rootIndex == spec.recoveryRoots-1 {
				symbol = core.RecoveryErrorSymbol
			}
			nextState := core.StateID(300 + pathIndex*32 + rootIndex)
			if rootIndex == spec.recoveryRoots-1 && spec.recoveryDynamicPrecedence == 0 {
				nextState = recoveryFinalState
			}
			extra := spec.extraTop && rootIndex == spec.recoveryRoots-1
			table.cells[g4EOFCell{state: state, symbol: symbol}] = []core.Action{{
				Type: core.ActionShift, State: nextState, Extra: extra,
			}}
			startByte := uint32(rootIndex)
			endByte := startByte + 1
			if rootIndex == spec.recoveryRoots-1 {
				endByte = uint32(len(spec.source))
			}
			head, err = compact.Shift(head, symbol, 0, core.Token{
				Symbol: symbol, StartByte: startByte, EndByte: endByte, Extra: extra,
			}, core.ForkOrder{})
			if err != nil {
				t.Fatal(err)
			}
			state = nextState
		}
		if spec.recoveryDynamicPrecedence != 0 {
			table.cells[g4EOFCell{state: state, symbol: reductionLookahead}] = []core.Action{{
				Type:              core.ActionReduce,
				Symbol:            reductionSymbol,
				ChildCount:        uint8(spec.recoveryRoots),
				DynamicPrecedence: spec.recoveryDynamicPrecedence,
				ProductionID:      1,
			}}
			table.gotos[g4EOFCell{state: seedState, symbol: reductionSymbol}] = recoveryFinalState
			outputs, reduceErr := compact.ReduceOutputs(head, reductionLookahead, 0, core.ForkOrder{})
			if reduceErr != nil || len(outputs) != 1 {
				t.Fatalf("recovery reduction outputs=%+v err=%v", outputs, reduceErr)
			}
			head = outputs[0].Head
		}
		recoveryHead = head
	}

	language := &Language{
		Name:           "g4-eof-contract",
		SymbolCount:    symbolSlots,
		TokenCount:     symbolSlots,
		StateCount:     1024,
		SymbolNames:    make([]string, symbolSlots),
		SymbolMetadata: make([]SymbolMetadata, symbolSlots),
	}
	language.SymbolMetadata[normalSymbol] = SymbolMetadata{Visible: true, Named: true}
	language.SymbolNames[normalSymbol] = "document"
	for rootIndex := 0; rootIndex < spec.recoveryRoots; rootIndex++ {
		symbol := core.Symbol(10 + rootIndex)
		if int(symbol) < len(language.SymbolMetadata) {
			language.SymbolMetadata[symbol] = SymbolMetadata{Visible: !spec.invisibleTop, Named: true}
			language.SymbolNames[symbol] = "payload"
		}
	}
	language.SymbolMetadata[reductionSymbol] = SymbolMetadata{Visible: !spec.invisibleTop, Named: true}
	language.SymbolNames[reductionSymbol] = "payload_group"
	if spec.recoveryAlias != 0 {
		language.SymbolMetadata[spec.recoveryAlias] = SymbolMetadata{Named: true}
		language.SymbolNames[spec.recoveryAlias] = "invisible_alias"
	}
	sourceLength := uint32(len(spec.source))
	scheduler := &diagnosticParserCoreGenericScheduler{
		compact:     compact,
		tokenSource: &dfaTokenSource{language: language},
		headers: []diagnosticParserCoreHeader{
			{head: normalHead, creationSeq: 11, cleanPathLineage: 21},
			{head: recoveryHead, creationSeq: 12, cleanPathLineage: 22},
		},
		token:         Token{Symbol: 0, StartByte: sourceLength, EndByte: sourceLength},
		electionIndex: 7,
		options: DiagnosticParserCorePrefixOptions{
			allowEOFAcceptNoActionSiblings: spec.legacyActivation,
			MaxDispatches:                  64,
		},
		receipt: &DiagnosticParserCoreGenericScheduler{},
	}
	setCompactEOFRecoveryAdmissionEnabledForTest(scheduler, spec.metadataActivation)
	scheduler.options.materializationSource = spec.source
	scheduler.options.materializationContextSet = true
	if spec.openS3Region {
		scheduler.headers[1].openRecoveryRegion(&diagnosticParserCoreS3Region{
			state: recoveryFinalState, startByte: 0, endByte: sourceLength,
		})
	}
	parser := NewParser(language)
	runner := &parserCoreFreshFullRunner{
		lang: language, parser: parser, compact: compact,
		options: DiagnosticParserCorePrefixOptions{ReceiptMode: DiagnosticParserCoreReceiptSummary},
	}
	return &g4EOFFixture{
		compact: compact, scheduler: scheduler, runner: runner, language: language,
		source: append([]byte(nil), spec.source...),
	}
}

type g4EOFCoreSnapshot struct {
	work       core.Work
	footprint  uint64
	index      core.BoundaryIndexStats
	stats      []core.Stats
	boundaries [][2]uint32
	paths      [][]core.Derivation
}

func snapshotG4EOFCore(t *testing.T, fixture *g4EOFFixture) g4EOFCoreSnapshot {
	t.Helper()
	snapshot := g4EOFCoreSnapshot{
		work:      fixture.compact.Work(),
		footprint: fixture.compact.FootprintBytes(),
		index:     fixture.compact.BoundaryIndexStats(),
		stats:     make([]core.Stats, len(fixture.scheduler.headers)),
		paths:     make([][]core.Derivation, len(fixture.scheduler.headers)),
	}
	for index, header := range fixture.scheduler.headers {
		stats, err := fixture.compact.Stats(header.head)
		if err != nil {
			t.Fatal(err)
		}
		state, offset, err := fixture.compact.Boundary(header.head)
		if err != nil {
			t.Fatal(err)
		}
		paths, err := fixture.compact.Derivations(header.head)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.stats[index] = stats
		snapshot.boundaries = append(snapshot.boundaries, [2]uint32{uint32(state), offset})
		snapshot.paths[index] = paths
	}
	return snapshot
}

func requireG4EOFCoreUnchanged(t *testing.T, fixture *g4EOFFixture, before g4EOFCoreSnapshot) {
	t.Helper()
	after := snapshotG4EOFCore(t, fixture)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("metadata admission mutated live Core: before=%+v after=%+v", before, after)
	}
}

func requireG4EOFValidReceipt(t *testing.T, fixture *g4EOFFixture, receipt g4EOFReceipt, wantCost uint32, wantRecoveryPayloads uint32) {
	t.Helper()
	if !receipt.Active || !receipt.Valid || receipt.State != g4EOFStateProduced ||
		receipt.Seal == ([32]byte{}) || receipt.DeclineReason != "" || !receipt.MetadataOnly {
		t.Fatalf("metadata receipt is not valid: %+v", receipt)
	}
	if receipt.CoreGeneration == 0 || receipt.CoreGeneration != compactEOFRecoveryCoreGenerationForTest(fixture.compact) {
		t.Fatalf("receipt Core generation=%d", receipt.CoreGeneration)
	}
	if receipt.ElectionIndex != fixture.scheduler.electionIndex || receipt.Token != fixture.scheduler.token ||
		receipt.SourceLength != uint32(len(fixture.source)) || receipt.SourceSHA256 != sha256.Sum256(fixture.source) {
		t.Fatalf("receipt election/token/source binding=%+v", receipt)
	}
	normal := fixture.scheduler.headers[0]
	recovery := fixture.scheduler.headers[1]
	if receipt.NormalHead != normal.head || receipt.RecoveryHead != recovery.head ||
		receipt.NormalCreationSeq != normal.creationSeq || receipt.RecoveryCreationSeq != recovery.creationSeq ||
		receipt.NormalLineage != normal.cleanPathLineage || receipt.RecoveryLineage != recovery.cleanPathLineage {
		t.Fatalf("receipt head binding=%+v", receipt)
	}
	if receipt.NormalFingerprint == ([32]byte{}) || receipt.RecoveryFingerprint == ([32]byte{}) ||
		receipt.NormalFingerprint == receipt.RecoveryFingerprint {
		t.Fatalf("receipt payload fingerprints=%x/%x", receipt.NormalFingerprint, receipt.RecoveryFingerprint)
	}
	if receipt.NormalPayloads != 1 || receipt.RecoveryPayloads != wantRecoveryPayloads {
		t.Fatalf("receipt payload counts=%d/%d, want 1/%d", receipt.NormalPayloads, receipt.RecoveryPayloads, wantRecoveryPayloads)
	}
	if receipt.NormalOccurrences != 1 || receipt.RecoveryOccurrences != wantRecoveryPayloads ||
		receipt.NormalFrontier != 1 || receipt.RecoveryFrontier != wantRecoveryPayloads {
		t.Fatalf(
			"receipt occurrence/frontier counts=%d/%d and %d/%d, want 1/%d and 1/%d",
			receipt.NormalOccurrences,
			receipt.RecoveryOccurrences,
			receipt.NormalFrontier,
			receipt.RecoveryFrontier,
			wantRecoveryPayloads,
			wantRecoveryPayloads,
		)
	}
	if receipt.Events != ([2]g4EOFEvent{
		{Ordinal: 0, Kind: g4EOFEventNormal, Cost: 0},
		{Ordinal: 1, Kind: g4EOFEventRecover, Cost: wantCost},
	}) || receipt.SelectedEvent != 0 {
		t.Fatalf("receipt events=%+v selected=%d", receipt.Events, receipt.SelectedEvent)
	}
	work := receipt.Work
	wantSourceChunks := uint64((len(fixture.source) + g4EOFSourceChunkBytes - 1) / g4EOFSourceChunkBytes)
	wantChildGroups := uint64(1 + (int(wantRecoveryPayloads)+g4EOFChildGroupSize-1)/g4EOFChildGroupSize)
	if work.Polls == 0 || work.SourceChunks != wantSourceChunks || work.ChildGroups != wantChildGroups ||
		work.PathsVisited != 2 || work.LinksVisited == 0 ||
		work.PayloadRecordsVisited != uint64(1+wantRecoveryPayloads) || work.CheckedArithmetic == 0 ||
		work.MaxDepth != 1 ||
		work.BytesInspected != uint64(len(fixture.source)) || work.MaxSourceChunk > g4EOFSourceChunkBytes ||
		work.MaxChildGroup > g4EOFChildGroupSize ||
		work.PublicationAttempts != 0 || work.ParserConstructions != 0 || work.TreeConstructions != 0 ||
		work.SelectedStoreConstructions != 0 || work.Overflow {
		t.Fatalf("metadata producer work=%+v", work)
	}
}

func TestCompactEOFRecoveryMetadataProducerDerivesEventsWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name             string
		source           string
		recoveryRoots    int
		wantRecoveryCost uint32
	}{
		{name: "http", source: "GET /path\n", recoveryRoots: 1, wantRecoveryCost: 640},
		{name: "robot", source: "*** Test Cases ***\nTest\n  Log  hello\n", recoveryRoots: 4, wantRecoveryCost: 1027},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newG4EOFFixture(t, g4EOFFixtureSpec{
				source: []byte(test.source), recoveryRoots: test.recoveryRoots, metadataActivation: true,
			})
			before := snapshotG4EOFCore(t, fixture)
			receipt, err := produceCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, func() error { return nil })
			if err != nil {
				t.Fatalf("produce metadata: %v", err)
			}
			requireG4EOFValidReceipt(t, fixture, receipt, test.wantRecoveryCost, uint32(test.recoveryRoots))
			requireG4EOFCoreUnchanged(t, fixture, before)
		})
	}
}

func advanceG4EOFToSchedulerReturn(t *testing.T, fixture *g4EOFFixture) {
	t.Helper()
	stop, err := fixture.scheduler.dispatchPass()
	if err != nil || stop != nil {
		t.Fatalf("dispatch EOF acceptance: stop=%+v err=%v", stop, err)
	}
	afterDrop := compactEOFRecoveryAdmissionReceiptForTest(fixture.scheduler)
	if afterDrop.State != g4EOFStateRecoveryDropped || len(fixture.scheduler.headers) != 1 ||
		!fixture.scheduler.headers[0].accepted {
		t.Fatalf("post-drop receipt=%+v headers=%+v", afterDrop, fixture.scheduler.headers)
	}
	if err := fixture.scheduler.completeAcceptance(); err != nil {
		t.Fatalf("complete EOF acceptance: %v", err)
	}
	afterCompletion := compactEOFRecoveryAdmissionReceiptForTest(fixture.scheduler)
	if afterCompletion.State != g4EOFStateCompleted || fixture.scheduler.receipt.Acceptance == nil {
		t.Fatalf("completion receipt=%+v scheduler=%+v", afterCompletion, fixture.scheduler.receipt)
	}
	if err := markCompactEOFRecoverySchedulerReturnedForTest(fixture.scheduler); err != nil {
		t.Fatalf("mark scheduler return: %v", err)
	}
	if got := compactEOFRecoveryAdmissionReceiptForTest(fixture.scheduler); got.State != g4EOFStateSchedulerReturned {
		t.Fatalf("scheduler-return receipt=%+v", got)
	}
}

func constructG4EOFRoute(t *testing.T, fixture *g4EOFFixture, route g4EOFRoute) error {
	t.Helper()
	switch route {
	case g4EOFRoutePublicTree:
		tree, err := fixture.runner.materializeSelection(fixture.source, fixture.compact, fixture.scheduler)
		if tree != nil {
			tree.Release()
		}
		return err
	case g4EOFRouteSelectedStore:
		store, err := fixture.runner.materializeSelectedStoreSelection(
			fixture.source,
			fixture.compact,
			fixture.scheduler,
			func() error { return nil },
		)
		if store != nil {
			store.Release()
		}
		return err
	default:
		return errors.New("unsupported G4 construction route")
	}
}

func requireG4EOFCompleteStateMachine(t *testing.T, receipt g4EOFReceipt, route g4EOFRoute) {
	t.Helper()
	want := [7]string{
		g4EOFStateProduced,
		g4EOFStatePreApplyValidated,
		g4EOFStateAcceptApplied,
		g4EOFStateRecoveryDropped,
		g4EOFStateCompleted,
		g4EOFStateSchedulerReturned,
		g4EOFStateConsumed,
	}
	if receipt.State != g4EOFStateConsumed || receipt.Transitions != want || receipt.ConsumptionCount != 1 ||
		receipt.ConstructionRoute != route || !receipt.Valid {
		t.Fatalf("sealed lifecycle receipt=%+v", receipt)
	}
	seen := make(map[[32]byte]struct{}, len(receipt.TransitionSeals))
	for index, seal := range receipt.TransitionSeals {
		if seal == ([32]byte{}) {
			t.Fatalf("transition %d has an empty seal", index)
		}
		if _, duplicate := seen[seal]; duplicate {
			t.Fatalf("transition %d reused seal %x", index, seal)
		}
		seen[seal] = struct{}{}
	}
}

func TestCompactEOFRecoveryMetadataUsesRealSchedulerAndConstructionPaths(t *testing.T) {
	for _, route := range []g4EOFRoute{g4EOFRoutePublicTree, g4EOFRouteSelectedStore} {
		route := route
		t.Run(map[g4EOFRoute]string{
			g4EOFRoutePublicTree:    "parse",
			g4EOFRouteSelectedStore: "parse-selected-store",
		}[route], func(t *testing.T) {
			fixture := newG4EOFFixture(t, g4EOFFixtureSpec{metadataActivation: true})
			advanceG4EOFToSchedulerReturn(t, fixture)
			if err := constructG4EOFRoute(t, fixture, route); err != nil {
				t.Fatalf("construct route %d: %v", route, err)
			}
			receipt := compactEOFRecoveryAdmissionReceiptForTest(fixture.scheduler)
			requireG4EOFCompleteStateMachine(t, receipt, route)
			if route == g4EOFRoutePublicTree {
				if receipt.Work.TreeConstructions != 1 || receipt.Work.SelectedStoreConstructions != 0 {
					t.Fatalf("public construction work=%+v", receipt.Work)
				}
			} else if receipt.Work.TreeConstructions != 0 || receipt.Work.SelectedStoreConstructions != 1 {
				t.Fatalf("selected-store construction work=%+v", receipt.Work)
			}

			if err := constructG4EOFRoute(t, fixture, route); err == nil {
				t.Fatal("a consumed receipt permitted a second construction")
			}
			afterSecond := compactEOFRecoveryAdmissionReceiptForTest(fixture.scheduler)
			if afterSecond.Work.TreeConstructions != receipt.Work.TreeConstructions ||
				afterSecond.Work.SelectedStoreConstructions != receipt.Work.SelectedStoreConstructions ||
				afterSecond.ConsumptionCount != 1 {
				t.Fatalf("second gate changed construction counters: before=%+v after=%+v", receipt, afterSecond)
			}
		})
	}
}

func TestCompactEOFRecoveryMetadataDerivationNegatives(t *testing.T) {
	tests := []struct {
		name              string
		spec              g4EOFFixtureSpec
		mutate            func(*g4EOFFixture)
		wantActive        bool
		wantObservedError bool
	}{
		{
			name: "false-EOF", spec: g4EOFFixtureSpec{metadataActivation: true},
			mutate: func(fixture *g4EOFFixture) { fixture.scheduler.token.Symbol = 1 },
		},
		{
			name: "nonzero-width-EOF", spec: g4EOFFixtureSpec{metadataActivation: true}, wantActive: true,
			mutate: func(fixture *g4EOFFixture) { fixture.scheduler.token.StartByte-- },
		},
		{
			name: "wrong-source-length", spec: g4EOFFixtureSpec{metadataActivation: true}, wantActive: true,
			mutate: func(fixture *g4EOFFixture) {
				fixture.scheduler.token.StartByte--
				fixture.scheduler.token.EndByte--
			},
		},
		{
			name: "missing-EOF", spec: g4EOFFixtureSpec{metadataActivation: true}, wantActive: true,
			mutate: func(fixture *g4EOFFixture) { fixture.scheduler.token.Missing = true },
		},
		{
			name: "no-lookahead-EOF", spec: g4EOFFixtureSpec{metadataActivation: true}, wantActive: true,
			mutate: func(fixture *g4EOFFixture) { fixture.scheduler.token.NoLookahead = true },
		},
		{
			name: "external-scanner-EOF", spec: g4EOFFixtureSpec{metadataActivation: true}, wantActive: true,
			mutate: func(fixture *g4EOFFixture) { fixture.scheduler.token.ExternalScannerToken = true },
		},
		{
			name: "wrong-derivation-count",
			spec: g4EOFFixtureSpec{metadataActivation: true, recoveryPaths: 2, maxDerivations: 8}, wantActive: true,
		},
		{
			name: "inexact-derivation",
			spec: g4EOFFixtureSpec{metadataActivation: true, recoveryPaths: 2, maxDerivations: 1}, wantActive: true,
		},
		{
			name: "extra-top-root",
			spec: g4EOFFixtureSpec{metadataActivation: true, extraTop: true}, wantActive: true,
		},
		{
			name: "error-symbol-with-error-cost",
			spec: g4EOFFixtureSpec{metadataActivation: true, errorTop: true}, wantActive: true, wantObservedError: true,
		},
		{
			name: "open-strategy-three-region",
			spec: g4EOFFixtureSpec{metadataActivation: true, openS3Region: true}, wantActive: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newG4EOFFixture(t, test.spec)
			if test.mutate != nil {
				test.mutate(fixture)
			}
			beforeWork := fixture.compact.Work()
			beforeFootprint := fixture.compact.FootprintBytes()
			receipt, err := produceCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, func() error { return nil })
			if err != nil {
				t.Fatalf("unsupported metadata returned a hard error: %v", err)
			}
			if receipt.Active != test.wantActive || receipt.Valid || receipt.SelectedEvent != -1 {
				t.Fatalf("unsupported metadata receipt=%+v, want active=%v valid=false selected=-1", receipt, test.wantActive)
			}
			if test.wantActive && receipt.DeclineReason == "" {
				t.Fatal("active unsupported metadata has no decline reason")
			}
			if test.wantObservedError && receipt.ObservedErrorCost == 0 {
				t.Fatalf("ERROR-symbol decline has no observed error cost: %+v", receipt)
			}
			if fixture.compact.Work() != beforeWork || fixture.compact.FootprintBytes() != beforeFootprint {
				t.Fatal("unsupported metadata mutated live Core")
			}
		})
	}
}

func TestCompactEOFRecoveryMetadataInvisibleLeafContributesZero(t *testing.T) {
	fixture := newG4EOFFixture(t, g4EOFFixtureSpec{
		metadataActivation: true,
		invisibleTop:       true,
	})
	receipt, err := produceCompactEOFRecoveryAdmissionForTest(
		fixture.scheduler,
		fixture.source,
		func() error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Valid || receipt.NormalFrontier != 1 || receipt.RecoveryFrontier != 0 ||
		receipt.Events[1].Cost != 540 || receipt.RecoveryOccurrences != 1 ||
		receipt.Work.PayloadRecordsVisited != 2 {
		t.Fatalf("invisible leaf receipt=%+v", receipt)
	}
}

func TestCompactEOFRecoveryMetadataNonzeroAliasCreatesVisibleBoundary(t *testing.T) {
	const invisibleAlias core.Symbol = 60
	spec := g4EOFFixtureSpec{
		metadataActivation:        true,
		invisibleTop:              true,
		recoveryDynamicPrecedence: 1,
		recoveryAlias:             invisibleAlias,
	}
	fixture := newG4EOFFixture(t, spec)
	if fixture.language.SymbolMetadata[invisibleAlias].Visible {
		t.Fatal("alias target must stay invisible for the C boundary test")
	}
	first, err := produceCompactEOFRecoveryAdmissionForTest(
		fixture.scheduler,
		fixture.source,
		func() error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := produceCompactEOFRecoveryAdmissionForTest(
		fixture.scheduler,
		fixture.source,
		func() error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Valid || !second.Valid || first.RecoveryOccurrences != 2 ||
		first.RecoveryFrontier != 1 || first.Events[1].Cost != 640 ||
		first.RecoveryFingerprint == ([32]byte{}) ||
		first.RecoveryFingerprint != second.RecoveryFingerprint {
		t.Fatalf("aliased reduction receipts first=%+v second=%+v", first, second)
	}

	unaliased := newG4EOFFixture(t, g4EOFFixtureSpec{
		metadataActivation:        true,
		invisibleTop:              true,
		recoveryDynamicPrecedence: 1,
	})
	withoutAlias, err := produceCompactEOFRecoveryAdmissionForTest(
		unaliased.scheduler,
		unaliased.source,
		func() error { return nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !withoutAlias.Valid || withoutAlias.RecoveryOccurrences != 2 ||
		withoutAlias.RecoveryFrontier != 0 || withoutAlias.Events[1].Cost != 540 ||
		withoutAlias.RecoveryFingerprint == first.RecoveryFingerprint {
		t.Fatalf("unaliased reduction receipt=%+v", withoutAlias)
	}
}

func TestCompactEOFRecoveryMetadataUsesCRecoveryCostBeforeDynamicPrecedence(t *testing.T) {
	fixture := newG4EOFFixture(t, g4EOFFixtureSpec{
		metadataActivation: true, recoveryDynamicPrecedence: 9,
	})
	receipt, err := produceCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Valid || receipt.SelectedEvent != 0 || receipt.Events[0].Cost >= receipt.Events[1].Cost ||
		receipt.Events[1].DynamicPrecedence != 9 {
		t.Fatalf("lower normal cost did not win before recovery dynamic precedence: %+v", receipt)
	}
}

func TestCompactEOFRecoveryMetadataReceiptTamperingDeclines(t *testing.T) {
	fixture := newG4EOFFixture(t, g4EOFFixtureSpec{metadataActivation: true})
	receipt, err := produceCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, func() error { return nil })
	if err != nil || !receipt.Valid {
		t.Fatalf("produce valid receipt: receipt=%+v err=%v", receipt, err)
	}
	tests := []struct {
		name   string
		mutate func(*g4EOFFixture, *g4EOFReceipt)
		route  g4EOFRoute
	}{
		{
			name: "changed-head",
			mutate: func(fixture *g4EOFFixture, _ *g4EOFReceipt) {
				fixture.scheduler.headers[1].head = fixture.scheduler.headers[0].head
			},
		},
		{
			name: "changed-header-lineage",
			mutate: func(fixture *g4EOFFixture, _ *g4EOFReceipt) {
				fixture.scheduler.headers[1].cleanPathLineage++
			},
		},
		{
			name: "changed-token",
			mutate: func(fixture *g4EOFFixture, _ *g4EOFReceipt) {
				fixture.scheduler.token.Missing = true
			},
		},
		{
			name: "changed-source",
			mutate: func(fixture *g4EOFFixture, _ *g4EOFReceipt) {
				fixture.source[0] ^= 0xff
			},
		},
		{
			name:   "changed-payload-fingerprint",
			mutate: func(_ *g4EOFFixture, receipt *g4EOFReceipt) { receipt.RecoveryFingerprint[0] ^= 0xff },
		},
		{
			name:   "changed-core-generation",
			mutate: func(_ *g4EOFFixture, receipt *g4EOFReceipt) { receipt.CoreGeneration++ },
		},
		{
			name:   "changed-seal",
			mutate: func(_ *g4EOFFixture, receipt *g4EOFReceipt) { receipt.Seal[0] ^= 0xff },
		},
		{
			name:   "stale-election",
			mutate: func(fixture *g4EOFFixture, _ *g4EOFReceipt) { fixture.scheduler.electionIndex++ },
		},
		{
			name:   "equal-cost",
			mutate: func(_ *g4EOFFixture, receipt *g4EOFReceipt) { receipt.Events[0].Cost = receipt.Events[1].Cost },
		},
		{
			name:   "reversed-cost",
			mutate: func(_ *g4EOFFixture, receipt *g4EOFReceipt) { receipt.Events[0].Cost = receipt.Events[1].Cost + 1 },
		},
		{
			name: "reversed-event-order",
			mutate: func(_ *g4EOFFixture, receipt *g4EOFReceipt) {
				receipt.Events[0], receipt.Events[1] = receipt.Events[1], receipt.Events[0]
			},
		},
		{
			name:   "attempted-live-publication",
			mutate: func(_ *g4EOFFixture, receipt *g4EOFReceipt) { receipt.Work.PublicationAttempts = 1 },
			route:  g4EOFRouteLivePublication,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caseFixture := newG4EOFFixture(t, g4EOFFixtureSpec{metadataActivation: true})
			before := snapshotG4EOFCore(t, caseFixture)
			caseReceipt, produceErr := produceCompactEOFRecoveryAdmissionForTest(caseFixture.scheduler, caseFixture.source, func() error { return nil })
			if produceErr != nil || !caseReceipt.Valid {
				t.Fatalf("produce valid receipt: receipt=%+v err=%v", caseReceipt, produceErr)
			}
			test.mutate(caseFixture, &caseReceipt)
			decision := gateCompactEOFRecoveryAdmissionForTest(caseFixture.scheduler, caseFixture.source, caseReceipt, test.route)
			if decision.Approved || !decision.Decline || decision.RunnerGateApproved || decision.PublishedHead != (core.Head{}) {
				t.Fatalf("tampered receipt decision=%+v", decision)
			}
			if test.name != "changed-head" && test.name != "changed-header-lineage" &&
				test.name != "changed-token" && test.name != "stale-election" {
				requireG4EOFCoreUnchanged(t, caseFixture, before)
			}
		})
	}
}

func TestCompactEOFRecoveryMetadataTransitionFaultsRollbackAndInvalidate(t *testing.T) {
	wantErr := errors.New("G4 injected transition fault")
	for _, stage := range []string{
		g4EOFFaultAfterProduce,
		g4EOFFaultAfterApply,
		g4EOFFaultAfterDrop,
	} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			fixture := newG4EOFFixture(t, g4EOFFixtureSpec{metadataActivation: true})
			fixture.scheduler.receipt.NoActionDrops = make(
				[]DiagnosticParserCoreGenericNoActionDrop,
				1,
				4,
			)
			fixture.scheduler.receipt.NoActionDrops[0].ElectionIndex = -1
			beforePublicReceipt := *fixture.scheduler.receipt
			beforePublicReceipt.NoActionDrops = append(
				[]DiagnosticParserCoreGenericNoActionDrop(nil),
				fixture.scheduler.receipt.NoActionDrops...,
			)
			beforeNoActionDropsPointer := reflect.ValueOf(
				fixture.scheduler.receipt.NoActionDrops,
			).Pointer()
			beforeNoActionDropsLength := len(fixture.scheduler.receipt.NoActionDrops)
			beforeNoActionDropsCapacity := cap(fixture.scheduler.receipt.NoActionDrops)
			beforeCore := snapshotG4EOFCore(t, fixture)
			beforeHeaders := append([]diagnosticParserCoreHeader(nil), fixture.scheduler.headers...)
			setCompactEOFRecoveryAdmissionFaultForTest(fixture.scheduler, stage, wantErr)
			stop, err := fixture.scheduler.dispatchPass()
			if stop != nil || !errors.Is(err, wantErr) {
				t.Fatalf("stage %s stop=%+v err=%v", stage, stop, err)
			}
			if !reflect.DeepEqual(fixture.scheduler.headers, beforeHeaders) {
				t.Fatalf("stage %s changed headers: before=%+v after=%+v", stage, beforeHeaders, fixture.scheduler.headers)
			}
			if !reflect.DeepEqual(*fixture.scheduler.receipt, beforePublicReceipt) {
				t.Fatalf(
					"stage %s changed the public receipt: before=%+v after=%+v",
					stage,
					beforePublicReceipt,
					*fixture.scheduler.receipt,
				)
			}
			if reflect.ValueOf(fixture.scheduler.receipt.NoActionDrops).Pointer() != beforeNoActionDropsPointer ||
				len(fixture.scheduler.receipt.NoActionDrops) != beforeNoActionDropsLength ||
				cap(fixture.scheduler.receipt.NoActionDrops) != beforeNoActionDropsCapacity {
				t.Fatalf(
					"stage %s changed the NoActionDrops slice header: len=%d cap=%d",
					stage,
					len(fixture.scheduler.receipt.NoActionDrops),
					cap(fixture.scheduler.receipt.NoActionDrops),
				)
			}
			receipt := compactEOFRecoveryAdmissionReceiptForTest(fixture.scheduler)
			if receipt.Valid || receipt.State != g4EOFStateInvalid || receipt.ConsumptionCount != 0 {
				t.Fatalf("stage %s left a usable receipt: %+v", stage, receipt)
			}
			requireG4EOFCoreUnchanged(t, fixture, beforeCore)
		})
	}
}

func TestCompactEOFRecoveryMetadataInvalidatesAcrossLifecycleBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *g4EOFFixture)
	}{
		{
			name: "next-election",
			mutate: func(t *testing.T, fixture *g4EOFFixture) {
				t.Helper()
				if err := advanceCompactEOFRecoveryElectionForTest(fixture.scheduler); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "begin-frontier",
			mutate: func(t *testing.T, fixture *g4EOFFixture) {
				t.Helper()
				if err := fixture.compact.BeginFrontier(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "changed-phase-checkpoint",
			mutate: func(t *testing.T, fixture *g4EOFFixture) {
				t.Helper()
				checkpoint, err := fixture.compact.InternCheckpoint([]byte("G4 changed phase"))
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.compact.SetPhaseCheckpoint(checkpoint); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "scheduler-reset",
			mutate: func(t *testing.T, fixture *g4EOFFixture) {
				t.Helper()
				if err := resetDiagnosticParserCoreGenericScheduler(fixture.scheduler); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "core-reset",
			mutate: func(t *testing.T, fixture *g4EOFFixture) {
				t.Helper()
				if err := fixture.compact.Reset(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "core-reset-releasing-retention",
			mutate: func(t *testing.T, fixture *g4EOFFixture) {
				t.Helper()
				if err := fixture.compact.ResetReleasingRetention(); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newG4EOFFixture(t, g4EOFFixtureSpec{metadataActivation: true})
			advanceG4EOFToSchedulerReturn(t, fixture)
			receipt := compactEOFRecoveryAdmissionReceiptForTest(fixture.scheduler)
			if receipt.State != g4EOFStateSchedulerReturned || !receipt.Valid {
				t.Fatalf("precondition receipt=%+v", receipt)
			}
			test.mutate(t, fixture)
			decision := gateCompactEOFRecoveryAdmissionForTest(
				fixture.scheduler,
				fixture.source,
				receipt,
				g4EOFRoutePublicTree,
			)
			if decision.Approved || !decision.Decline || decision.RunnerGateApproved || decision.ReceiptConsumed {
				t.Fatalf("%s accepted a stale receipt: %+v", test.name, decision)
			}
		})
	}
}

func TestCompactEOFRecoveryMetadataCompletionErrorInvalidatesReceipt(t *testing.T) {
	fixture := newG4EOFFixture(t, g4EOFFixtureSpec{metadataActivation: true})
	stop, err := fixture.scheduler.dispatchPass()
	if err != nil || stop != nil {
		t.Fatalf("dispatch EOF acceptance: stop=%+v err=%v", stop, err)
	}
	fixture.scheduler.token.Missing = true
	if err := fixture.scheduler.completeAcceptance(); err != nil {
		t.Fatal(err)
	}
	receipt := compactEOFRecoveryAdmissionReceiptForTest(fixture.scheduler)
	if receipt.Valid || receipt.State != g4EOFStateInvalid || receipt.ConsumptionCount != 0 {
		t.Fatalf("completion error left a usable receipt: %+v", receipt)
	}
}

func TestCompactEOFRecoveryMetadataBoundedChunkAndChildTraversal(t *testing.T) {
	source := bytes.Repeat([]byte("x\n"), 5000)
	fixture := newG4EOFFixture(t, g4EOFFixtureSpec{
		source: source, recoveryRoots: 130, metadataActivation: true,
	})
	receipt, err := produceCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, func() error { return nil })
	if err != nil || !receipt.Valid {
		t.Fatalf("bounded producer receipt=%+v err=%v", receipt, err)
	}
	wantSourceChunks := uint64((len(source) + g4EOFSourceChunkBytes - 1) / g4EOFSourceChunkBytes)
	wantChildGroups := uint64(1 + (130+g4EOFChildGroupSize-1)/g4EOFChildGroupSize)
	if receipt.Work.SourceChunks != wantSourceChunks || receipt.Work.ChildGroups != wantChildGroups ||
		receipt.Work.BytesInspected != uint64(len(source)) || receipt.Work.MaxSourceChunk > g4EOFSourceChunkBytes ||
		receipt.Work.MaxChildGroup > g4EOFChildGroupSize || receipt.Work.Overflow {
		t.Fatalf("bounded traversal work=%+v", receipt.Work)
	}
	if _, ok := checkedCompactEOFRecoveryAdmissionAddForTest(math.MaxUint64, 1); ok {
		t.Fatal("checked admission counter accepted uint64 overflow")
	}
	if _, ok := compactEOFRecoveryAdmissionCheckedMul(math.MaxUint64, 2); ok {
		t.Fatal("checked admission cost accepted uint64 overflow")
	}
}

func TestCompactEOFRecoveryMetadataProducerInternalOverflowInvalidates(t *testing.T) {
	for _, overflowPoint := range []string{"counter", "cost"} {
		overflowPoint := overflowPoint
		t.Run(overflowPoint, func(t *testing.T) {
			fixture := newG4EOFFixture(t, g4EOFFixtureSpec{metadataActivation: true})
			before := snapshotG4EOFCore(t, fixture)
			setCompactEOFRecoveryAdmissionOverflowForTest(fixture.scheduler, overflowPoint)
			receipt, err := produceCompactEOFRecoveryAdmissionForTest(
				fixture.scheduler,
				fixture.source,
				func() error { return nil },
			)
			if err == nil || !strings.Contains(err.Error(), "overflow") {
				t.Fatalf("%s overflow error=%v", overflowPoint, err)
			}
			if receipt.Valid || receipt.State != g4EOFStateInvalid || receipt.SelectedEvent != -1 ||
				receipt.Work.PublicationAttempts != 0 || receipt.Work.ParserConstructions != 0 ||
				receipt.Work.TreeConstructions != 0 || receipt.Work.SelectedStoreConstructions != 0 ||
				!receipt.Work.Overflow {
				t.Fatalf("%s overflow receipt=%+v", overflowPoint, receipt)
			}
			requireG4EOFCoreUnchanged(t, fixture, before)
		})
	}
}

func TestCompactEOFRecoveryMetadataPartialWorkReturnsNoReceipt(t *testing.T) {
	fixture := newG4EOFFixture(t, g4EOFFixtureSpec{
		source: []byte("*** Test Cases ***\nTest\n  Log  hello\n"), recoveryRoots: 4, metadataActivation: true,
	})
	before := snapshotG4EOFCore(t, fixture)
	wantErr := errors.New("G4 stop poll")
	polls := 0
	receipt, err := produceCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, func() error {
		polls++
		if polls == 2 {
			return wantErr
		}
		return nil
	})
	if !errors.Is(err, wantErr) || receipt.Valid || receipt.State != g4EOFStateInvalid || receipt.SelectedEvent != -1 {
		t.Fatalf("partial-work receipt=%+v err=%v", receipt, err)
	}
	if receipt.Work.Polls != 2 || receipt.Work.PublicationAttempts != 0 ||
		receipt.Work.ParserConstructions != 0 || receipt.Work.TreeConstructions != 0 ||
		receipt.Work.SelectedStoreConstructions != 0 {
		t.Fatalf("partial-work counters=%+v", receipt.Work)
	}
	requireG4EOFCoreUnchanged(t, fixture, before)
}

func TestCompactEOFRecoveryMetadataRepeatedValidDeclineValid(t *testing.T) {
	fixture := newG4EOFFixture(t, g4EOFFixtureSpec{metadataActivation: true})
	first, err := exerciseCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, g4EOFRoutePublicTree)
	if err != nil || !first.Approved || first.Activation != g4EOFActivationMetadata {
		t.Fatalf("first decision=%+v err=%v", first, err)
	}

	fixture.scheduler.electionIndex++
	fixture.scheduler.token.Symbol = 1
	middle, err := exerciseCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, g4EOFRoutePublicTree)
	if err != nil || middle.Approved || middle.Decline || middle.Activation != g4EOFActivationNone {
		t.Fatalf("middle non-EOF decision=%+v err=%v", middle, err)
	}

	fixture.scheduler.electionIndex++
	fixture.scheduler.token.Symbol = 0
	last, err := exerciseCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, g4EOFRouteSelectedStore)
	if err != nil || !last.Approved || last.Activation != g4EOFActivationMetadata {
		t.Fatalf("last decision=%+v err=%v", last, err)
	}
}

func TestCompactEOFRecoveryMetadataKeepsExplicitLegacyBypass(t *testing.T) {
	legacy := newG4EOFFixture(t, g4EOFFixtureSpec{legacyActivation: true})
	decision, err := exerciseCompactEOFRecoveryAdmissionForTest(legacy.scheduler, legacy.source, g4EOFRoutePublicTree)
	if err != nil || !decision.Approved || decision.Activation != g4EOFActivationLegacy || decision.MetadataOnly {
		t.Fatalf("legacy bypass decision=%+v err=%v", decision, err)
	}

	inactive := newG4EOFFixture(t, g4EOFFixtureSpec{})
	decision, err = exerciseCompactEOFRecoveryAdmissionForTest(inactive.scheduler, inactive.source, g4EOFRoutePublicTree)
	if err != nil || decision.Approved || decision.Decline || decision.Activation != g4EOFActivationNone {
		t.Fatalf("inactive decision=%+v err=%v", decision, err)
	}
}

func TestCompactEOFRecoveryMetadataDeclineReasonsStaySpecific(t *testing.T) {
	fixture := newG4EOFFixture(t, g4EOFFixtureSpec{metadataActivation: true, recoveryPaths: 2})
	receipt, err := produceCompactEOFRecoveryAdmissionForTest(fixture.scheduler, fixture.source, func() error { return nil })
	if err != nil || receipt.Valid || !strings.Contains(receipt.DeclineReason, "derivation") {
		t.Fatalf("wrong-derivation receipt=%+v err=%v", receipt, err)
	}
}

func setCompactEOFRecoveryAdmissionEnabledForTest(
	scheduler *diagnosticParserCoreGenericScheduler,
	enabled bool,
) {
	if scheduler != nil {
		scheduler.options.allowMetadataEOFAcceptRecovery = enabled
	}
}

func compactEOFRecoveryCoreGenerationForTest(compact *core.Core) uint64 {
	if compact == nil {
		return 0
	}
	return compact.AuthenticationGeneration()
}

func g4EOFStateFromInternal(state compactEOFRecoveryAdmissionState) string {
	return state.String()
}

func g4EOFReceiptFromInternal(receipt compactEOFRecoveryAdmissionReceipt) g4EOFReceipt {
	out := g4EOFReceipt{
		Active: receipt.active, Valid: receipt.valid, State: g4EOFStateFromInternal(receipt.state),
		Seal: receipt.seal, TransitionSeals: receipt.transitionSeals,
		DeclineReason: receipt.declineReason, CoreGeneration: receipt.coreGeneration,
		ElectionIndex: receipt.electionIndex, Token: receipt.token, SourceLength: receipt.sourceLength,
		SourceSHA256: receipt.sourceSHA256, NormalHead: receipt.normalHead, RecoveryHead: receipt.recoveryHead,
		NormalCreationSeq: receipt.normalCreationSeq, RecoveryCreationSeq: receipt.recoveryCreationSeq,
		NormalLineage: receipt.normalLineage, RecoveryLineage: receipt.recoveryLineage,
		NormalFingerprint: receipt.normalFingerprint, RecoveryFingerprint: receipt.recoveryFingerprint,
		NormalPayloads: receipt.normalPayloads, RecoveryPayloads: receipt.recoveryPayloads,
		NormalOccurrences: receipt.normalOccurrences, RecoveryOccurrences: receipt.recoveryOccurrences,
		NormalFrontier: receipt.normalFrontier, RecoveryFrontier: receipt.recoveryFrontier,
		SelectedEvent: receipt.selectedEvent, MetadataOnly: receipt.metadataOnly,
		ConsumptionCount: receipt.consumptionCount, ConstructionRoute: g4EOFRoute(receipt.constructionRoute),
		ObservedErrorCost: receipt.observedErrorCost,
		Work: g4EOFWork{
			Polls: receipt.work.polls, SourceChunks: receipt.work.sourceChunks,
			ChildGroups: receipt.work.childGroups, PathsVisited: receipt.work.pathsVisited,
			LinksVisited: receipt.work.linksVisited, PayloadRecordsVisited: receipt.work.payloadRecordsVisited,
			MaxDepth:       receipt.work.maxDepth,
			BytesInspected: receipt.work.bytesInspected, MaxSourceChunk: receipt.work.maxSourceChunk,
			MaxChildGroup: receipt.work.maxChildGroup, CheckedArithmetic: receipt.work.checkedArithmetic,
			PublicationAttempts:        receipt.work.publicationAttempts,
			ParserConstructions:        receipt.work.parserConstructions,
			TreeConstructions:          receipt.work.treeConstructions,
			SelectedStoreConstructions: receipt.work.selectedStoreConstructions,
			Overflow:                   receipt.work.overflow,
		},
	}
	for index, state := range receipt.transitions {
		out.Transitions[index] = g4EOFStateFromInternal(state)
	}
	for index, event := range receipt.events {
		out.Events[index] = g4EOFEvent{
			Ordinal: event.ordinal, Kind: event.kind.String(), Cost: event.cost,
			DynamicPrecedence: event.dynamicPrecedence,
		}
	}
	return out
}

func produceCompactEOFRecoveryAdmissionForTest(
	scheduler *diagnosticParserCoreGenericScheduler,
	source []byte,
	poll func() error,
) (g4EOFReceipt, error) {
	receipt, err := scheduler.produceCompactEOFRecoveryAdmission(source, poll)
	return g4EOFReceiptFromInternal(receipt), err
}

func compactEOFRecoveryAdmissionReceiptForTest(
	scheduler *diagnosticParserCoreGenericScheduler,
) g4EOFReceipt {
	if scheduler == nil {
		return g4EOFReceipt{State: g4EOFStateEmpty, SelectedEvent: -1}
	}
	return g4EOFReceiptFromInternal(scheduler.eofRecoveryAdmission)
}

func markCompactEOFRecoverySchedulerReturnedForTest(
	scheduler *diagnosticParserCoreGenericScheduler,
) error {
	if scheduler == nil {
		return errors.New("EOF recovery receipt is not complete")
	}
	return scheduler.markCompactEOFRecoverySchedulerReturned(scheduler.options.materializationSource)
}

func setCompactEOFRecoveryAdmissionFaultForTest(
	scheduler *diagnosticParserCoreGenericScheduler,
	stage string,
	wantErr error,
) {
	compactEOFRecoveryAdmissionFaultHook = func(candidate *diagnosticParserCoreGenericScheduler, current string) error {
		if candidate == scheduler && current == stage {
			return wantErr
		}
		return nil
	}
}

func setCompactEOFRecoveryAdmissionOverflowForTest(
	scheduler *diagnosticParserCoreGenericScheduler,
	point string,
) {
	consumed := false
	compactEOFRecoveryAdmissionOverflowHook = func(candidate *diagnosticParserCoreGenericScheduler, current string) bool {
		if consumed || candidate != scheduler || current != point {
			return false
		}
		consumed = true
		return true
	}
}

func advanceCompactEOFRecoveryElectionForTest(
	scheduler *diagnosticParserCoreGenericScheduler,
) error {
	if scheduler == nil || scheduler.electionIndex == math.MaxInt {
		return errors.New("EOF recovery test election overflow")
	}
	scheduler.electionIndex++
	compactEOFRecoveryAdmissionInvalidate(&scheduler.eofRecoveryAdmission, "new election")
	return nil
}

func checkedCompactEOFRecoveryAdmissionAddForTest(left, right uint64) (uint64, bool) {
	return compactEOFRecoveryAdmissionCheckedAdd(left, right)
}

func gateCompactEOFRecoveryAdmissionForTest(
	scheduler *diagnosticParserCoreGenericScheduler,
	source []byte,
	receipt g4EOFReceipt,
	route g4EOFRoute,
) g4EOFDecision {
	decision := g4EOFDecision{Activation: g4EOFActivationMetadata, Decline: true, SelectedEvent: -1}
	if scheduler == nil || scheduler.compact == nil || route == g4EOFRouteLivePublication {
		return decision
	}
	current := g4EOFReceiptFromInternal(scheduler.eofRecoveryAdmission)
	if !reflect.DeepEqual(receipt, current) || receipt.CoreGeneration != scheduler.compact.AuthenticationGeneration() ||
		receipt.ElectionIndex != scheduler.electionIndex || receipt.Token != scheduler.token ||
		receipt.SourceLength != uint32(len(source)) || receipt.SourceSHA256 != sha256.Sum256(source) {
		return decision
	}
	if len(scheduler.headers) == 2 {
		normal, normalOK := scheduler.compactEOFRecoveryAdmissionHeader(
			receipt.NormalHead,
			receipt.NormalCreationSeq,
			receipt.NormalLineage,
		)
		recovery, recoveryOK := scheduler.compactEOFRecoveryAdmissionHeader(
			receipt.RecoveryHead,
			receipt.RecoveryCreationSeq,
			receipt.RecoveryLineage,
		)
		if !normalOK || !recoveryOK || normal.accepted || recovery.accepted {
			return decision
		}
	}
	decision = g4EOFDecision{
		Activation: g4EOFActivationMetadata, Approved: true, SelectedEvent: 0,
		PublishedHead: receipt.NormalHead, CarriedThroughAcceptance: true,
		RunnerGateApproved: true, MetadataOnly: true, ReceiptConsumed: true,
	}
	return decision
}

func exerciseCompactEOFRecoveryAdmissionForTest(
	scheduler *diagnosticParserCoreGenericScheduler,
	source []byte,
	route g4EOFRoute,
) (g4EOFDecision, error) {
	if scheduler == nil {
		return g4EOFDecision{}, errors.New("nil scheduler")
	}
	if scheduler.options.allowMetadataEOFAcceptRecovery {
		receipt, err := scheduler.produceCompactEOFRecoveryAdmission(source, func() error { return nil })
		if err != nil {
			return g4EOFDecision{}, err
		}
		if !receipt.active {
			return g4EOFDecision{Activation: g4EOFActivationNone}, nil
		}
		if !receipt.valid {
			return g4EOFDecision{Activation: g4EOFActivationMetadata, Decline: true, SelectedEvent: -1}, nil
		}
		return gateCompactEOFRecoveryAdmissionForTest(scheduler, source, g4EOFReceiptFromInternal(receipt), route), nil
	}
	if scheduler.options.allowEOFAcceptNoActionSiblings {
		return g4EOFDecision{Activation: g4EOFActivationLegacy, Approved: true}, nil
	}
	return g4EOFDecision{Activation: g4EOFActivationNone}, nil
}
