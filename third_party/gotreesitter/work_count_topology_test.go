//go:build gts_workcount

package gotreesitter_test

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"testing"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

const erlangIssue984Source = "000\"0A!A \"A\"=0:A0!)A\"0%0000"

type diagnosticTopologyCapture struct {
	sexpr  string
	digest string
	events uint64
}

func captureErlangTopology(source string) (diagnosticTopologyCapture, error) {
	parser := gts.NewParser(grammars.ErlangLanguage())
	parser.SetAdmissionCandidateRoute(false)
	gts.BeginDiagnosticTopologyReceipt()
	tree, parseErr := parser.Parse([]byte(source))
	receipt := gts.EndDiagnosticTopologyReceipt()
	if parseErr != nil {
		return diagnosticTopologyCapture{}, parseErr
	}
	if tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		return diagnosticTopologyCapture{}, fmt.Errorf("parse returned no root")
	}
	defer tree.Release()
	if !receipt.Complete() {
		return diagnosticTopologyCapture{}, fmt.Errorf("receipt is incomplete: %+v", receipt)
	}
	return diagnosticTopologyCapture{
		sexpr:  tree.RootNode().SExpr(grammars.ErlangLanguage()),
		digest: receipt.SHA256(),
		events: receipt.EventsSeen,
	}, nil
}

func TestDiagnosticTopologyReceiptSerializesConcurrentOwners(t *testing.T) {
	sources := [...]string{erlangIssue984Source, "f() -> ."}
	var want [len(sources)]diagnosticTopologyCapture
	for i := range sources {
		capture, err := captureErlangTopology(sources[i])
		if err != nil {
			t.Fatalf("capture baseline %d: %v", i, err)
		}
		want[i] = capture
	}

	const workerCount = 8
	type result struct {
		worker  int
		capture diagnosticTopologyCapture
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, workerCount)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func(worker int) {
			defer workers.Done()
			<-start
			capture, err := captureErlangTopology(sources[worker%len(sources)])
			results <- result{worker: worker, capture: capture, err: err}
		}(worker)
	}
	close(start)
	workers.Wait()
	close(results)
	for got := range results {
		if got.err != nil {
			t.Errorf("worker %d: %v", got.worker, got.err)
			continue
		}
		if expected := want[got.worker%len(sources)]; got.capture != expected {
			t.Errorf("worker %d capture = %+v, want %+v", got.worker, got.capture, expected)
		}
	}
}

func captureErlangIssue984Topology(t *testing.T) (string, gts.DiagnosticTopologyReceipt) {
	t.Helper()
	if got := len(erlangIssue984Source); got != 27 {
		t.Fatalf("Erlang issue 984 witness length = %d, want 27", got)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(erlangIssue984Source))); got != "ad9482f4aabfc3f348a20f6071a2732dcf0473d87b61fa8df4f960e84d4b1ab4" {
		t.Fatalf("Erlang issue 984 witness SHA-256 = %s", got)
	}
	parser := gts.NewParser(grammars.ErlangLanguage())
	parser.SetAdmissionCandidateRoute(false)
	gts.BeginDiagnosticTopologyReceipt()
	tree, err := parser.Parse([]byte(erlangIssue984Source))
	receipt := gts.EndDiagnosticTopologyReceipt()
	if err != nil {
		t.Fatalf("parse the Erlang issue 984 witness: %v", err)
	}
	if tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		t.Fatal("parse the Erlang issue 984 witness: no root")
	}
	defer tree.Release()
	if tree.RootNode().HasError() {
		t.Fatalf("Erlang issue 984 witness has an error root: %s", tree.RootNode().SExpr(grammars.ErlangLanguage()))
	}
	return tree.RootNode().SExpr(grammars.ErlangLanguage()), receipt
}

func assertDiagnosticTopologyEventInvariants(t *testing.T, receipt gts.DiagnosticTopologyReceipt) {
	t.Helper()
	const allowedFlags = uint64(1<<6) - 1
	nextVersionID := uint64(1)
	nextLinkID := uint64(1)
	nextElectionID := uint64(1)
	lastActionID := uint64(0)
	lastPopID := uint64(0)
	lastPathOrdinal := uint64(0)
	for i, event := range receipt.Events {
		if event.EventID != uint64(i+1) {
			t.Fatalf("event %d has ID %d", i, event.EventID)
		}
		if event.Kind < gts.DiagnosticTopologyEventAction || event.Kind > gts.DiagnosticTopologyEventAcceptElection {
			t.Fatalf("event %d has kind %d", event.EventID, event.Kind)
		}
		if event.Flags&^allowedFlags != 0 {
			t.Fatalf("event %d has reserved flags %#x", event.EventID, event.Flags)
		}
		hasAction := event.Flags&gts.DiagnosticTopologyFlagActionContextKnown != 0
		if hasAction && (event.ActionID == 0 || event.ActionType > uint64(gts.ParseActionRecover) || event.ActionOrdinal < -1) {
			t.Fatalf("event %d has invalid action context: %+v", event.EventID, event)
		}
		if !hasAction && (event.ActionID != 0 || event.ActionOrdinal != -1 || event.ActionType != 255) {
			t.Fatalf("event %d has invalid absent action context: %+v", event.EventID, event)
		}
		if event.Kind != gts.DiagnosticTopologyEventAction && hasAction && event.ActionID != lastActionID {
			t.Fatalf("event %d is outside action %d: latest action is %d", event.EventID, event.ActionID, lastActionID)
		}
		switch event.Kind {
		case gts.DiagnosticTopologyEventAction:
			if event.ActionID <= lastActionID {
				t.Fatalf("action ID %d follows %d", event.ActionID, lastActionID)
			}
			lastActionID = event.ActionID
		case gts.DiagnosticTopologyEventVersionAdd, gts.DiagnosticTopologyEventVersionCopy:
			if event.VersionID != nextVersionID || event.NodeID == 0 {
				t.Fatalf("version event %d has ID/node %d/%d, want ID %d", event.EventID, event.VersionID, event.NodeID, nextVersionID)
			}
			if event.Kind == gts.DiagnosticTopologyEventVersionCopy && event.SourceVersionID == 0 {
				t.Fatalf("version copy event %d has no source: %+v", event.EventID, event)
			}
			nextVersionID++
		case gts.DiagnosticTopologyEventVersionRenumber:
			if event.VersionID == 0 || event.SourceVersionID != event.VersionID || event.TargetVersionID == 0 ||
				event.VersionIndex != event.TargetIndex || event.SourceIndex <= event.TargetIndex || event.NodeID == 0 ||
				event.Flags&gts.DiagnosticTopologyFlagTargetReplaced == 0 {
				t.Fatalf("invalid renumber event %d: %+v", event.EventID, event)
			}
		case gts.DiagnosticTopologyEventMerge:
			if event.SurvivorVersionID == 0 || event.RemovedVersionID == 0 || event.SurvivorVersionID != event.SourceVersionID ||
				event.RemovedVersionID != event.TargetVersionID || event.SourceIndex >= event.TargetIndex {
				t.Fatalf("invalid merge event %d: %+v", event.EventID, event)
			}
		case gts.DiagnosticTopologyEventLinkInsert:
			if event.NodeID == 0 || event.PredecessorNodeID == 0 || event.LinkID != nextLinkID || event.LinkOrdinal >= 8 {
				t.Fatalf("link event %d has an incomplete identity: %+v", event.EventID, event)
			}
			nextLinkID++
		case gts.DiagnosticTopologyEventPopPath:
			if event.PopID == 0 || event.VersionID == 0 || event.SourceVersionID == 0 || event.NodeID == 0 || event.PopToNodeID == 0 {
				t.Fatalf("pop event %d has an incomplete identity: %+v", event.EventID, event)
			}
			if event.PopID == lastPopID {
				if event.PathOrdinal != lastPathOrdinal+1 {
					t.Fatalf("pop %d path ordinal %d follows %d", event.PopID, event.PathOrdinal, lastPathOrdinal)
				}
			} else if event.PopID <= lastPopID || event.PathOrdinal != 0 {
				t.Fatalf("new pop %d path ordinal %d follows pop %d", event.PopID, event.PathOrdinal, lastPopID)
			}
			lastPopID = event.PopID
			lastPathOrdinal = event.PathOrdinal
		case gts.DiagnosticTopologyEventChildElection, gts.DiagnosticTopologyEventAcceptElection:
			if event.ElectionID != nextElectionID || event.CandidateID == 0 || event.SelectedID == 0 {
				t.Fatalf("election event %d has an incomplete identity: %+v", event.EventID, event)
			}
			if event.Flags&gts.DiagnosticTopologyFlagNoIncumbent == 0 && event.IncumbentID == 0 {
				t.Fatalf("election event %d has no incumbent: %+v", event.EventID, event)
			}
			nextElectionID++
		}
	}
}

func TestDiagnosticTopologyReceiptErlangIssue984(t *testing.T) {
	sexpr, receipt := captureErlangIssue984Topology(t)
	if want := `(source_file (integer) (string) (var) (string) (integer) (comment))`; sexpr != want {
		t.Fatalf("production witness tree = %s, want %s", sexpr, want)
	}
	if receipt.Schema != gts.DiagnosticTopologyReceiptSchema || receipt.Capacity != gts.DiagnosticTopologyReceiptCapacity {
		t.Fatalf("receipt identity = %q/%d", receipt.Schema, receipt.Capacity)
	}
	if !receipt.Complete() || receipt.EventsRetained != receipt.EventsSeen || receipt.EventsDropped != 0 {
		t.Fatalf("receipt is incomplete: %+v", receipt)
	}
	assertDiagnosticTopologyEventInvariants(t, receipt)

	counts := make(map[uint64]int)
	anchors := make(map[[4]uint64]bool)
	type actionSliceEntry struct {
		state, actionType uint64
		ordinal           int64
		versionIndex      uint64
	}
	var state320Slice []actionSliceEntry
	state320Started := false
	var acceptEvents []gts.DiagnosticTopologyEvent
	for _, event := range receipt.Events {
		counts[event.Kind]++
		if event.Kind == gts.DiagnosticTopologyEventAction {
			if event.State == 320 && event.ByteOffset == 10 {
				state320Started = true
			}
			if state320Started && event.ByteOffset == 10 {
				state320Slice = append(state320Slice, actionSliceEntry{event.State, event.ActionType, event.ActionOrdinal, event.VersionIndex})
			}
		}
		if event.Kind == gts.DiagnosticTopologyEventAction && event.ActionType == uint64(gts.ParseActionReduce) {
			anchors[[4]uint64{event.State, event.LookaheadSymbol, event.ByteOffset, uint64(event.ActionOrdinal)}] = true
			if (event.State == 320 && event.LookaheadSymbol == 130 && event.ByteOffset == 10) ||
				(event.State == 274 && event.LookaheadSymbol == 133 && event.ByteOffset == 11) {
				t.Logf("anchor event=%d state=%d lookahead=%d byte=%d ordinal=%d version=%d index=%d", event.EventID, event.State, event.LookaheadSymbol, event.ByteOffset, event.ActionOrdinal, event.VersionID, event.VersionIndex)
			}
		}
		if event.Kind == gts.DiagnosticTopologyEventAcceptElection {
			acceptEvents = append(acceptEvents, event)
		}
	}
	for _, anchor := range [][4]uint64{
		{320, 130, 10, 0},
		{320, 130, 10, 1},
		{274, 133, 11, 0},
		{274, 133, 11, 1},
	} {
		if !anchors[anchor] {
			t.Errorf("missing all-REDUCE anchor state/lookahead/byte/ordinal %v", anchor)
		}
	}
	wantState320Slice := []actionSliceEntry{
		{320, uint64(gts.ParseActionReduce), 0, 0},
		{320, uint64(gts.ParseActionReduce), 1, 0},
		{830, uint64(gts.ParseActionShift), 0, 0},
		{295, uint64(gts.ParseActionReduce), 0, 1},
		{264, uint64(gts.ParseActionReduce), 0, 1},
		{50, uint64(gts.ParseActionReduce), 0, 1},
		{44, uint64(gts.ParseActionShift), 0, 1},
	}
	state320Matches := len(state320Slice) == len(wantState320Slice)
	for i := 0; state320Matches && i < len(state320Slice); i++ {
		state320Matches = state320Slice[i] == wantState320Slice[i]
	}
	if !state320Matches {
		t.Fatalf("state-320 frontier slice = %v, want %v", state320Slice, wantState320Slice)
	}
	for _, kind := range []uint64{
		gts.DiagnosticTopologyEventAction,
		gts.DiagnosticTopologyEventVersionAdd,
		gts.DiagnosticTopologyEventVersionRenumber,
		gts.DiagnosticTopologyEventMerge,
		gts.DiagnosticTopologyEventLinkInsert,
		gts.DiagnosticTopologyEventPopPath,
		gts.DiagnosticTopologyEventChildElection,
		gts.DiagnosticTopologyEventAcceptElection,
	} {
		if counts[kind] == 0 {
			t.Errorf("receipt has no event kind %d", kind)
		}
	}
	if len(acceptEvents) != 1 {
		t.Fatalf("accept elections = %d, want one accepted candidate", len(acceptEvents))
	}
	wantCounts := map[uint64]int{
		gts.DiagnosticTopologyEventAction:          43,
		gts.DiagnosticTopologyEventVersionAdd:      41,
		gts.DiagnosticTopologyEventVersionCopy:     0,
		gts.DiagnosticTopologyEventVersionRenumber: 35,
		gts.DiagnosticTopologyEventMerge:           55,
		gts.DiagnosticTopologyEventLinkInsert:      63,
		gts.DiagnosticTopologyEventPopPath:         41,
		gts.DiagnosticTopologyEventChildElection:   1,
		gts.DiagnosticTopologyEventAcceptElection:  1,
	}
	for kind, want := range wantCounts {
		if counts[kind] != want {
			t.Errorf("event kind %d count = %d, want %d", kind, counts[kind], want)
		}
	}
	wantAcceptPayloads := []uint64{3}
	for i := range acceptEvents {
		if acceptEvents[i].PayloadCount != wantAcceptPayloads[i] {
			t.Fatalf("accept payload = %d, want %v", acceptEvents[0].PayloadCount, wantAcceptPayloads)
		}
		if acceptEvents[i].Flags&gts.DiagnosticTopologyFlagActionContextKnown == 0 ||
			acceptEvents[i].ActionID == 0 || acceptEvents[i].ActionType != uint64(gts.ParseActionAccept) {
			t.Fatalf("accept election lacks ACCEPT action context: %+v", acceptEvents[i])
		}
	}
	last := acceptEvents[len(acceptEvents)-1]
	if last.VersionIndex != 0 || last.SelectedID != last.CandidateID ||
		last.Flags&gts.DiagnosticTopologyFlagSuccessOrSelected == 0 || last.Flags&gts.DiagnosticTopologyFlagNoIncumbent == 0 {
		t.Fatalf("final production selection = %+v, want the physical slot 0 candidate selected without an incumbent", last)
	}
	wantFinalKinds := []uint64{
		gts.DiagnosticTopologyEventAction,
		gts.DiagnosticTopologyEventLinkInsert,
		gts.DiagnosticTopologyEventVersionAdd,
		gts.DiagnosticTopologyEventPopPath,
		gts.DiagnosticTopologyEventAcceptElection,
	}
	finalEvents := receipt.Events[len(receipt.Events)-len(wantFinalKinds):]
	for i, wantKind := range wantFinalKinds {
		if finalEvents[i].Kind != wantKind {
			t.Fatalf("final event %d kind = %d, want %d", i, finalEvents[i].Kind, wantKind)
		}
	}
	acceptAction := finalEvents[0]
	if acceptAction.ActionType != uint64(gts.ParseActionAccept) {
		t.Fatalf("final action = %+v, want ACCEPT", acceptAction)
	}
	if eofLink := finalEvents[1]; eofLink.ActionID != acceptAction.ActionID || eofLink.State != 1 ||
		eofLink.Flags&gts.DiagnosticTopologyFlagPrimaryLink == 0 {
		t.Fatalf("ACCEPT EOF push = %+v, want a primary state-1 link", eofLink)
	}
	if pop := finalEvents[3]; pop.ActionID != acceptAction.ActionID || pop.PayloadCount != 3 || pop.PathOrdinal != 0 {
		t.Fatalf("ACCEPT pop = %+v, want one three-payload path", pop)
	}
	t.Logf("receipt sha256=%s events=%d kinds=%v selected_candidate=%d", receipt.SHA256(), receipt.EventsSeen, counts, last.SelectedID)
	if receipt.EventsSeen != 280 || receipt.SHA256() != "f90b82a19bd52475a0b61376a631fe53b69ac827c89bec6802940e8302d77754" {
		t.Fatalf("canonical receipt = %d/%s", receipt.EventsSeen, receipt.SHA256())
	}

	secondSExpr, second := captureErlangIssue984Topology(t)
	if secondSExpr != sexpr || second.SHA256() != receipt.SHA256() {
		t.Fatalf("receipt is not deterministic: first=%s/%s second=%s/%s", sexpr, receipt.SHA256(), secondSExpr, second.SHA256())
	}
}

func TestDiagnosticTopologyReceiptRecoveryDoesNotForgeAcceptContext(t *testing.T) {
	parser := gts.NewParser(grammars.ErlangLanguage())
	parser.SetAdmissionCandidateRoute(false)
	gts.BeginDiagnosticTopologyReceipt()
	tree, err := parser.Parse([]byte("f() -> ."))
	receipt := gts.EndDiagnosticTopologyReceipt()
	if err != nil {
		t.Fatalf("parse recovery witness: %v", err)
	}
	if tree == nil || tree.RootNode() == nil {
		if tree != nil {
			tree.Release()
		}
		t.Fatal("parse recovery witness: no root")
	}
	defer tree.Release()
	if !tree.RootNode().HasError() {
		t.Fatalf("recovery witness has no error: %s", tree.RootNode().SExpr(grammars.ErlangLanguage()))
	}
	if !receipt.Complete() {
		t.Fatalf("recovery receipt is incomplete: %+v", receipt)
	}
	foundPotentialReduction := false
	for _, event := range receipt.Events {
		if event.Kind == gts.DiagnosticTopologyEventAction &&
			event.ActionType == uint64(gts.ParseActionReduce) &&
			event.ActionOrdinal == -1 && event.State == 414 && event.ByteOffset == 6 {
			if event.LookaheadSymbol != 2 {
				t.Fatalf("recovery reduction lookahead = %d, want 2: %+v", event.LookaheadSymbol, event)
			}
			foundPotentialReduction = true
		}
		if event.Kind == gts.DiagnosticTopologyEventAcceptElection &&
			(event.Flags&gts.DiagnosticTopologyFlagActionContextKnown == 0 || event.ActionType != uint64(gts.ParseActionAccept)) {
			t.Fatalf("recovery forged an ACCEPT election: %+v", event)
		}
	}
	if !foundPotentialReduction {
		t.Fatal("recovery receipt has no state-414 potential reduction at byte 6")
	}
}
