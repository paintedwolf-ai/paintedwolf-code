//go:build !gts_no_parsercorephase0 && gts_eof_history_census && gts_eof_recovery_shadow

package gotreesitter

import (
	"crypto/sha256"
	"errors"
	"unsafe"

	core "github.com/odvcencio/gotreesitter/internal/parsercorephase0"
)

// diagnosticEOFRecoveryReductionPlans exposes only the immutable reduction
// plans that generic materialization validates. It retains no Parser.
type diagnosticEOFRecoveryReductionPlans struct {
	tables *parserCoreLanguageTables
}

const diagnosticEOFRecoveryReductionPlanProviderBytes = uint64(unsafe.Sizeof(diagnosticEOFRecoveryReductionPlans{}))

func newDiagnosticEOFRecoveryReductionPlans(parser *Parser) (*diagnosticEOFRecoveryReductionPlans, error) {
	tables, err := acquireParserCoreLanguageTables(parser)
	if err != nil {
		return nil, err
	}
	if tables == nil {
		return nil, errors.New("private EOF recovery has no immutable language tables")
	}
	return &diagnosticEOFRecoveryReductionPlans{tables: tables}, nil
}

func (p *diagnosticEOFRecoveryReductionPlans) ReductionPlan(
	productionID uint16,
	childCount int,
) (core.ReductionPlan, error) {
	if p == nil || p.tables == nil || childCount < 0 || childCount >= p.tables.reductionPlanStride {
		return core.ReductionPlan{}, errors.New("parser-core phase zero: reduction plan pair is outside authenticated index")
	}
	pairIndex := int(productionID)*p.tables.reductionPlanStride + childCount
	if pairIndex < 0 || pairIndex >= len(p.tables.reductionPlanIndex) {
		return core.ReductionPlan{}, errors.New("parser-core phase zero: reduction plan production is outside authenticated index")
	}
	planID := p.tables.reductionPlanIndex[pairIndex]
	if planID == 0 || int(planID) > len(p.tables.reductionPlans) {
		return core.ReductionPlan{}, errors.New("parser-core phase zero: reduction plan pair was not authenticated from an action row")
	}
	return p.tables.reductionPlans[planID-1], nil
}

func (s *diagnosticParserCoreGenericScheduler) censusEOFRecoveryShadow(
	headerIndex int,
	paths []core.Derivation,
	record *EOFAcceptHistoryHead,
) {
	if record == nil {
		return
	}
	receipt := &EOFRecoveryShadowReceipt{AcceptIndex: 1, Kind: "recover_eof"}
	record.RecoveryShadow = receipt
	if s == nil || s.compact == nil || headerIndex < 0 || headerIndex >= len(s.headers) ||
		!s.options.materializationContextSet || s.options.materializationParser == nil ||
		s.options.materializationParser.language == nil {
		receipt.Error = "no materialization context"
		return
	}
	if len(paths) != 1 {
		receipt.Error = "private EOF recovery requires one exact derivation"
		return
	}
	if parserCoreReplayParseStatesEnabled() {
		receipt.Error = "private EOF recovery does not support global parse-state replay"
		return
	}

	head := s.headers[headerIndex].head
	beforeHeader, err := diagnosticParserCoreHeaderReceipt(s.compact, s.headers[headerIndex])
	if err != nil {
		receipt.Error = err.Error()
		return
	}
	beforeStats, err := s.compact.Stats(head)
	if err != nil {
		receipt.Error = err.Error()
		return
	}
	beforeWork := s.compact.Work()
	defer func() {
		afterHeader, headerErr := diagnosticParserCoreHeaderReceipt(s.compact, s.headers[headerIndex])
		afterStats, statsErr := s.compact.Stats(head)
		receipt.LiveHeaderStatsWorkUnchanged = headerErr == nil && statsErr == nil &&
			afterHeader == beforeHeader && afterStats == beforeStats && s.compact.Work() == beforeWork
	}()

	liveParser := s.options.materializationParser
	isolatedParser := NewParser(liveParser.language)
	receipt.IsolatedParser = isolatedParser != nil && isolatedParser != liveParser
	receipt.SharedLanguagePointer = isolatedParser != nil && isolatedParser.language == liveParser.language
	if !receipt.IsolatedParser || !receipt.SharedLanguagePointer {
		receipt.Error = "private EOF recovery did not isolate the parser"
		return
	}

	shadow, root, forkReceipt, err := core.ForkDiagnosticEOFRecovery(
		s.compact,
		head,
		paths[0].Payloads,
		diagnosticEOFRecoveryReductionPlanProviderBytes,
	)
	copyEOFRecoveryForkReceipt(receipt, forkReceipt)
	if err != nil {
		receipt.Error = err.Error()
		return
	}

	provider, err := newDiagnosticEOFRecoveryReductionPlans(isolatedParser)
	if err != nil {
		receipt.Error = err.Error()
		return
	}
	cachedTables, _ := isolatedParser.language.compactTables.(*parserCoreLanguageTables)
	receipt.ProviderConstructedFromIsolatedParser = provider.tables != nil && provider.tables == cachedTables
	receipt.ProviderSharesImmutableLanguageTables = receipt.ProviderConstructedFromIsolatedParser
	_, hasTableView := any(provider).(core.TableView)
	_, hasSelectedStore := any(provider).(core.SelectedStorePolicyProvider)
	receipt.ProviderReductionPlanOnly = !hasTableView && !hasSelectedStore
	providerReceipt, err := shadow.BindDiagnosticEOFRecoveryReductionPlans(s.compact, provider)
	receipt.SourceProvidersDetached = providerReceipt.SourceProvidersDetached
	receipt.IsolatedReductionPlansAttached = providerReceipt.ReductionPlansAttached
	receipt.ProviderDiffersFromLive = providerReceipt.ProviderDiffersFromSource
	receipt.ProviderTableViewDetached = providerReceipt.TableViewDetached
	receipt.ProviderSelectedStoreDetached = providerReceipt.SelectedStoreDetached
	if err != nil {
		receipt.Error = err.Error()
		return
	}
	if !receipt.ProviderConstructedFromIsolatedParser || !receipt.ProviderSharesImmutableLanguageTables ||
		!receipt.ProviderReductionPlanOnly || !receipt.IsolatedReductionPlansAttached ||
		!receipt.ProviderDiffersFromLive || !receipt.ProviderTableViewDetached ||
		!receipt.ProviderSelectedStoreDetached {
		receipt.Error = "private EOF recovery did not isolate the reduction-plan provider"
		return
	}
	validationScratchBefore := shadow.DiagnosticEOFRecoveryValidationScratch()
	receipt.ValidationScratchReserved = receipt.ValidationScratchReserved &&
		validationScratchBefore == (core.DiagnosticEOFRecoveryValidationScratch{
			StructuralPositions: receipt.ValidationStructuralPositions,
			RemappedFields:      receipt.ValidationRemappedFields,
			RemappedAliases:     receipt.ValidationRemappedAliases,
		})
	if !receipt.ValidationScratchReserved {
		receipt.Error = "private EOF recovery did not reserve the planned validation scratch"
		return
	}

	isolatedScratch := new(parserCoreRunnerScratch)
	receipt.IsolatedScratch = isolatedScratch != nil
	if !receipt.IsolatedScratch {
		receipt.Error = "private EOF recovery did not isolate runner scratch"
		return
	}

	tree, err := materializeDiagnosticParserCoreAcceptedSelectionWithRootFinalization(
		shadow,
		head,
		[]core.SubtreeID{root},
		isolatedParser,
		s.options.materializationSource,
		isolatedScratch,
		false,
		true,
		diagnosticParserCoreFinalizeRecoverEOF,
	)
	if err != nil {
		receipt.Error = err.Error()
		return
	}
	defer tree.Release()
	receipt.ValidationScratchNoGrowth = shadow.DiagnosticEOFRecoveryValidationScratch() == validationScratchBefore
	if !receipt.ValidationScratchNoGrowth {
		receipt.Error = "private EOF recovery materialization grew validation scratch"
		return
	}
	if tree.root == nil {
		receipt.Error = errors.New("private EOF recovery materialized no root").Error()
		return
	}
	rootNode := tree.RootNode()
	receipt.RootSymbol = tree.root.symbol
	receipt.RootNamed = rootNode.IsNamed()
	receipt.RootExtra = rootNode.IsExtra()
	receipt.RootMissing = rootNode.IsMissing()
	receipt.RootIsError = rootNode.IsError()
	receipt.RootHasError = rootNode.HasError()
	receipt.RootDynamicPrecedence = tree.root.dynamicPrecedence
	receipt.RootShape = eofAcceptHistoryTreeShape(isolatedParser.language, rootNode)
	receipt.DeepSHA256 = sha256.Sum256([]byte(receipt.RootShape))
	receipt.ErrorCost = cNodeErrorCostLang(isolatedParser.language, tree.root)
}

func copyEOFRecoveryForkReceipt(target *EOFRecoveryShadowReceipt, source core.DiagnosticEOFRecoveryForkReceipt) {
	if target == nil {
		return
	}
	target.Steps = source.Steps
	target.MaxSteps = source.MaxSteps
	target.Payloads = source.Payloads
	target.MaxPayloads = source.MaxPayloads
	target.SourceFootprintBytes = source.SourceFootprintBytes
	target.CoreHeaderBytes = source.CoreHeaderBytes
	target.CopiedArenaBytes = source.CopiedArenaBytes
	target.AppendReserveBytes = source.AppendReserveBytes
	target.MapBytes = source.MapBytes
	target.TemporaryBytes = source.TemporaryBytes
	target.PreservationBytes = source.PreservationBytes
	target.ProviderWrapperBytes = source.ProviderWrapperBytes
	target.ValidationStructuralPositions = source.ValidationStructuralPositions
	target.ValidationRemappedFields = source.ValidationRemappedFields
	target.ValidationRemappedAliases = source.ValidationRemappedAliases
	target.ValidationScratchReserved = source.ValidationScratchReserved
	target.PeakCloneBytes = source.PeakCloneBytes
	target.MaxCloneBytes = source.MaxCloneBytes
	target.StartByte = source.StartByte
	target.EndByte = source.EndByte
	target.SubtreesBefore = source.SubtreesBefore
	target.SubtreesAfter = source.SubtreesAfter
	target.ChildrenBefore = source.ChildrenBefore
	target.ChildrenAfter = source.ChildrenAfter
	target.CheckpointMapEntries = source.CheckpointMapEntries
	target.RetainedSelectedPolicy = source.RetainedSelectedPolicy
	target.SourceSchedulerActive = source.SourceSchedulerActive
	target.SchedulerFrameDetached = source.SchedulerFrameDetached
	target.SourceProvidersDetached = source.SourceProvidersDetached
	target.CopiedArenaPrefixesEqual = source.CopiedArenaPrefixesEqual
	target.CopiedHeadersEqual = source.CopiedHeadersEqual
	target.MetadataConstructionUnauthenticated = source.MetadataConstructionUnauthenticated
	target.RootChildrenExact = source.RootChildrenExact
	target.MutableStorageDisjoint = source.MutableStorageDisjoint
	target.WorkBefore = source.WorkBefore
	target.WorkAfter = source.WorkAfter
}
