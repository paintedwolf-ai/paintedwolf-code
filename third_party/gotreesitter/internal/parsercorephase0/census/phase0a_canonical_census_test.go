//go:build gts_parsercorephase0 && gts_parsercorephase0a

package census

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/odvcencio/gotreesitter/internal/benchfixtures"
	core "github.com/odvcencio/gotreesitter/internal/parsercorephase0"
)

type canonicalCensus struct {
	Fixture                     string            `json:"fixture"`
	SourceBytes                 int               `json:"source_bytes"`
	PopRoutes                   int               `json:"pop_routes"`
	PopRouteLinks               int               `json:"pop_route_links"`
	TrailingExtraMigrations     int               `json:"trailing_extra_migrations"`
	TrailingExtraRolledBack     int               `json:"trailing_extra_rolled_back"`
	TrailingExtraUnresolved     int               `json:"trailing_extra_unresolved"`
	UnresolvedCandidates        int               `json:"unresolved_candidates"`
	PopRoutesBound              int               `json:"pop_routes_bound"`
	PopRoutesUnbound            int               `json:"pop_routes_unbound"`
	AcceptedSelections          int               `json:"accepted_selections"`
	AcceptedSpineLinks          int               `json:"accepted_spine_links"`
	AcceptedBoundDirect         int               `json:"accepted_bound_direct"`
	AcceptedBoundFactorChoice   int               `json:"accepted_bound_factor_choice"`
	AcceptedResolvedDirect      int               `json:"accepted_resolved_direct"`
	AcceptedResolvedOther       int               `json:"accepted_resolved_other"`
	AcceptedUniqueJoins         int               `json:"accepted_unique_joins"`
	AcceptedUnjoined            int               `json:"accepted_unjoined"`
	RawCompactOccurrences       uint64            `json:"raw_compact_occurrences"`
	SelectedOccurrenceTrees     int               `json:"selected_occurrence_trees"`
	SelectedOccurrences         int               `json:"selected_occurrences"`
	SelectedTerminals           uint64            `json:"selected_terminals"`
	SelectedReductions          uint64            `json:"selected_reductions"`
	SelectedMigrated            uint64            `json:"selected_migrated"`
	SelectedMaxDepth            uint32            `json:"selected_max_depth"`
	SelectedDigest              string            `json:"selected_digest,omitempty"`
	RawSelectedDigest           string            `json:"raw_selected_digest,omitempty"`
	RawSelectedDigestError      string            `json:"raw_selected_digest_error,omitempty"`
	PublicSelectedNodes         uint64            `json:"public_selected_nodes"`
	FoldedOccurrences           uint64            `json:"folded_occurrences"`
	FoldRatePPM                 uint64            `json:"fold_rate_ppm"`
	RawSelectedError            string            `json:"raw_selected_error,omitempty"`
	ProofFailure                string            `json:"proof_failure,omitempty"`
	Blockers                    map[string]uint64 `json:"blockers"`
	EveryAcceptedEdgeJoined     bool              `json:"every_accepted_edge_joined"`
	EveryRawOccurrenceJoined    bool              `json:"every_raw_occurrence_joined"`
	EveryPublicOccurrenceJoined bool              `json:"every_public_occurrence_joined"`
}

type acceptedJoin struct {
	Occurrence core.ConstructionOccurrenceKey
	Edge       core.IncomingEdgeKey
}

type currentCensusContract struct {
	routes     int
	links      int
	migrations int
	spine      int
	raw        uint64
	public     uint64
	terminals  uint64
	reductions uint64
	migrated   uint64
	maxDepth   uint32
	digest     string
}

var currentCanonicalCensus = map[string]currentCensusContract{
	"query_compile": {routes: 8103, links: 14703, migrations: 32, spine: 1, raw: 11331, public: 7524, terminals: 5125, reductions: 6206, migrated: 17, maxDepth: 78, digest: "c6ab3989e0492de265c74ce19526d85d30223801b984156b4fa4d265f1a65a85"},
	"rewrite":       {routes: 1646, links: 2995, migrations: 16, spine: 1, raw: 2237, public: 1524, terminals: 1035, reductions: 1202, migrated: 16, maxDepth: 40, digest: "728ea27e31b3972b019db8b1e817d53caec9d905930df29ae475fe94846caad8"},
	"language":      {routes: 8408, links: 15864, migrations: 256, spine: 6, raw: 10761, public: 7082, terminals: 5057, reductions: 5704, migrated: 150, maxDepth: 125, digest: "481aba4139174c63b03fa6856b427fa09268539e240f951ac8fe40cf1e0fe006"},
	"grammargen_lr": {routes: 84928, links: 153463, migrations: 316, spine: 1, raw: 109614, public: 71768, terminals: 49911, reductions: 59703, migrated: 218, maxDepth: 348, digest: "e7a5faa302b6b27539694c0a08830cf026a288f6813607a3687a96340eaaab44"},
}

func TestCanonicalAcceptedRouteSpineCensus(t *testing.T) {
	fixtures, err := benchfixtures.LoadGoFullParseFixtures()
	if err != nil {
		t.Fatal(err)
	}
	lang := grammars.GoLanguage()
	if lang == nil {
		t.Fatal("Go language unavailable")
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.Fixture.ID, func(t *testing.T) {
			baseline := parseCanonical(t, fixture.Source)
			defer baseline.MaterializedTree.Release()

			if err := core.ResetPhase0ADiagnosticRunReceipts(); err != nil {
				t.Fatal(err)
			}
			if err := core.BeginPhase0AObserverSession(canonicalObserverLimits()); err != nil {
				t.Fatal(err)
			}
			sessionActive := true
			defer func() {
				if sessionActive {
					_ = core.EndPhase0AObserverSession()
				}
			}()

			observed := parseCanonical(t, fixture.Source)
			defer observed.MaterializedTree.Release()
			receipt, ok := core.TakePhase0ADiagnosticRunReceipt()
			if !ok {
				t.Fatal("observer driver produced no completed run receipt")
			}
			if _, extra := core.TakePhase0ADiagnosticRunReceipt(); extra {
				t.Fatal("observer driver produced more than one run receipt")
			}
			if err := core.EndPhase0AObserverSession(); err != nil {
				t.Fatal(err)
			}
			sessionActive = false

			requireUnperturbed(t, fixture, lang, baseline, observed)
			row := summarizeCanonicalCensus(fixture, observed, receipt)
			encoded, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("PHASE0A_CENSUS %s", encoded)
			requireCurrentCensusContract(t, row, receipt)
		})
	}
}

func requireCurrentCensusContract(t *testing.T, row canonicalCensus, receipt core.Phase0ADiagnosticRunReceipt) {
	t.Helper()
	want, ok := currentCanonicalCensus[row.Fixture]
	if !ok {
		t.Fatalf("fixture %q has no explicit Phase0A census contract", row.Fixture)
	}
	proof := receipt.Proof
	if proof.Failure != nil || row.ProofFailure != "" || len(row.Blockers) != 0 {
		t.Fatalf("fixture %s introduced a Phase0A blocker: failure=%+v blockers=%v", row.Fixture, proof.Failure, row.Blockers)
	}
	if row.PopRoutes != want.routes || row.PopRouteLinks != want.links || row.TrailingExtraMigrations != want.migrations || row.PopRoutesBound != want.routes || row.PopRoutesUnbound != 0 {
		t.Fatalf("fixture %s route census drifted: routes/links/migrations/bound/unbound=%d/%d/%d/%d/%d want=%d/%d/%d/%d/0", row.Fixture, row.PopRoutes, row.PopRouteLinks, row.TrailingExtraMigrations, row.PopRoutesBound, row.PopRoutesUnbound, want.routes, want.links, want.migrations, want.routes)
	}
	if row.TrailingExtraRolledBack != 0 || row.TrailingExtraUnresolved != 0 || row.UnresolvedCandidates != 0 {
		t.Fatalf("fixture %s migration proof retained rolled-back or unresolved rows: rolled_back=%d unresolved=%d candidates=%d", row.Fixture, row.TrailingExtraRolledBack, row.TrailingExtraUnresolved, row.UnresolvedCandidates)
	}
	if row.AcceptedSelections != 1 || row.AcceptedSpineLinks != want.spine || row.AcceptedBoundDirect != want.spine || row.AcceptedBoundFactorChoice != 0 || row.AcceptedResolvedDirect != want.spine || row.AcceptedResolvedOther != 0 || row.AcceptedUniqueJoins != want.spine || row.AcceptedUnjoined != 0 || !row.EveryAcceptedEdgeJoined {
		t.Fatalf("fixture %s accepted-spine census drifted: %+v want one selection and %d direct uniquely joined links", row.Fixture, row, want.spine)
	}
	if receipt.RawSelectedError != "" {
		t.Fatalf("fixture %s raw selected census failed: %s", row.Fixture, receipt.RawSelectedError)
	}
	if receipt.RawSelectedDigestError != "" {
		t.Fatalf("fixture %s raw selected digest failed: %s", row.Fixture, receipt.RawSelectedDigestError)
	}
	if receipt.RawSelected.Nodes != want.raw || row.PublicSelectedNodes != want.public {
		t.Fatalf("fixture %s selected census drifted: raw/public=%d/%d want=%d/%d", row.Fixture, receipt.RawSelected.Nodes, row.PublicSelectedNodes, want.raw, want.public)
	}
	if len(proof.SelectedOccurrenceTrees) != 1 || len(proof.SelectedOccurrences) != int(want.raw) {
		t.Fatalf("fixture %s selected occurrence proof width=%d/%d want=1/%d", row.Fixture, len(proof.SelectedOccurrenceTrees), len(proof.SelectedOccurrences), want.raw)
	}
	tree := proof.SelectedOccurrenceTrees[0]
	if tree.Namespace != proof.Namespace || tree.Generation != proof.AcceptedSelections[0].Generation || tree.FirstOccurrence != 0 || tree.OccurrenceCount != want.raw ||
		tree.TerminalCount != want.terminals || tree.ReductionCount != want.reductions || tree.MigratedCount != want.migrated || tree.MaxDepth != want.maxDepth || tree.Digest != receipt.RawSelectedDigest {
		t.Fatalf("fixture %s selected occurrence snapshot disagrees with independent compact walk: tree=%+v raw_digest=%x", row.Fixture, tree, receipt.RawSelectedDigest)
	}
	if want.digest != "" && row.SelectedDigest != want.digest {
		t.Fatalf("fixture %s selected occurrence digest=%s want=%s", row.Fixture, row.SelectedDigest, want.digest)
	}
	if !row.EveryRawOccurrenceJoined {
		t.Fatalf("fixture %s did not join every raw selected occurrence", row.Fixture)
	}
	if row.EveryPublicOccurrenceJoined {
		t.Fatalf("fixture %s unexpectedly claimed recursive public-occurrence joins; Phase0A currently proves only the accepted physical spine", row.Fixture)
	}
}

func parseCanonical(t *testing.T, source []byte) gotreesitter.DiagnosticParserCorePrefixResult {
	t.Helper()
	result, err := gotreesitter.DiagnosticParseParserCorePrefix(
		grammars.GoExternalScanner{}, source,
		gotreesitter.DiagnosticParserCorePrefixOptions{
			ReceiptMode:   gotreesitter.DiagnosticParserCoreReceiptSummary,
			MaxTokens:     300000,
			MaxDispatches: 600000,
			Limits: core.Limits{
				MaxNodes: 1 << 20, MaxLinks: 1 << 20, MaxSubtrees: 1 << 20,
				MaxChildren: 4 << 20, MaxMetadata: 2 << 20,
				MaxLinksPerBoundary: 8, MaxPopPaths: 1 << 16, MaxDerivations: 1 << 16,
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || !result.Materialized || result.MaterializedTree == nil || result.GenericScheduler == nil || result.GenericScheduler.Acceptance == nil {
		t.Fatalf("canonical route did not accept and materialize: %+v", result)
	}
	return result
}

func requireUnperturbed(t *testing.T, fixture benchfixtures.LoadedFixture, lang *gotreesitter.Language, baseline, observed gotreesitter.DiagnosticParserCorePrefixResult) {
	t.Helper()
	if baseline.Boundary != observed.Boundary || baseline.Detail != observed.Detail || baseline.Dispatches != observed.Dispatches || baseline.Tokens != observed.Tokens || baseline.State != observed.State || baseline.Lookahead != observed.Lookahead || baseline.SourceSHA256 != observed.SourceSHA256 || baseline.GrammarBlobSHA256 != observed.GrammarBlobSHA256 || baseline.Grammar != observed.Grammar || baseline.ExactRootDFA != observed.ExactRootDFA {
		t.Fatalf("observer perturbed canonical result metadata\nbaseline=%+v\nobserved=%+v", baseline, observed)
	}
	if !reflect.DeepEqual(baseline.GenericScheduler, observed.GenericScheduler) {
		t.Fatalf("observer perturbed canonical scheduler, work, or acceptance for %s", fixture.Fixture.ID)
	}
	baselineTree, err := benchfixtures.InspectGoTree(baseline.MaterializedTree.RootNode(), lang)
	if err != nil {
		t.Fatal(err)
	}
	observedTree, err := benchfixtures.InspectGoTree(observed.MaterializedTree.RootNode(), lang)
	if err != nil {
		t.Fatal(err)
	}
	if baselineTree.SHA256 != fixture.Fixture.DeepTreeSHA256 || observedTree.SHA256 != fixture.Fixture.DeepTreeSHA256 || !reflect.DeepEqual(baselineTree, observedTree) {
		t.Fatalf("observer perturbed deep tree for %s: baseline=%+v observed=%+v want=%s", fixture.Fixture.ID, baselineTree, observedTree, fixture.Fixture.DeepTreeSHA256)
	}
}

func summarizeCanonicalCensus(fixture benchfixtures.LoadedFixture, result gotreesitter.DiagnosticParserCorePrefixResult, receipt core.Phase0ADiagnosticRunReceipt) canonicalCensus {
	proof := receipt.Proof
	expressions := make(map[core.Phase0AExpressionID]core.Phase0AExpressionKind, len(proof.Expressions))
	for _, expression := range proof.Expressions {
		expressions[expression.ID] = expression.Kind
	}
	row := canonicalCensus{
		Fixture: fixture.Fixture.ID, SourceBytes: len(fixture.Source),
		PopRoutes: len(proof.PopRoutes), PopRouteLinks: len(proof.PopRouteLinks), TrailingExtraMigrations: len(proof.TrailingExtraMigrations),
		AcceptedSelections: len(proof.AcceptedSelections), AcceptedSpineLinks: len(proof.AcceptedLinks),
		RawCompactOccurrences:  receipt.RawSelected.Nodes,
		PublicSelectedNodes:    result.GenericScheduler.Acceptance.SelectedNodes,
		RawSelectedError:       receipt.RawSelectedError,
		RawSelectedDigest:      hex.EncodeToString(receipt.RawSelectedDigest[:]),
		RawSelectedDigestError: receipt.RawSelectedDigestError,
		Blockers:               make(map[string]uint64),
	}
	row.SelectedOccurrenceTrees = len(proof.SelectedOccurrenceTrees)
	row.SelectedOccurrences = len(proof.SelectedOccurrences)
	if len(proof.SelectedOccurrenceTrees) == 1 {
		tree := proof.SelectedOccurrenceTrees[0]
		row.SelectedTerminals, row.SelectedReductions, row.SelectedMigrated, row.SelectedMaxDepth = tree.TerminalCount, tree.ReductionCount, tree.MigratedCount, tree.MaxDepth
		row.SelectedDigest = hex.EncodeToString(tree.Digest[:])
	}
	for _, route := range proof.PopRoutes {
		if route.Occurrence == (core.ConstructionOccurrenceKey{}) || route.Edge == (core.IncomingEdgeKey{}) {
			row.PopRoutesUnbound++
		} else {
			row.PopRoutesBound++
		}
	}
	for _, migration := range proof.TrailingExtraMigrations {
		if migration.RolledBack {
			row.TrailingExtraRolledBack++
			continue
		}
		if migration.SourceLink == 0 || migration.SourceLowerLink == 0 || migration.SourceExpression == 0 || migration.SourceOccurrence == (core.ConstructionOccurrenceKey{}) || migration.SourceEdge == (core.IncomingEdgeKey{}) || migration.TargetLink == 0 || migration.TargetOccurrence == (core.ConstructionOccurrenceKey{}) || migration.TargetEdge == (core.IncomingEdgeKey{}) || migration.Occurrence == (core.ConstructionOccurrenceKey{}) || migration.Edge == (core.IncomingEdgeKey{}) || migration.Payload == 0 || migration.Predecessor == 0 {
			row.TrailingExtraUnresolved++
		}
	}
	for _, candidate := range proof.Candidates {
		if !candidate.RolledBack && !candidate.Resolved {
			row.UnresolvedCandidates++
		}
	}
	joins := make(map[acceptedJoin]struct{}, len(proof.AcceptedLinks))
	for _, link := range proof.AcceptedLinks {
		switch expressions[link.BoundExpression] {
		case core.Phase0AExpressionDirect:
			row.AcceptedBoundDirect++
		case core.Phase0AExpressionFactorChoice:
			row.AcceptedBoundFactorChoice++
		}
		if expressions[link.ResolvedExpression] == core.Phase0AExpressionDirect {
			row.AcceptedResolvedDirect++
		} else {
			row.AcceptedResolvedOther++
		}
		join := acceptedJoin{Occurrence: link.Occurrence, Edge: link.Edge}
		if join.Occurrence == (core.ConstructionOccurrenceKey{}) || join.Edge == (core.IncomingEdgeKey{}) {
			row.AcceptedUnjoined++
			continue
		}
		if _, duplicate := joins[join]; duplicate {
			row.AcceptedUnjoined++
			continue
		}
		joins[join] = struct{}{}
		row.AcceptedUniqueJoins++
	}
	row.EveryAcceptedEdgeJoined = row.AcceptedSelections == 1 && row.AcceptedSpineLinks > 0 && row.AcceptedUnjoined == 0 && row.AcceptedUniqueJoins == row.AcceptedSpineLinks
	row.EveryRawOccurrenceJoined = len(proof.SelectedOccurrenceTrees) == 1 && uint64(len(proof.SelectedOccurrences)) == receipt.RawSelected.Nodes && proof.SelectedOccurrenceTrees[0].OccurrenceCount == receipt.RawSelected.Nodes && proof.SelectedOccurrenceTrees[0].Digest == receipt.RawSelectedDigest
	row.EveryPublicOccurrenceJoined = row.EveryAcceptedEdgeJoined && uint64(row.AcceptedSpineLinks) == row.PublicSelectedNodes
	if row.RawCompactOccurrences >= row.PublicSelectedNodes {
		row.FoldedOccurrences = row.RawCompactOccurrences - row.PublicSelectedNodes
		if row.RawCompactOccurrences != 0 {
			row.FoldRatePPM = row.FoldedOccurrences * 1000000 / row.RawCompactOccurrences
		}
	}
	if proof.Failure != nil {
		row.ProofFailure = proof.Failure.Error()
		row.Blockers[string(proof.Failure.Kind)]++
	}
	if receipt.RawSelectedError != "" {
		row.Blockers["raw_selected_census"]++
	}
	if receipt.RawSelectedDigestError != "" {
		row.Blockers["raw_selected_digest"]++
	}
	return row
}

func canonicalObserverLimits() core.Phase0AObserverLimits {
	return core.Phase0AObserverLimits{
		MaxCores: 1, MaxRecords: 8_000_000, MaxBytes: 1 << 30,
		MaxFrames: 1_000_000, MaxMutations: 1_000_000,
		MaxOccurrences: 1_000_000, MaxOccurrenceBytes: 128 << 20,
		MaxCandidates: 500_000, MaxExpressions: 500_000, MaxBindings: 500_000,
		MaxTransitions: 1_000_000, MaxSelectors: 250_000, MaxSelectorRoutes: 500_000,
		MaxPopRoutes: 500_000, MaxPopRouteLinks: 1_000_000,
		MaxSelectionCapabilities: 4, MaxAcceptedSelections: 4, MaxAcceptedLinks: 500_000,
		MaxSelectedOccurrences: 250_000, MaxSelectedDepth: 4096,
		MaxSelectedIndexEntries: 2_000_000, MaxSelectedIndexBytes: 512 << 20,
	}
}
