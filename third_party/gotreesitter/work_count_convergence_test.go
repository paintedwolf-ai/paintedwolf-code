//go:build gts_workcount

package gotreesitter

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"testing"
)

const workCountContractV2SHA256 = "a438ddae8eaaf8c343faeb5efccdc15035911ee4bf7d22e627091d123a9ff46d"
const workCountContractV3SHA256 = "a4f28e92b4916277c56683b39a9e8da7358a9c8dd824b08f7f2be470e0daadde"

func TestWorkCountContractV3Stability(t *testing.T) {
	v2 := readWorkCountContract(t, "cgo_harness/work_count/contract_v2.json")
	if got := fmt.Sprintf("%x", sha256.Sum256(v2)); got != workCountContractV2SHA256 {
		t.Fatalf("contract v2 changed: sha256=%s want=%s", got, workCountContractV2SHA256)
	}
	v3 := readWorkCountContract(t, "cgo_harness/work_count/contract_v3.json")
	if got := fmt.Sprintf("%x", sha256.Sum256(v3)); got != workCountContractV3SHA256 {
		t.Fatalf("contract v3 changed: sha256=%s want=%s", got, workCountContractV3SHA256)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(v3, &raw); err != nil {
		t.Fatal(err)
	}
	wantTop := []string{"schema", "counter_contract", "scope", "historical_compatibility", "direct", "terminal", "proxy", "derived", "attempt_attribution", "convergence_frontier", "admission"}
	if len(raw) != len(wantTop) {
		t.Fatalf("contract v3 top-level keys=%d want=%d", len(raw), len(wantTop))
	}
	for _, key := range wantTop {
		if _, ok := raw[key]; !ok {
			t.Fatalf("contract v3 missing %q", key)
		}
	}
	requireExactJSONKeys(t, "historical_compatibility", raw["historical_compatibility"], []string{
		"v2_manifest", "direct_and_proxy_counter_semantics", "detailed_convergence",
	})
	requireExactJSONKeys(t, "convergence_frontier", raw["convergence_frontier"], []string{
		"status", "capability", "static_c_emits_comparable_events", "max_events", "max_packed_path_count",
		"packed_path_parse_work_budget", "packed_path_per_walk_work_budget", "packed_path_depth_budget",
		"record_first_reject_per_reason", "first_reject_semantics", "decision_ids", "head_status_priority", "phases", "phase_outcomes", "outcomes", "reject_reasons", "identity",
		"snapshots", "event_invariants", "candidate_count_units", "bounded_trace", "separate_integrity_signals",
		"include_saturating_aggregates", "terminal_counters", "interpretation",
	})
	requireExactJSONKeys(t, "admission", raw["admission"], []string{
		"digest_format", "fixture", "require_uninstrumented_go_static_c_digest_match_first",
		"require_instrumented_digest_match", "require_full_span", "require_clean_root", "require_no_overflow",
		"require_exact_fields", "require_ordinary_untagged_go_child_first", "require_clean_git_source_for_authoritative_receipt",
		"require_private_content_addressed_go_source_snapshot", "require_private_c_input_snapshot",
		"sanitize_go_and_parser_environment", "atomic_receipt_publish",
	})
	var admission map[string]any
	if err := json.Unmarshal(raw["admission"], &admission); err != nil {
		t.Fatal(err)
	}
	if admission["digest_format"] != "gts-deep-tree-v1" || admission["fixture"] != "query_compile" {
		t.Fatalf("admission identity=%v/%v", admission["digest_format"], admission["fixture"])
	}
	for key, value := range admission {
		if len(key) > len("require_") && key[:len("require_")] == "require_" && value != true {
			t.Fatalf("admission %s=%v want true", key, value)
		}
	}
	for _, key := range []string{"sanitize_go_and_parser_environment", "atomic_receipt_publish"} {
		if admission[key] != true {
			t.Fatalf("admission %s=%v want true", key, admission[key])
		}
	}
	var manifest struct {
		Schema          string `json:"schema"`
		CounterContract string `json:"counter_contract"`
		Historical      struct {
			Detailed string `json:"detailed_convergence"`
		} `json:"historical_compatibility"`
		Convergence struct {
			Status               string              `json:"status"`
			Capability           string              `json:"capability"`
			StaticCComparable    bool                `json:"static_c_emits_comparable_events"`
			MaxEvents            int                 `json:"max_events"`
			MaxPackedPaths       int                 `json:"max_packed_path_count"`
			ParseWorkBudget      int                 `json:"packed_path_parse_work_budget"`
			PerWalkWorkBudget    int                 `json:"packed_path_per_walk_work_budget"`
			DepthBudget          int                 `json:"packed_path_depth_budget"`
			RecordFirstReject    bool                `json:"record_first_reject_per_reason"`
			FirstRejectSemantics string              `json:"first_reject_semantics"`
			HeadStatusPriority   []string            `json:"head_status_priority"`
			Phases               []string            `json:"phases"`
			PhaseOutcomes        map[string][]string `json:"phase_outcomes"`
			Outcomes             []string            `json:"outcomes"`
			Reasons              []string            `json:"reject_reasons"`
			Identity             []string            `json:"identity"`
			BoundedTrace         string              `json:"bounded_trace"`
			CandidateCountUnits  string              `json:"candidate_count_units"`
		} `json:"convergence_frontier"`
	}
	if err := json.Unmarshal(v3, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != "gts-work-count-contract/v3" || manifest.CounterContract != DiagnosticWorkCountContract {
		t.Fatalf("contract identity=%q/%q", manifest.Schema, manifest.CounterContract)
	}
	convergence := manifest.Convergence
	if convergence.Status != "implemented" || convergence.Capability != workCountConvergenceCapability || convergence.StaticCComparable {
		t.Fatalf("convergence capability=%q/%q static_c=%v", convergence.Status, convergence.Capability, convergence.StaticCComparable)
	}
	if convergence.MaxEvents != workCountConvergenceMaxEvents || convergence.MaxPackedPaths != workCountConvergenceMaxPackedPaths ||
		convergence.ParseWorkBudget != workCountConvergencePathParseBudget || convergence.PerWalkWorkBudget != workCountConvergencePathWalkBudget ||
		convergence.DepthBudget != workCountConvergencePathDepthBudget || !convergence.RecordFirstReject {
		t.Fatalf("convergence bounds=%+v", convergence)
	}
	requireStringSliceEqual(t, "phases", convergence.Phases, workCountConvergencePhases[:])
	requireStringSliceEqual(t, "outcomes", convergence.Outcomes, workCountConvergenceOutcomes[:])
	requireStringSliceEqual(t, "reject reasons", convergence.Reasons, workCountConvergenceRejectReasons[:])
	wantPhaseOutcomes := map[string][]string{
		workCountConvergencePhaseReduceWindowSelect:  {workCountConvergenceOutcomeCandidate, workCountConvergenceOutcomeCapHit},
		workCountConvergencePhasePostReducePrimary:   {workCountConvergenceOutcomeCandidate, workCountConvergenceOutcomePacked, workCountConvergenceOutcomeRejected, workCountConvergenceOutcomeMutationPartial},
		workCountConvergencePhasePostReducePending:   {workCountConvergenceOutcomeCandidate, workCountConvergenceOutcomePacked, workCountConvergenceOutcomeRejected, workCountConvergenceOutcomeMutationPartial},
		workCountConvergencePhaseBoundaryGSS:         {workCountConvergenceOutcomeCandidate, workCountConvergenceOutcomePacked, workCountConvergenceOutcomeRejected, workCountConvergenceOutcomeMutationPartial},
		workCountConvergencePhaseBoundaryEquivalence: {workCountConvergenceOutcomeRejected},
		workCountConvergencePhaseBoundaryCull:        {workCountConvergenceOutcomeRejected},
		workCountConvergencePhasePendingWork:         {workCountConvergenceOutcomePendingQueued, workCountConvergenceOutcomePendingDrained, workCountConvergenceOutcomePendingDiscarded},
		workCountConvergencePhaseFinalExpand:         {workCountConvergenceOutcomePackedRootEmitted, workCountConvergenceOutcomeObservationUnknown},
		workCountConvergencePhaseFinalSelect:         {workCountConvergenceOutcomeSelected},
		workCountConvergencePhaseTerminalAccept:      {workCountConvergenceOutcomeAcceptedHead},
	}
	if !reflect.DeepEqual(convergence.PhaseOutcomes, wantPhaseOutcomes) {
		t.Fatalf("phase/outcome matrix=%v want=%v", convergence.PhaseOutcomes, wantPhaseOutcomes)
	}
	for _, required := range []string{"decision_id", "lookahead_present", "election_scanner_comparable", "election_scanner_checkpoint_presence_and_hash"} {
		if !workCountConvergenceStringIn(required, convergence.Identity) {
			t.Fatalf("contract identity misses %q", required)
		}
	}
	if convergence.BoundedTrace == "" || convergence.CandidateCountUnits == "" || convergence.FirstRejectSemantics == "" || !reflect.DeepEqual(convergence.HeadStatusPriority, []string{"dead", "accepted", "paused", "live"}) || manifest.Historical.Detailed == "" {
		t.Fatal("v3 compatibility/boundedness prose is empty")
	}
}

func TestWorkCountConvergenceRetainsScorePrefilterDecision(t *testing.T) {
	var nodes gssScratch
	node := NewLeafNode(11, true, 0, 5, Point{}, Point{Column: 5})
	entries := []stackEntry{{state: 1}, newStackEntryNode(7, node)}
	makeStack := func(score int, paused bool) glrStack {
		return glrStack{
			gss:        buildGSSStack(entries, &nodes),
			byteOffset: 5,
			score:      score,
			cPaused:    paused,
		}
	}
	left := makeStack(1, false)
	right := makeStack(2, true)
	if !stacksHeaderEquivalent(&left, &right) {
		t.Fatal("test setup did not produce same-header stacks")
	}
	if leftCost, rightCost := cStackErrorCostForMerge(nil, &left), cStackErrorCostForMerge(nil, &right); leftCost == rightCost {
		t.Fatalf("test setup recovery costs equal: left=%d right=%d", leftCost, rightCost)
	}
	result := []glrStack{left}
	leftHead := left.gss.head
	leftLinks := leftHead.linkCount()
	scratch := glrMergeScratch{cRecoveryCost: true}

	token := beginWorkCountConvergenceAttempt(t)
	merged, attempted := tryGSSMainMergeResult(&scratch, result, 0, &right)
	counts := endWorkCountConvergenceAttempt(t, token)
	if merged || attempted {
		t.Fatalf("distinct-score GSS merge = merged:%v attempted:%v, want false/false", merged, attempted)
	}
	if result[0].gss.head != leftHead || leftHead.linkCount() != leftLinks {
		t.Fatal("distinct-score GSS prefilter mutated the retained stack")
	}
	if counts.MergeAttemptsProxy != 1 {
		t.Fatalf("merge front-door calls = %d, want 1", counts.MergeAttemptsProxy)
	}
	if len(counts.Convergence.Events) != 2 {
		t.Fatalf("retained convergence events = %d, want candidate+rejection", len(counts.Convergence.Events))
	}
	candidate, rejected := counts.Convergence.Events[0], counts.Convergence.Events[1]
	if candidate.Outcome != workCountConvergenceOutcomeCandidate || rejected.Outcome != workCountConvergenceOutcomeRejected ||
		rejected.RejectReason != workCountConvergenceReasonScore || candidate.DecisionID != rejected.DecisionID {
		t.Fatalf("score prefilter events = candidate:%+v rejected:%+v", candidate, rejected)
	}
}

func TestWorkCountConvergenceRetainsFirstRejectBeyondEventCap(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	for i := 0; i < workCountConvergenceMaxEvents; i++ {
		workCountSetConvergenceIteration(i)
		workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryEquivalence, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", nil, nil, 0, 0, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	}
	workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryCull, workCountConvergenceOutcomeRejected, workCountConvergenceReasonCull, "", nil, nil, 1, 0, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	counts := endWorkCountConvergenceAttempt(t, token)

	if len(counts.Convergence.Events) != workCountConvergenceMaxEvents || !counts.Convergence.EventTruncated || counts.Convergence.ArithmeticOverflow {
		t.Fatalf("event bound=%d truncated=%v overflow=%v", len(counts.Convergence.Events), counts.Convergence.EventTruncated, counts.Convergence.ArithmeticOverflow)
	}
	if counts.Convergence.EventsSeen != workCountConvergenceMaxEvents+1 || len(counts.Convergence.FirstRejects) != 1 {
		t.Fatalf("seen=%d first_rejects=%d", counts.Convergence.EventsSeen, len(counts.Convergence.FirstRejects))
	}
	first := counts.Convergence.FirstRejects[0]
	if first.Sequence != workCountConvergenceMaxEvents+1 || first.RejectReason != workCountConvergenceReasonCull {
		t.Fatalf("late first reject=%+v", first)
	}
}

func TestWorkCountConvergenceSkipsFullSnapshotsBeyondEventCap(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	for i := 0; i < workCountConvergenceMaxEvents; i++ {
		workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryEquivalence, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", nil, nil, 2, 2, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	}
	target := newGLRStack(1)
	candidate := newGLRStack(1)
	workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryEquivalence, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", &target, &candidate, 2, 2, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	if activeWorkCountConvergence.nextDecision != 0 {
		t.Fatalf("post-cap scalar event allocated decision ID %d", activeWorkCountConvergence.nextDecision)
	}
	workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryGSS, workCountConvergenceOutcomeRejected, workCountConvergenceReasonStatus, "", &target, &candidate, 2, 2, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	counts := endWorkCountConvergenceAttempt(t, token)
	if len(counts.Convergence.Events) != workCountConvergenceMaxEvents || len(counts.Convergence.FirstRejects) != 1 {
		t.Fatalf("retained events=%d first_rejects=%d", len(counts.Convergence.Events), len(counts.Convergence.FirstRejects))
	}
	if got := counts.Convergence.FirstRejects[0].DecisionID; got != 1 {
		t.Fatalf("first-reject decision ID=%d want 1", got)
	}
	if counts.Convergence.Aggregates.Outcomes.Candidate != workCountConvergenceMaxEvents+1 || counts.Convergence.Aggregates.Outcomes.Rejected != 1 {
		t.Fatalf("post-cap scalar aggregates=%+v", counts.Convergence.Aggregates.Outcomes)
	}
}

func TestWorkCountConvergenceBoundsMergeDetailBeyondEventCap(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	for i := 0; i < workCountConvergenceMaxEvents; i++ {
		workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryEquivalence, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", nil, nil, 0, 0, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	}
	if workCountConvergenceRetainsDetail(workCountConvergenceReasonNone) {
		t.Fatal("post-cap non-rejection traffic retained formatted detail")
	}
	if !workCountConvergenceRetainsDetail(workCountConvergenceReasonPreflight) {
		t.Fatal("first post-cap rejection did not retain detail")
	}
	workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryGSS, workCountConvergenceOutcomeRejected, workCountConvergenceReasonPreflight, "first preflight rejection", nil, nil, 2, 2, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	if workCountConvergenceRetainsDetail(workCountConvergenceReasonPreflight) {
		t.Fatal("repeated post-cap rejection retained formatted detail")
	}
	_ = endWorkCountConvergenceAttempt(t, token)
}

func TestStopFrontierSummaryDoesNotRecordConvergenceEvents(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	parser := &Parser{language: &Language{}}
	stacks := []glrStack{newGLRStack(1), newGLRStack(1)}
	_ = parser.stopFrontierSameHeaderSummary(stacks)
	counts := endWorkCountConvergenceAttempt(t, token)
	if counts.Convergence.EventsSeen != 0 || len(counts.Convergence.Events) != 0 {
		t.Fatalf("stop-frontier diagnostic polluted convergence ledger: seen=%d retained=%d", counts.Convergence.EventsSeen, len(counts.Convergence.Events))
	}
}

func TestWorkCountConvergenceAttemptIDsResetAndCorrelate(t *testing.T) {
	BeginDiagnosticWorkCount()
	target := newGLRStack(1)
	candidate := newGLRStack(1)
	first := beginWorkCountConvergenceAttemptWithoutBegin(t)
	for _, outcome := range []string{workCountConvergenceOutcomeCandidate, workCountConvergenceOutcomePacked} {
		workCountRecordConvergence(nil, workCountConvergencePhasePostReducePrimary, outcome, workCountConvergenceReasonNone, "", &target, &candidate, 2, 1, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	}
	candidate.branchOrder++
	workCountRecordConvergence(nil, workCountConvergencePhasePostReducePrimary, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", &target, &candidate, 1, 1, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	finalizeWorkCountConvergenceAttempt(first)
	second := beginWorkCountConvergenceAttemptWithoutBegin(t)
	workCountRecordConvergence(nil, workCountConvergencePhasePostReducePrimary, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", &target, &candidate, 1, 1, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	finalizeWorkCountConvergenceAttempt(second)
	counts := EndDiagnosticWorkCount()

	if got := []uint32{counts.Convergence.Events[0].DecisionID, counts.Convergence.Events[1].DecisionID, counts.Convergence.Events[2].DecisionID, counts.Convergence.Events[3].DecisionID}; !reflect.DeepEqual(got, []uint32{1, 1, 2, 1}) {
		t.Fatalf("decision IDs=%v want [1 1 2 1]", got)
	}
	if counts.Convergence.Events[0].Attempt != 1 || counts.Convergence.Events[3].Attempt != 2 {
		t.Fatalf("attempt attribution=%d/%d", counts.Convergence.Events[0].Attempt, counts.Convergence.Events[3].Attempt)
	}
}

func TestWorkCountConvergenceOverflowIsNotTruncation(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	activeDiagnosticWorkCount.Convergence.Aggregates.Outcomes.Candidate = math.MaxUint64
	workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryEquivalence, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", nil, nil, 0, 0, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	counts := endWorkCountConvergenceAttempt(t, token)
	if !counts.Overflow || !counts.Convergence.ArithmeticOverflow || counts.Convergence.EventTruncated {
		t.Fatalf("overflow=%v convergence_overflow=%v truncated=%v", counts.Overflow, counts.Convergence.ArithmeticOverflow, counts.Convergence.EventTruncated)
	}
}

func TestWorkCountConvergencePopulationEventHasNoDecisionID(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryCull, workCountConvergenceOutcomeRejected, workCountConvergenceReasonCull, "", nil, nil, 4, 2, workCountConvergenceShape{}, workCountConvergenceShape{}, false, true)
	counts := endWorkCountConvergenceAttempt(t, token)
	if got := counts.Convergence.Events[0].DecisionID; got != 0 {
		t.Fatalf("population-only decision ID=%d want 0", got)
	}
}

func TestWorkCountConvergenceDistinguishesUnsetAndSyntheticLookahead(t *testing.T) {
	BeginDiagnosticWorkCount()
	first := beginWorkCountConvergenceAttemptWithoutBegin(t)
	workCountSetConvergenceLookahead(Token{NoLookahead: true})
	workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryEquivalence, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", nil, nil, 0, 0, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	finalizeWorkCountConvergenceAttempt(first)
	second := beginWorkCountConvergenceAttemptWithoutBegin(t)
	workCountRecordConvergence(nil, workCountConvergencePhaseBoundaryEquivalence, workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", nil, nil, 0, 0, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	finalizeWorkCountConvergenceAttempt(second)
	counts := EndDiagnosticWorkCount()
	synthetic, unset := counts.Convergence.Events[0], counts.Convergence.Events[1]
	if unset.LookaheadPresent || unset.LookaheadNoLookahead {
		t.Fatalf("unset lookahead=%+v", unset)
	}
	if !synthetic.LookaheadPresent || !synthetic.LookaheadNoLookahead {
		t.Fatalf("synthetic lookahead=%+v", synthetic)
	}
}

func TestWorkCountConvergenceScannerIdentityBelongsToCurrentElection(t *testing.T) {
	t.Run("ordinary_language", func(t *testing.T) {
		token := beginWorkCountConvergenceAttempt(t)
		parser := &Parser{language: &Language{}}
		workCountRecordConvergence(parser, workCountConvergencePhaseBoundaryCull, workCountConvergenceOutcomeRejected, workCountConvergenceReasonCull, "", nil, nil, 2, 1, workCountConvergenceShape{}, workCountConvergenceShape{}, false, true)
		event := endWorkCountConvergenceAttempt(t, token).Convergence.Events[0]
		if !event.ElectionScannerComparable || event.ElectionScannerCheckpointPresent || event.ElectionScannerCheckpointHash != 0 {
			t.Fatalf("ordinary election scanner identity=%+v", event)
		}
	})
	t.Run("external_checkpoint_present", func(t *testing.T) {
		token := beginWorkCountConvergenceAttempt(t)
		checkpoint := externalScannerCheckpoint{start: []byte{1, 2}, end: []byte{3, 4}}
		parser := &Parser{language: &Language{ExternalScanner: &mockExternalScanner{}}, currentExternalTokenCheckpoint: checkpoint, currentExternalTokenCheckpointValid: true}
		workCountRecordConvergence(parser, workCountConvergencePhaseBoundaryCull, workCountConvergenceOutcomeRejected, workCountConvergenceReasonCull, "", nil, nil, 2, 1, workCountConvergenceShape{}, workCountConvergenceShape{}, false, true)
		event := endWorkCountConvergenceAttempt(t, token).Convergence.Events[0]
		if !event.ElectionScannerComparable || !event.ElectionScannerCheckpointPresent || event.ElectionScannerCheckpointHash != workCountCheckpointHash(checkpoint) {
			t.Fatalf("checkpoint election scanner identity=%+v", event)
		}
	})
	t.Run("external_checkpoint_absent", func(t *testing.T) {
		token := beginWorkCountConvergenceAttempt(t)
		parser := &Parser{language: &Language{ExternalScanner: &mockExternalScanner{}}}
		workCountRecordConvergence(parser, workCountConvergencePhaseBoundaryCull, workCountConvergenceOutcomeRejected, workCountConvergenceReasonCull, "", nil, nil, 2, 1, workCountConvergenceShape{}, workCountConvergenceShape{}, false, true)
		event := endWorkCountConvergenceAttempt(t, token).Convergence.Events[0]
		if event.ElectionScannerComparable || event.ElectionScannerCheckpointPresent || event.ElectionScannerCheckpointHash != 0 {
			t.Fatalf("absent checkpoint election scanner identity=%+v", event)
		}
	})
}

func TestWorkCountConvergencePendingAndFinalAggregates(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	shape := workCountConvergenceShape{Links: 2, PackedPaths: 3}
	workCountRecordConvergence(nil, workCountConvergencePhasePendingWork, workCountConvergenceOutcomePendingQueued, workCountConvergenceReasonNone, "", nil, nil, 0, 1, shape, shape, false, false)
	workCountRecordConvergence(nil, workCountConvergencePhasePendingWork, workCountConvergenceOutcomePendingDiscarded, workCountConvergenceReasonNone, "", nil, nil, 1, 0, shape, shape, false, false)
	workCountRecordConvergence(nil, workCountConvergencePhaseFinalExpand, workCountConvergenceOutcomePackedRootEmitted, workCountConvergenceReasonNone, "", nil, nil, 0, 0, workCountConvergenceShape{PackedPaths: 4}, workCountConvergenceShape{PackedPaths: 3}, false, true)
	workCountRecordConvergence(nil, workCountConvergencePhaseFinalSelect, workCountConvergenceOutcomeSelected, workCountConvergenceReasonNone, "", nil, nil, 3, 1, shape, shape, false, false)
	counts := endWorkCountConvergenceAttempt(t, token)
	totals := counts.Convergence.Aggregates
	if totals.Outcomes.PendingQueued != 1 || totals.Outcomes.PendingDiscarded != 1 || totals.Outcomes.Selected != 1 || totals.PackedRoots != 3 || totals.CapHits != 1 {
		t.Fatalf("pending/final totals=%+v", totals)
	}
	if totals.CandidateCountBeforeTotal != 4 || totals.CandidateCountAfterTotal != 2 {
		t.Fatalf("phase-local candidate counts=%d/%d", totals.CandidateCountBeforeTotal, totals.CandidateCountAfterTotal)
	}
	if counts.Convergence.VocabularyViolation {
		t.Fatal("valid pending/final events triggered a vocabulary violation")
	}
}

func TestWorkCountConvergencePathWalkIsParseBounded(t *testing.T) {
	BeginDiagnosticWorkCount()
	t.Cleanup(func() {
		activeDiagnosticWorkCount = nil
		workCountEndConvergence()
	})
	activeWorkCountConvergence.pathWorkRemaining = 2
	stack := glrStack{gss: gssStack{head: &gssNode{prev: &gssNode{prev: &gssNode{}}}}}
	shape := workCountConvergenceShapeOf(&stack)
	if !shape.PathCountUnknown || shape.PathCountCapped || activeDiagnosticWorkCount.Convergence.PathWalkWorkUsed != 2 || !activeDiagnosticWorkCount.Convergence.PathWalkWorkExhausted {
		t.Fatalf("bounded shape=%+v used=%d exhausted=%v", shape, activeDiagnosticWorkCount.Convergence.PathWalkWorkUsed, activeDiagnosticWorkCount.Convergence.PathWalkWorkExhausted)
	}
	beforeUsed := activeDiagnosticWorkCount.Convergence.PathWalkWorkUsed
	_ = workCountConvergenceShapeOf(&stack)
	if activeDiagnosticWorkCount.Convergence.PathWalkWorkUsed != beforeUsed {
		t.Fatal("exhausted parse-global path budget performed more work")
	}
	activeDiagnosticWorkCount = nil
	workCountEndConvergence()
}

func TestWorkCountConvergencePackedExpansionProvesCapHit(t *testing.T) {
	leaf := &gssNode{}
	branch := &gssNode{prev: leaf}
	branch.appendExtraLink(gssMainLink{prev: leaf})
	head := &gssNode{prev: branch}
	for i := 0; i < 3; i++ {
		head.appendExtraLink(gssMainLink{prev: branch})
	}
	stack := glrStack{gss: gssStack{head: head}}
	BeginDiagnosticWorkCount()
	t.Cleanup(func() {
		activeDiagnosticWorkCount = nil
		workCountEndConvergence()
	})
	emitted, capHit, unknown := workCountConvergencePackedResultExpansion(&stack, maxStacksPerMergeKey)
	shape := workCountConvergenceShapeOf(&stack)
	if emitted != maxStacksPerMergeKey || !capHit || unknown || shape.PackedPaths != workCountConvergenceMaxPackedPaths || !shape.PathCountCapped || shape.PathCountUnknown {
		t.Fatalf("expansion emitted=%d cap=%v unknown=%v shape=%+v", emitted, capHit, unknown, shape)
	}
	activeDiagnosticWorkCount = nil
	workCountEndConvergence()
}

func TestWorkCountConvergenceLinkCountDoesNotUsePackedPathCap(t *testing.T) {
	links, capped := workCountBoundedLinkCount(workCountConvergenceMaxPackedPaths + 2)
	if links != workCountConvergenceMaxPackedPaths+2 || capped {
		t.Fatalf("link count=%d capped=%v want=%d,false", links, capped, workCountConvergenceMaxPackedPaths+2)
	}
	links, capped = workCountBoundedLinkCount(math.MaxUint16 + 1)
	if links != math.MaxUint16 || !capped {
		t.Fatalf("saturated link count=%d capped=%v", links, capped)
	}

	extraLinks := make([]gssMainLink, 8)
	headNode := &gssNode{extraLinks: &extraLinks[0], extraLinkCount: 8, extraLinkCap: 8}
	stack := glrStack{gss: gssStack{head: headNode}}
	BeginDiagnosticWorkCount()
	t.Cleanup(func() {
		activeDiagnosticWorkCount = nil
		workCountEndConvergence()
	})
	shape := workCountConvergenceShapeOf(&stack)
	head := workCountConvergenceHead(nil, &stack)
	if shape.Links != 9 || shape.LinkCountCapped || head.LinkCount != 9 || head.LinkCountCapped {
		t.Fatalf("nine-link integration shape=%+v head=%+v", shape, head)
	}
	activeDiagnosticWorkCount = nil
	workCountEndConvergence()
}

func TestWorkCountConvergenceNoCullEventWithoutRemoval(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	workCountRecordBoundaryCull(nil, 4, 4)
	workCountRecordBoundaryCull(nil, 4, 2)
	counts := endWorkCountConvergenceAttempt(t, token)
	if counts.Convergence.EventsSeen != 1 || len(counts.Convergence.Events) != 1 {
		t.Fatalf("cull events seen=%d retained=%d", counts.Convergence.EventsSeen, len(counts.Convergence.Events))
	}
	event := counts.Convergence.Events[0]
	if event.RejectReason != workCountConvergenceReasonCull || event.CandidateCountBefore != 4 || event.CandidateCountAfter != 2 || !event.CapHit {
		t.Fatalf("cull event=%+v", event)
	}
}

func TestWorkCountConvergenceUnknownFinalExpansionDoesNotClaimPackedRoot(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	stack := newGLRStack(1)
	workCountRecordFinalExpand(nil, &stack, 0, false, true)
	counts := endWorkCountConvergenceAttempt(t, token)
	if len(counts.Convergence.Events) != 1 || counts.Convergence.Events[0].Outcome != workCountConvergenceOutcomeObservationUnknown || !counts.Convergence.Events[0].SnapshotUnknown {
		t.Fatalf("unknown expansion events=%+v", counts.Convergence.Events)
	}
	totals := counts.Convergence.Aggregates
	if totals.Outcomes.ObservationUnknown != 1 || totals.Outcomes.PackedRoot != 0 || totals.PackedRoots != 0 || totals.CapHits != 0 {
		t.Fatalf("unknown expansion totals=%+v", totals)
	}
}

func TestWorkCountConvergenceFinalPendingDiscardBelongsToOneAttempt(t *testing.T) {
	BeginDiagnosticWorkCount()
	first := beginWorkCountConvergenceAttemptWithoutBegin(t)
	pending := []glrStack{newGLRStack(1)}
	workCountRecordFinalPendingDiscards(nil, pending, nil)
	finalizeWorkCountConvergenceAttempt(first)
	second := beginWorkCountConvergenceAttemptWithoutBegin(t)
	pending = pending[:0] // parse-attempt reset is cleanup, not a second event.
	finalizeWorkCountConvergenceAttempt(second)
	counts := EndDiagnosticWorkCount()
	if counts.Convergence.Aggregates.Outcomes.PendingDiscarded != 1 || len(counts.Convergence.Events) != 1 || counts.Convergence.Events[0].Attempt != 1 {
		t.Fatalf("pending discard ownership events=%+v totals=%+v", counts.Convergence.Events, counts.Convergence.Aggregates.Outcomes)
	}
}

func TestWorkCountConvergenceDoesNotAttributeInitialStalePending(t *testing.T) {
	parser := NewParser(buildArithmeticLanguage())
	parser.pendingForkStacks = []glrStack{newGLRStack(1)}
	parser.pendingFrontierForkStacks = []glrStack{newGLRStack(1)}
	BeginDiagnosticWorkCount()
	tree, err := parser.Parse([]byte("1+2"))
	counts := EndDiagnosticWorkCount()
	if err != nil {
		if tree != nil {
			tree.Release()
		}
		t.Fatal(err)
	}
	defer tree.Release()
	if counts.Convergence.Aggregates.Outcomes.PendingDiscarded != 0 {
		t.Fatalf("initial stale pending attributed to new attempt: %+v", counts.Convergence.Aggregates.Outcomes)
	}
}

func TestWorkCountConvergenceHeadWithParserClaimsReadOnlyErrorCost(t *testing.T) {
	stack := newGLRStack(1)
	parser := &Parser{}
	head := workCountConvergenceHead(parser, &stack)
	if !head.ErrorCostComparable || head.ErrorCost != 0 {
		t.Fatalf("head with parser did not claim read-only error cost: %+v", head)
	}
}

func TestWorkCountConvergenceHeadStatusUsesDeadAcceptedPausedPriority(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		dead, accepted, paused bool
		want                   string
	}{
		{name: "dead_over_all", dead: true, accepted: true, paused: true, want: "dead"},
		{name: "accepted_over_paused", accepted: true, paused: true, want: "accepted"},
		{name: "paused", paused: true, want: "paused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stack := newGLRStack(1)
			stack.dead, stack.accepted, stack.cPaused = tc.dead, tc.accepted, tc.paused
			if got := workCountConvergenceHead(nil, &stack).Status; got != tc.want {
				t.Fatalf("status=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestWorkCountConvergenceHeadErrorCostIsReadOnly(t *testing.T) {
	lang := &Language{SymbolMetadata: []SymbolMetadata{{}, {Visible: true}}}
	missing := &Node{symbol: 1, equivVersion: 1}
	missing.setMissing(true)
	headNode := &gssNode{
		entry: newStackEntryNode(1, missing), depth: 1,
		aggGen: 77, aggCost: 13, aggVis: 9, aggValid: gssAggCostValid | gssAggVisValid,
	}
	stack := glrStack{gss: gssStack{head: headNode}}
	sentinel := &gssNode{depth: 99}
	mergeScratch := &glrMergeScratch{cPrefixPath: []*gssNode{sentinel}}
	parser := &Parser{
		language:       lang,
		mergeScratch:   mergeScratch,
		cPrefixPath:    []*gssNode{sentinel},
		cNodeMemoEpoch: 4,
		cNodeMemoCache: make([]cNodeMemoCacheEntry, 4),
	}
	parser.cNodeMemoCache[0] = cNodeMemoCacheEntry{node: 17, epoch: 4, cost: 23, hasCost: true}
	prefixBefore := append([]*gssNode(nil), parser.cPrefixPath...)
	mergePrefixBefore := append([]*gssNode(nil), mergeScratch.cPrefixPath...)
	memoBefore := append([]cNodeMemoCacheEntry(nil), parser.cNodeMemoCache...)
	aggGen, aggCost, aggVis, aggValid := headNode.aggGen, headNode.aggCost, headNode.aggVis, headNode.aggValid

	head := workCountConvergenceHead(parser, &stack)
	if !head.ErrorCostComparable || head.ErrorCost == 0 {
		t.Fatalf("read-only head cost=%+v", head)
	}
	if headNode.aggGen != aggGen || headNode.aggCost != aggCost || headNode.aggVis != aggVis || headNode.aggValid != aggValid {
		t.Fatalf("head aggregate cache mutated: %+v", headNode)
	}
	if !reflect.DeepEqual(parser.cPrefixPath, prefixBefore) || !reflect.DeepEqual(mergeScratch.cPrefixPath, mergePrefixBefore) || !reflect.DeepEqual(parser.cNodeMemoCache, memoBefore) || parser.cNodeMemoEpoch != 4 {
		t.Fatal("head observation mutated parser prefix or node memo scratch")
	}
}

func TestWorkCountConvergenceMutationEpochTracksSemanticGSSWrites(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	node := &gssNode{}
	base := &gssNode{}
	entry := stackEntry{state: 1}
	before := workCountGSSMutationEpoch()
	setGSSMainLink(node, 0, base, entry)
	if got := workCountGSSMutationEpoch(); got != before+1 {
		t.Fatalf("primary set epoch=%d want=%d", got, before+1)
	}
	setGSSMainLink(node, 0, base, entry)
	if got := workCountGSSMutationEpoch(); got != before+1 {
		t.Fatalf("no-op primary set changed epoch=%d", got)
	}
	node.appendExtraLink(gssMainLink{prev: base, entry: entry})
	if got := workCountGSSMutationEpoch(); got != before+2 {
		t.Fatalf("append-grow epoch=%d want=%d", got, before+2)
	}
	setGSSMainLink(node, 1, &gssNode{}, stackEntry{state: 2})
	if got := workCountGSSMutationEpoch(); got != before+3 {
		t.Fatalf("extra set epoch=%d want=%d", got, before+3)
	}
	node.appendExtraLink(gssMainLink{prev: base, entry: stackEntry{state: 3}})
	if got := workCountGSSMutationEpoch(); got != before+4 {
		t.Fatalf("append-reuse epoch=%d want=%d", got, before+4)
	}
	_ = endWorkCountConvergenceAttempt(t, token)
}

func TestWorkCountConvergenceFailedMergeAfterSemanticMutationIsPartial(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	target, candidate := newGLRStack(1), newGLRStack(1)
	before := workCountConvergenceShapeOf(&target)
	epoch := workCountGSSMutationEpoch()
	workCountRecordGSSMutation()
	workCountRecordGSSMergeResult(nil, workCountConvergencePhaseBoundaryGSS, "test merge", &target, &candidate, before, epoch, false)
	counts := endWorkCountConvergenceAttempt(t, token)
	event := counts.Convergence.Events[0]
	if event.Outcome != workCountConvergenceOutcomeMutationPartial || !event.MutationPartial || event.RejectReason != "" {
		t.Fatalf("partial mutation event=%+v", event)
	}
}

func TestWorkCountConvergencePostReduceLifecycleStaysInOnePhase(t *testing.T) {
	t.Run("early_reject", func(t *testing.T) {
		token := beginWorkCountConvergenceAttempt(t)
		target, candidate := newGLRStack(1), newGLRStack(1)
		target.accepted = true
		reason := postReduceForkMergePreflight(&target, &candidate)
		workCountRecordPostReduceCandidate(nil, workCountConvergencePhasePostReducePrimary, "candidate", &target, &candidate, reason)
		counts := endWorkCountConvergenceAttempt(t, token)
		if len(counts.Convergence.Events) != 2 || counts.Convergence.Events[0].Outcome != workCountConvergenceOutcomeCandidate || counts.Convergence.Events[1].Outcome != workCountConvergenceOutcomeRejected {
			t.Fatalf("early lifecycle=%+v", counts.Convergence.Events)
		}
		for _, event := range counts.Convergence.Events {
			if event.Phase != workCountConvergencePhasePostReducePrimary {
				t.Fatalf("early lifecycle leaked phase=%q", event.Phase)
			}
		}
		if counts.Convergence.Aggregates.Phases.BoundaryGSS != 0 {
			t.Fatalf("early lifecycle emitted boundary GSS event")
		}
	})
	t.Run("success", func(t *testing.T) {
		token := beginWorkCountConvergenceAttempt(t)
		var scratch gssScratch
		base := scratch.allocNode(stackEntry{state: 1}, nil, 1)
		build := func() glrStack {
			return glrStack{gss: gssStack{head: scratch.allocNode(stackEntry{state: 2}, base, 2)}, byteOffset: 3}
		}
		target, candidate := build(), build()
		workCountRecordPostReduceCandidate(nil, workCountConvergencePhasePostReducePending, "candidate", &target, &candidate, workCountConvergenceReasonNone)
		if !workCountTryPostReduceMergeObserved(nil, workCountConvergencePhasePostReducePending, "merge", &target, &candidate) {
			t.Fatal("post-reduce merge declined")
		}
		counts := endWorkCountConvergenceAttempt(t, token)
		if len(counts.Convergence.Events) != 2 || counts.Convergence.Events[0].Outcome != workCountConvergenceOutcomeCandidate || counts.Convergence.Events[1].Outcome != workCountConvergenceOutcomePacked {
			t.Fatalf("success lifecycle=%+v", counts.Convergence.Events)
		}
		for _, event := range counts.Convergence.Events {
			if event.Phase != workCountConvergencePhasePostReducePending {
				t.Fatalf("success lifecycle leaked phase=%q", event.Phase)
			}
		}
		if counts.Convergence.Aggregates.Phases.BoundaryGSS != 0 {
			t.Fatalf("success lifecycle emitted boundary GSS event")
		}
	})
}

func TestWorkCountConvergenceClosedVocabularyAndPointerFreeJSON(t *testing.T) {
	token := beginWorkCountConvergenceAttempt(t)
	workCountRecordConvergence(nil, "not_a_phase", workCountConvergenceOutcomeCandidate, workCountConvergenceReasonNone, "", nil, nil, 0, 0, workCountConvergenceShape{}, workCountConvergenceShape{}, false, false)
	counts := endWorkCountConvergenceAttempt(t, token)
	if !counts.Convergence.VocabularyViolation || counts.Convergence.EventsSeen != 0 {
		t.Fatalf("vocabulary violation=%v events=%d", counts.Convergence.VocabularyViolation, counts.Convergence.EventsSeen)
	}
	assertNoPointerKinds(t, reflect.TypeOf(DiagnosticWorkCountConvergenceEvent{}))
	data, err := json.Marshal(counts.Convergence)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("0x")) || bytes.Contains(data, []byte("pointer")) || bytes.Contains(data, []byte("address")) {
		t.Fatalf("serialized convergence ledger exposes pointer material: %s", data)
	}
}

func beginWorkCountConvergenceAttempt(t *testing.T) workCountAttemptToken {
	t.Helper()
	BeginDiagnosticWorkCount()
	return beginWorkCountConvergenceAttemptWithoutBegin(t)
}

func beginWorkCountConvergenceAttemptWithoutBegin(t *testing.T) workCountAttemptToken {
	t.Helper()
	token := workCountBeginParseAttempt(6, 1024, 6)
	if token == 0 {
		t.Fatal("zero attempt token")
	}
	workCountResolveParseAttempt(token, 6, false, 6, 6, 1024, 1024)
	return token
}

func finalizeWorkCountConvergenceAttempt(token workCountAttemptToken) {
	workCountBeginFinalizeParseAttempt(token)
	workCountEndFinalizeParseAttempt(token, ParseStopAccepted, nil)
}

func endWorkCountConvergenceAttempt(t *testing.T, token workCountAttemptToken) DiagnosticWorkCount {
	t.Helper()
	finalizeWorkCountConvergenceAttempt(token)
	return EndDiagnosticWorkCount()
}

func readWorkCountContract(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func requireStringSliceEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s=%v want=%v", label, got, want)
	}
}

func requireExactJSONKeys(t *testing.T, label string, data json.RawMessage, want []string) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode %s: %v", label, err)
	}
	if len(raw) != len(want) {
		t.Fatalf("%s keys=%d want=%d: %v", label, len(raw), len(want), raw)
	}
	for _, key := range want {
		if _, ok := raw[key]; !ok {
			t.Fatalf("%s missing key %q", label, key)
		}
	}
}

func assertNoPointerKinds(t *testing.T, typ reflect.Type) {
	t.Helper()
	switch typ.Kind() {
	case reflect.Pointer, reflect.UnsafePointer, reflect.Uintptr:
		t.Fatalf("serialized type %s contains pointer kind %s", typ, typ.Kind())
	case reflect.Array, reflect.Slice:
		assertNoPointerKinds(t, typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			assertNoPointerKinds(t, typ.Field(i).Type)
		}
	}
}
