package promptloop

import "testing"

// Distinct rejection codes count toward the absolute cap, not the repeat cap.
func TestNovelBlockedCodeDoesNotSpendTheAllowance(t *testing.T) {
	st := &promptLoopTurnState{}
	for i, code := range []string{
		"SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT",
		"PROGRESS_ITEM_NOT_CLOSED",
		"DOOM_LOOP_CODE_REPEAT",
	} {
		if st.noteBlockedBatch(code, "") {
			t.Fatalf("closed out on blocked batch %d (%s); each Code was new information", i+1, code)
		}
	}
	if st.consecutiveBlockedBatches != 0 {
		t.Errorf("three distinct Codes charged %d repeats, want 0", st.consecutiveBlockedBatches)
	}
	if st.blockedBatchesThisStreak != 3 {
		t.Errorf("streak counted %d blocked batches, want 3", st.blockedBatchesThisStreak)
	}
}

func TestRepeatedBlockedCodeSpendsTheAllowance(t *testing.T) {
	st := &promptLoopTurnState{}
	if st.noteBlockedBatch("PROGRESS_ITEM_NOT_CLOSED", "") {
		t.Fatal("closed out on the Code's first appearance")
	}
	for i := 1; i < BlockedLoopRejectCap; i++ {
		if st.noteBlockedBatch("PROGRESS_ITEM_NOT_CLOSED", "") {
			t.Fatalf("closed out after %d repeats, want %d", i, BlockedLoopRejectCap)
		}
	}
	if !st.noteBlockedBatch("PROGRESS_ITEM_NOT_CLOSED", "") {
		t.Fatalf("did not close out after %d repeats of one Code", BlockedLoopRejectCap)
	}
}

// Alternating rejection codes still consume the repeat budget.
func TestAlternatingBlockedCodesStillCloseOut(t *testing.T) {
	st := &promptLoopTurnState{}
	closed := false
	for i := 0; i < BlockedLoopAbsoluteCap; i++ {
		code := "PROGRESS_ITEM_NOT_CLOSED"
		if i%2 == 1 {
			code = "SOURCE_EVIDENCE_UNMET_BEFORE_CLOSEOUT"
		}
		if st.noteBlockedBatch(code, "") {
			closed = true
			break
		}
	}
	if !closed {
		t.Fatalf("alternating two Codes never closed out in %d batches", BlockedLoopAbsoluteCap)
	}
}

// A fresh Code every batch still terminates, on the absolute ceiling.
func TestEveryBatchNovelStillHitsAbsoluteCap(t *testing.T) {
	st := &promptLoopTurnState{}
	for i := 0; i < BlockedLoopAbsoluteCap-1; i++ {
		if st.noteBlockedBatch(distinctCode(i), "") {
			t.Fatalf("closed out at batch %d on all-novel Codes; ceiling is %d", i+1, BlockedLoopAbsoluteCap)
		}
	}
	if !st.noteBlockedBatch(distinctCode(BlockedLoopAbsoluteCap), "") {
		t.Fatalf("all-novel Codes never hit the absolute ceiling of %d", BlockedLoopAbsoluteCap)
	}
}

// A batch that runs clears the streak, so a Code is new again afterwards.
func TestRunningBatchClearsTheStreak(t *testing.T) {
	st := &promptLoopTurnState{}
	st.noteBlockedBatch("PROGRESS_ITEM_NOT_CLOSED", "")
	st.noteBlockedBatch("PROGRESS_ITEM_NOT_CLOSED", "")
	if st.consecutiveBlockedBatches != 1 {
		t.Fatalf("consecutiveBlockedBatches = %d, want 1", st.consecutiveBlockedBatches)
	}
	st.clearBlockedStreak()
	if st.noteBlockedBatch("PROGRESS_ITEM_NOT_CLOSED", ""); st.consecutiveBlockedBatches != 0 {
		t.Errorf("Code was still charged after a batch ran: %d repeats", st.consecutiveBlockedBatches)
	}
	if st.blockedBatchesThisStreak != 1 {
		t.Errorf("streak total = %d after clear, want 1", st.blockedBatchesThisStreak)
	}
}

// The absolute cap leaves room for distinct rejection codes.
func TestAbsoluteCapLeavesRoomForNovelCodes(t *testing.T) {
	if BlockedLoopAbsoluteCap <= BlockedLoopRejectCap {
		t.Fatalf("absolute ceiling %d must sit above the repeat cap %d, or a Code's first appearance is never free",
			BlockedLoopAbsoluteCap, BlockedLoopRejectCap)
	}
}

func distinctCode(i int) string {
	return "CODE_" + string(rune('A'+i%26)) + string(rune('0'+i/26))
}
