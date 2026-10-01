//go:build gts_workcount

package gotreesitter_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
	"github.com/odvcencio/gotreesitter/internal/benchfixtures"
)

type pairedSemanticTraceReceipt struct {
	Schema            string                   `json:"schema"`
	Contract          string                   `json:"contract"`
	GrammarBlobSHA256 string                   `json:"grammar_blob_sha256"`
	Rows              []pairedSemanticTraceRow `json:"rows"`
	Production        pairedSemanticProduction `json:"production"`
	Compact           json.RawMessage          `json:"compact"`
	StaticC           json.RawMessage          `json:"static_c"`
	Decision          json.RawMessage          `json:"decision"`
}

type pairedSemanticProduction struct {
	BaseCommit          string `json:"base_commit"`
	PatchManifest       string `json:"patch_manifest"`
	PatchManifestSHA256 string `json:"patch_manifest_sha256"`
	PatchSetSHA256      string `json:"patch_set_sha256"`
	TraceContract       string `json:"trace_contract"`
	Observer            string `json:"observer"`
}

type pairedSemanticPatchManifest struct {
	Schema            string                            `json:"schema"`
	HashContract      string                            `json:"hash_contract"`
	BaseCommit        string                            `json:"base_commit"`
	PatchSetSHA256    string                            `json:"patch_set_sha256"`
	Files             []pairedSemanticPatchManifestFile `json:"files"`
	ExcludedGenerated []string                          `json:"excluded_generated"`
}

type pairedSemanticPatchManifestFile struct {
	Path              string `json:"path"`
	Status            string `json:"status"`
	BaseContentSHA256 string `json:"base_content_sha256"`
	ContentSHA256     string `json:"content_sha256"`
}

func TestDiagnosticPairedSemanticTracePatchManifest(t *testing.T) {
	const manifestPath = "cgo_harness/work_count/paired_semantic_trace_patch_manifest_v1.json"
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest pairedSemanticPatchManifest
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != "gts-base-pinned-patch-manifest/v1" || manifest.HashContract != "gts-base-pinned-patch-set/v1" || manifest.BaseCommit != "1743205133e5776b82d175ccf157927198e365de" {
		t.Fatalf("patch manifest identity drifted: %+v", manifest)
	}
	if len(manifest.Files) == 0 || !sort.SliceIsSorted(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path }) {
		t.Fatalf("patch manifest files are absent or unsorted: %+v", manifest.Files)
	}
	h := sha256.New()
	writePatchField := func(value string) {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	writePatchField(manifest.HashContract)
	writePatchField(manifest.BaseCommit)
	seen := make(map[string]bool, len(manifest.Files))
	for _, file := range manifest.Files {
		if file.Path == manifestPath || file.Path == "cgo_harness/work_count/paired_semantic_trace_receipt_v1.json" || seen[file.Path] || (file.Status != "modified" && file.Status != "added") || len(file.BaseContentSHA256) != 64 || len(file.ContentSHA256) != 64 {
			t.Fatalf("invalid or self-referential patch manifest file: %+v", file)
		}
		seen[file.Path] = true
		contents, err := os.ReadFile(file.Path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(contents)); got != file.ContentSHA256 {
			t.Fatalf("patch manifest content drift path=%s got=%s want=%s", file.Path, got, file.ContentSHA256)
		}
		writePatchField(file.Status)
		writePatchField(file.Path)
		writePatchField(file.BaseContentSHA256)
		writePatchField(file.ContentSHA256)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != manifest.PatchSetSHA256 {
		t.Fatalf("patch-set digest=%s want=%s", got, manifest.PatchSetSHA256)
	}
	wantExcluded := []string{manifestPath, "cgo_harness/work_count/paired_semantic_trace_receipt_v1.json"}
	if !reflect.DeepEqual(manifest.ExcludedGenerated, wantExcluded) {
		t.Fatalf("patch manifest exclusions=%v want=%v", manifest.ExcludedGenerated, wantExcluded)
	}
}

type pairedSemanticTraceRow struct {
	Fixture        string `json:"fixture"`
	SourceSHA256   string `json:"source_sha256"`
	SourceBytes    uint32 `json:"source_bytes"`
	DeepTreeSHA256 string `json:"deep_tree_sha256"`
	Production     struct {
		TraceSHA256                string `json:"trace_sha256"`
		EventsSeen                 uint64 `json:"events_seen"`
		EventsDropped              uint64 `json:"events_dropped"`
		ActionLookups              uint64 `json:"action_lookups"`
		ActionExecutions           uint64 `json:"action_executions"`
		Decisions                  uint64 `json:"decisions"`
		Shifts                     uint64 `json:"shifts"`
		Reductions                 uint64 `json:"reductions"`
		Accepts                    uint64 `json:"accepts"`
		ScannerExactRetained       uint64 `json:"scanner_exact_retained"`
		ScannerUnavailableRetained uint64 `json:"scanner_unavailable_retained"`
		ActionCellDistinctHashes   uint64 `json:"action_cell_distinct_hashes"`
		BoundaryDistinctHashes     uint64 `json:"boundary_distinct_hashes"`
		ActionCellAuditTruncated   bool   `json:"action_cell_audit_truncated"`
		BoundaryAuditTruncated     bool   `json:"boundary_audit_truncated"`
		CollisionAuditComplete     bool   `json:"collision_audit_complete"`
	} `json:"production"`
	Compact struct {
		Tokens             uint64 `json:"tokens"`
		Dispatches         uint64 `json:"dispatches"`
		ActionLookups      uint64 `json:"action_lookups"`
		Canonicalizations  uint64 `json:"canonicalizations"`
		Shifts             uint64 `json:"shifts"`
		Reductions         uint64 `json:"reductions"`
		Accepts            uint64 `json:"accepts"`
		EmittedPopPaths    uint64 `json:"emitted_pop_paths"`
		EmittedPopPayloads uint64 `json:"emitted_pop_payloads"`
	} `json:"compact"`
}

func TestDiagnosticPairedSemanticTraceCanonicalReceipt(t *testing.T) {
	data, err := os.ReadFile("cgo_harness/work_count/paired_semantic_trace_receipt_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var receipt pairedSemanticTraceReceipt
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Schema != "gts-paired-semantic-trace-receipt/v1" || receipt.Contract != "gts-paired-semantic-trace-contract/v1" || receipt.GrammarBlobSHA256 != benchfixtures.GoGrammarBlobSHA256 {
		t.Fatalf("paired receipt identity drifted: schema=%q contract=%q grammar=%q", receipt.Schema, receipt.Contract, receipt.GrammarBlobSHA256)
	}
	manifestData, err := os.ReadFile(receipt.Production.PatchManifest)
	if err != nil {
		t.Fatal(err)
	}
	var manifest pairedSemanticPatchManifest
	manifestDecoder := json.NewDecoder(strings.NewReader(string(manifestData)))
	manifestDecoder.DisallowUnknownFields()
	if err := manifestDecoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if receipt.Production.BaseCommit != manifest.BaseCommit || receipt.Production.TraceContract != gotreesitter.DiagnosticSemanticPhaseTraceContract || receipt.Production.Observer != "diagnostic_only" ||
		receipt.Production.PatchSetSHA256 != manifest.PatchSetSHA256 || receipt.Production.PatchManifestSHA256 != fmt.Sprintf("%x", sha256.Sum256(manifestData)) {
		t.Fatalf("paired receipt does not pin its production patch manifest: production=%+v manifest=%+v", receipt.Production, manifest)
	}
	fixtures, err := benchfixtures.LoadGoFullParseFixtures()
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.Rows) != len(fixtures) {
		t.Fatalf("paired rows=%d want=%d", len(receipt.Rows), len(fixtures))
	}
	rows := make(map[string]pairedSemanticTraceRow, len(receipt.Rows))
	for _, row := range receipt.Rows {
		if _, exists := rows[row.Fixture]; exists {
			t.Fatalf("duplicate paired fixture %q", row.Fixture)
		}
		rows[row.Fixture] = row
	}
	for _, fixture := range fixtures {
		row, ok := rows[fixture.Fixture.ID]
		if !ok {
			t.Fatalf("paired receipt missing fixture %q", fixture.Fixture.ID)
		}
		if row.SourceSHA256 != fixture.Fixture.SHA256 || row.SourceBytes != uint32(len(fixture.Source)) || row.DeepTreeSHA256 != fixture.Fixture.DeepTreeSHA256 {
			t.Fatalf("%s paired fixture identity drifted: %+v", fixture.Fixture.ID, row)
		}
		offCounts, offDigest := parseSemanticPhaseTraceFixture(t, fixture.Source)
		onCounts, onDigest, trace := parseSemanticPhaseTraceFixtureObserved(t, fixture.Source)
		if offDigest != onDigest || onDigest != fixture.Fixture.DeepTreeSHA256 || !reflect.DeepEqual(offCounts, onCounts) {
			t.Fatalf("%s observer mismatch off/on=%s/%s counts_equal=%v", fixture.Fixture.ID, offDigest, onDigest, reflect.DeepEqual(offCounts, onCounts))
		}
		traceData, err := json.Marshal(trace)
		if err != nil {
			t.Fatal(err)
		}
		traceSum := sha256.Sum256(traceData)
		var exact, missing uint64
		for _, event := range trace.Events {
			if event.ScannerCheckpoint.State == "exact" || event.ScannerCheckpoint.State == "exact_empty" {
				exact++
			} else {
				missing++
			}
		}
		gotTraceSHA := fmt.Sprintf("%x", traceSum)
		production := row.Production
		t.Logf("paired trace refresh fixture=%s sha=%s events=%d/%d aggregates=%+v audit=%+v", fixture.Fixture.ID, gotTraceSHA, trace.EventsSeen, trace.EventsDropped, trace.Aggregates, trace.CollisionAudit)
		if gotTraceSHA != production.TraceSHA256 || trace.EventsSeen != production.EventsSeen || trace.EventsDropped != production.EventsDropped ||
			trace.Aggregates.ActionLookupEvents != production.ActionLookups || trace.Aggregates.ActionExecutionEvents != production.ActionExecutions || trace.Aggregates.DecisionEvents != production.Decisions ||
			trace.Aggregates.ExecutionByActionType[0] != production.Shifts || trace.Aggregates.ExecutionByActionType[1] != production.Reductions || trace.Aggregates.ExecutionByActionType[2] != production.Accepts ||
			exact != production.ScannerExactRetained || missing != production.ScannerUnavailableRetained ||
			trace.CollisionAudit.ActionCellDistinctHashes != production.ActionCellDistinctHashes || trace.CollisionAudit.CoarseBoundaryDistinctHashes != production.BoundaryDistinctHashes ||
			trace.CollisionAudit.ActionCellAuditTruncated != production.ActionCellAuditTruncated || trace.CollisionAudit.CoarseBoundaryAuditTruncated != production.BoundaryAuditTruncated ||
			trace.CollisionAudit.Complete != production.CollisionAuditComplete {
			t.Errorf("%s production paired trace drifted: sha=%s exact/missing=%d/%d aggregates=%+v audit=%+v want=%+v", fixture.Fixture.ID, gotTraceSHA, exact, missing, trace.Aggregates, trace.CollisionAudit, production)
		}
		if production.ScannerExactRetained != 0 || production.ScannerUnavailableRetained != uint64(len(trace.Events)) {
			t.Fatalf("%s unexpectedly acquired a cross-engine scanner join: exact=%d unavailable=%d retained=%d", fixture.Fixture.ID, production.ScannerExactRetained, production.ScannerUnavailableRetained, len(trace.Events))
		}
		if row.Compact.Accepts != 1 || row.Compact.Dispatches == 0 || row.Compact.ActionLookups == 0 || row.Compact.Shifts == 0 || row.Compact.Reductions == 0 || row.Compact.EmittedPopPaths == 0 || row.Compact.EmittedPopPayloads == 0 {
			t.Fatalf("%s compact aggregate receipt is incomplete: %+v", fixture.Fixture.ID, row.Compact)
		}
	}
}

func TestDiagnosticSemanticPhaseTraceObserverEquality(t *testing.T) {
	fixtures, err := benchfixtures.LoadGoFullParseFixtures()
	if err != nil {
		t.Fatal(err)
	}
	if fixtures[0].Fixture.ID != "query_compile" {
		t.Fatalf("first canonical fixture=%q want query_compile", fixtures[0].Fixture.ID)
	}
	cases := []struct {
		name       string
		source     []byte
		wantDigest string
		wantGLR    bool
		want       semanticTraceReceipt
	}{
		{
			name: "query_compile", source: fixtures[0].Source, wantDigest: fixtures[0].Fixture.DeepTreeSHA256, wantGLR: true,
			want: semanticTraceReceipt{eventsSeen: 83689, retained: 8192, dropped: 75497, lookups: 1797, executions: 3239, decisions: 3156, unique: 749, repeated: 1048, maxMultiplicity: 4},
		},
		{
			name: "straight_lr", source: loadWorkCountAttributionFixture(t, straightLRFixturePath, straightLRFixtureBytes, straightLRFixtureSHA256),
			want: semanticTraceReceipt{eventsSeen: 193, retained: 193, lookups: 61, executions: 130, decisions: 2, unique: 61, maxMultiplicity: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			offCounts, offDigest := parseSemanticPhaseTraceFixture(t, tc.source)
			onCounts, onDigest, trace := parseSemanticPhaseTraceFixtureObserved(t, tc.source)
			if offDigest != onDigest {
				t.Fatalf("observer changed tree digest: off=%s on=%s", offDigest, onDigest)
			}
			if tc.wantDigest != "" && onDigest != tc.wantDigest {
				t.Fatalf("tree digest=%s want frozen=%s", onDigest, tc.wantDigest)
			}
			if !reflect.DeepEqual(offCounts, onCounts) {
				t.Fatal("semantic trace changed diagnostic work counts")
			}
			if trace.Contract != gotreesitter.DiagnosticSemanticPhaseTraceContract || trace.MaxEvents != gotreesitter.DiagnosticSemanticPhaseTraceMaxEvents {
				t.Fatalf("trace contract=%q max=%d", trace.Contract, trace.MaxEvents)
			}
			if trace.EventsSeen == 0 || len(trace.Events) == 0 || trace.ArithmeticOverflow {
				t.Fatalf("trace events_seen=%d retained=%d overflow=%v", trace.EventsSeen, len(trace.Events), trace.ArithmeticOverflow)
			}
			if !trace.CollisionAudit.Complete || trace.CollisionAudit.ActionCellHashCollisions != 0 || trace.CollisionAudit.CoarseBoundaryHashCollisions != 0 {
				t.Fatalf("semantic trace collision audit failed: %+v", trace.CollisionAudit)
			}
			var lookupEvents, executionEvents, executionUnknownOrdinal, decisionEvents, convergenceDecisions int
			lookupCounts := make(map[coarseLookupClass]int)
			executionPhases := make(map[string]int)
			decisionPhases := make(map[string]int)
			for _, event := range trace.Events {
				switch event.Kind {
				case "action_lookup":
					lookupEvents++
					lookupCounts[coarseLookupClass{
						token: event.TokenOrdinal, byteOffset: event.ByteOffset, state: event.State,
						lookahead: event.LookaheadSymbol, cell: event.LookupActionCellFingerprint,
						boundary: event.CoarseBoundaryClass, ordinal: event.ActionOrdinal,
					}]++
					if event.LookupActionCellFingerprint == 0 || event.ExecutionActionCellFingerprint != 0 || event.ExecutionCellRecomputed || event.CoarseBoundaryClass == 0 || event.ActionOrdinal < -1 || event.ActionType < -1 {
						t.Fatalf("invalid lookup event: %+v", event)
					}
				case "action_execution":
					executionEvents++
					executionPhases[event.Phase]++
					if event.ActionOrdinal < 0 {
						executionUnknownOrdinal++
					}
					if event.ExecutionActionCellFingerprint == 0 || event.LookupActionCellFingerprint != 0 || !event.ExecutionCellRecomputed || event.CoarseBoundaryClass == 0 || event.ActionOrdinal < -1 || event.ActionType < 0 {
						t.Fatalf("invalid execution event: %+v", event)
					}
				case "decision":
					decisionEvents++
					decisionPhases[event.Phase+"/"+event.Outcome]++
					if event.Phase != "final_select" && event.Phase != "terminal_accept" {
						convergenceDecisions++
					}
				default:
					t.Fatalf("unknown event kind %q", event.Kind)
				}
			}
			if lookupEvents == 0 || executionEvents == 0 {
				t.Fatalf("trace retained lookups=%d executions=%d", lookupEvents, executionEvents)
			}
			if tc.wantGLR && decisionEvents == 0 {
				t.Fatal("GLR fixture retained no post-reduction decisions")
			}
			if tc.wantGLR && executionPhases["extra-shift"] == 0 {
				t.Fatal("GLR fixture retained no extra-shift fast-path execution")
			}
			if !tc.wantGLR && convergenceDecisions != 0 {
				t.Fatalf("straight-LR control nonterminal convergence decisions=%d", convergenceDecisions)
			}
			uniqueClasses, repeatedClassEvents, maxClassMultiplicity := len(lookupCounts), 0, 0
			for _, count := range lookupCounts {
				if count > 1 {
					repeatedClassEvents += count - 1
				}
				if count > maxClassMultiplicity {
					maxClassMultiplicity = count
				}
			}
			gotReceipt := semanticTraceReceipt{
				eventsSeen: trace.EventsSeen, retained: len(trace.Events), dropped: trace.EventsDropped,
				lookups: lookupEvents, executions: executionEvents, decisions: decisionEvents,
				unique: uniqueClasses, repeated: repeatedClassEvents, maxMultiplicity: maxClassMultiplicity,
			}
			if gotReceipt != tc.want {
				t.Fatalf("trace receipt=%+v want=%+v", gotReceipt, tc.want)
			}
			if executionUnknownOrdinal != 0 {
				t.Fatalf("execution events with unknown action ordinal=%d", executionUnknownOrdinal)
			}
			if trace.Aggregates.ActionLookupEvents+trace.Aggregates.ActionExecutionEvents+trace.Aggregates.DecisionEvents != trace.EventsSeen ||
				trace.Aggregates.ActionLookupEvents < uint64(lookupEvents) || trace.Aggregates.ActionExecutionEvents < uint64(executionEvents) || trace.Aggregates.DecisionEvents < uint64(decisionEvents) {
				t.Fatalf("whole-parse aggregates do not cover retained prefix: aggregates=%+v retained=%d/%d/%d seen=%d", trace.Aggregates, lookupEvents, executionEvents, decisionEvents, trace.EventsSeen)
			}
			t.Logf("semantic_phase_trace fixture=%s events_seen=%d retained=%d dropped=%d lookups=%d executions=%d execution_unknown_ordinal=%d execution_phases=%v unique_coarse_classes=%d repeated_coarse_class_events=%d max_coarse_class_multiplicity=%d decisions=%d decision_phases=%v digest=%s", tc.name, trace.EventsSeen, len(trace.Events), trace.EventsDropped, lookupEvents, executionEvents, executionUnknownOrdinal, executionPhases, uniqueClasses, repeatedClassEvents, maxClassMultiplicity, decisionEvents, decisionPhases, onDigest)
		})
	}
}

type semanticTraceReceipt struct {
	eventsSeen      uint64
	retained        int
	dropped         uint64
	lookups         int
	executions      int
	decisions       int
	unique          int
	repeated        int
	maxMultiplicity int
}

type coarseLookupClass struct {
	token      uint64
	byteOffset uint32
	state      uint32
	lookahead  uint32
	cell       uint64
	boundary   uint64
	ordinal    int16
}

func TestDiagnosticSemanticPhaseTraceContractIdentity(t *testing.T) {
	files := map[string]string{
		"cgo_harness/work_count/semantic_phase_trace_contract_v4.json":  "cbe46fcf6f41da65f44d7191456fae86d5c8b9f845fd516fc3111b3a62095076",
		"cgo_harness/work_count/semantic_phase_trace_contract_v5.json":  "dafbe93d17e9ceee9ef5a015440a8452c981eaaf22e794cff2069bcefdc89f8d",
		"cgo_harness/work_count/paired_semantic_trace_contract_v1.json": "1ca1b5822396b995c0b3d0760c011daa6dcf5b4e91977a449d10490899c9ccac",
	}
	for path, wantSHA256 := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != wantSHA256 {
			t.Fatalf("%s sha256=%s want=%s", path, got, wantSHA256)
		}
	}
}

func parseSemanticPhaseTraceFixture(t *testing.T, source []byte) (gotreesitter.DiagnosticWorkCount, string) {
	t.Helper()
	parser := gotreesitter.NewParser(grammars.GoLanguage())
	gotreesitter.BeginDiagnosticWorkCount()
	tree, err := parser.Parse(source)
	counts := gotreesitter.EndDiagnosticWorkCount()
	if err != nil {
		if tree != nil {
			tree.Release()
		}
		t.Fatal(err)
	}
	defer tree.Release()
	inspection, err := benchfixtures.InspectGoTree(tree.RootNode(), grammars.GoLanguage())
	if err != nil {
		t.Fatal(err)
	}
	return counts, inspection.SHA256
}

func parseSemanticPhaseTraceFixtureObserved(t *testing.T, source []byte) (gotreesitter.DiagnosticWorkCount, string, gotreesitter.DiagnosticSemanticPhaseTrace) {
	t.Helper()
	parser := gotreesitter.NewParser(grammars.GoLanguage())
	gotreesitter.BeginDiagnosticWorkCount()
	gotreesitter.BeginDiagnosticSemanticPhaseTrace()
	tree, err := parser.Parse(source)
	trace := gotreesitter.EndDiagnosticSemanticPhaseTrace()
	counts := gotreesitter.EndDiagnosticWorkCount()
	if err != nil {
		if tree != nil {
			tree.Release()
		}
		t.Fatal(err)
	}
	defer tree.Release()
	inspection, err := benchfixtures.InspectGoTree(tree.RootNode(), grammars.GoLanguage())
	if err != nil {
		t.Fatal(err)
	}
	return counts, inspection.SHA256, trace
}
