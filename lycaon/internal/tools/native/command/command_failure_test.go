package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestFailureStreakSourceAdmission(t *testing.T) {
	for _, variant := range []string{"unchanged", "changed", "unknown-baseline", "unknown-current", "other-root"} {
		t.Run(variant, func(t *testing.T) {
			tracker := NewCommandFailureTracker()
			current := commandSourceIdentity{"a", "root"}
			if variant == "unknown-baseline" {
				current = commandSourceIdentity{}
			}
			captures := 0
			observe := func() commandSourceIdentity { captures++; return current }
			for i := 0; i < 3; i++ {
				attempt, reject := tracker.begin("session", "approach", "command", observe)
				if reject != nil {
					t.Fatalf("attempt %d rejected: %+v", i, reject)
				}
				tracker.record(attempt, false)
				want := 0
				if i == 2 {
					want = 1
				}
				if captures != want {
					t.Fatalf("captures=%d want %d", captures, want)
				}
			}
			switch variant {
			case "changed":
				current.Revision = "b"
			case "other-root":
				current.Root = "other"
			case "unknown-current":
				current = commandSourceIdentity{}
			case "unknown-baseline":
				current = commandSourceIdentity{"b", "root"}
			}
			attempt, reject := tracker.begin("session", "approach", "command", observe)
			allowed := variant == "changed" || variant == "other-root"
			if (reject == nil) != allowed {
				t.Fatalf("allowed=%v reject=%+v", allowed, reject)
			}
			if reject != nil {
				if reject.Data["failure_source_unchanged"] != (variant == "unchanged") {
					t.Fatalf("source fact=%+v", reject.Data)
				}
				for i := 0; i < 3; i++ {
					if _, r := tracker.begin("session", "approach", "command", observe); r == nil {
						t.Fatal("unbounded retry")
					}
				}
			} else {
				tracker.record(attempt, false)
				if tracker.failures["session"].Count != 1 {
					t.Fatal("changed context did not start a bounded streak")
				}
			}
		})
	}
}

func TestFailureStreakDiscardsSupersededOutcomesAndComparisons(t *testing.T) {
	tracker := NewCommandFailureTracker()
	source := commandSourceIdentity{"a", "root"}
	observe := func() commandSourceIdentity { return source }
	first, _ := tracker.begin("s", "a", "a", observe)
	second, _ := tracker.begin("s", "b", "b", observe)
	tracker.record(second, false)
	tracker.record(first, true)
	if tracker.failures["s"].LastApproach != "b" || tracker.failures["s"].Count != 1 {
		t.Fatal("late success erased newer failure")
	}
	tracker.record(second, false)
	if tracker.failures["s"].Count != 1 {
		t.Fatal("duplicate outcome counted twice")
	}
	for i := 0; i < 2; i++ {
		a, _ := tracker.begin("s", "b", "b", observe)
		tracker.record(a, false)
	}
	_, reject := tracker.begin("s", "b", "b", func() commandSourceIdentity {
		other, _ := tracker.begin("s", "different", "different", nil)
		// A pre-execution failure supplies no process outcome, but invalidates the in-flight comparison.
		_ = other
		return commandSourceIdentity{"changed", "root"}
	})
	if reject == nil || reject.Data["failure_source_unchanged"] != false {
		t.Fatalf("stale comparison admitted retry: %+v", reject)
	}
	// The valid later capture can still observe the real change.
	source.Revision = "changed"
	a, r := tracker.begin("s", "b", "b", observe)
	if r != nil {
		t.Fatalf("fresh comparison rejected: %+v", r)
	}
	tracker.record(a, true)
	if tracker.failures["s"].Count != 0 {
		t.Fatal("successful retry retained streak")
	}
}

func TestFailureStreakNilTracker(t *testing.T) {
	var tracker *CommandFailureTracker
	a, r := tracker.begin("s", "a", "a", func() commandSourceIdentity { t.Fatal("nil tracker captured source"); return commandSourceIdentity{} })
	if r != nil {
		t.Fatal("nil tracker rejected")
	}
	tracker.record(a, false)
	tracker = NewCommandFailureTracker()
	a, r = tracker.begin("s", "", "", nil)
	if r != nil {
		t.Fatal("empty approach rejected")
	}
	tracker.record(a, false)
}

func TestCommandSourceReadersRespectExecutionAndReceiptRoots(t *testing.T) {
	primary, attached, worker, scratch := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	ledger := sourceledger.New(testdbfixture.Open(t, "source.db"), filepath.Join(t.TempDir(), "content"))
	t.Cleanup(func() { testutil.FailErr(t, "close snapshots", ledger.Snapshots.Close()) })
	tctx := tools.ToolContext{Source: tools.InvocationSource{Observations: ledger.Inventory, Roots: []projectroot.RootRef{{ID: "primary", Path: primary, IsPrimary: true}, {ID: "attached", Path: attached}}, ActiveRootID: "primary"}}
	for _, tc := range []struct{ name, cwd, branch, want string }{
		{"primary", primary, "", primary}, {"attached", attached, "", attached}, {"worker", worker, worker, worker}, {"worker-external", primary, worker, ""}, {"scratch", scratch, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := tctx
			local.Source.WorkerBranchRoot = tc.branch
			if got := commandFailureSourceRoot(local, tc.cwd); got != tc.want {
				t.Fatalf("root=%q want%q", got, tc.want)
			}
			failure, verification := commandSourceReaders(t.Context(), local, tc.cwd)
			first := failure()
			if first.known() != (tc.want != "") {
				t.Fatalf("failure identity=%+v", first)
			}
			receiptRoot := tools.HostWriteRoot(local)
			// Changing content between consumers proves same-scope receipt reuses the captured bytes.
			testutil.FailErr(t, "change receipt source", os.WriteFile(filepath.Join(receiptRoot, "changed.txt"), []byte(tc.name), 0600))
			repochange.Advance(receiptRoot)
			receipt := verification()
			if !receipt.known() {
				t.Fatal("receipt scope unknown")
			}
			if tc.want == receiptRoot && receipt != first {
				t.Fatal("same-root verification recaptured source")
			}
			if tc.want != receiptRoot && receipt.Root == first.Root {
				t.Fatal("receipt consumed other workspace identity")
			}
		})
	}
	// Mutating active source cannot clear a streak against the attached workspace.
	tracker := NewCommandFailureTracker()
	observe := func() commandSourceIdentity { return commandSourceReader(t.Context(), tctx, attached)() }
	for i := 0; i < 3; i++ {
		a, r := tracker.begin("s", "same", "same", observe)
		if r != nil {
			t.Fatalf("setup rejected: %+v", r)
		}
		tracker.record(a, false)
	}
	testutil.FailErr(t, "change unrelated root", os.WriteFile(filepath.Join(primary, "unrelated.txt"), []byte("changed"), 0600))
	repochange.Advance(primary)
	if _, r := tracker.begin("s", "same", "same", observe); r == nil {
		t.Fatal("unrelated active-root mutation cleared attached-root streak")
	}
}

func TestFailureStreakAggregatesConcurrentContextOutcomes(t *testing.T) {
	for _, order := range []string{"all-fail", "success-then-failure", "failure-then-success"} {
		t.Run(order, func(t *testing.T) {
			tracker := NewCommandFailureTracker()
			attempts := make([]*commandFailureAttempt, 3)
			for i := range attempts {
				attempts[i], _ = tracker.begin("s", "same", "same", nil)
			}
			switch order {
			case "all-fail":
				// Reverse completion order must still count all three distinct failures.
				for i := 2; i >= 0; i-- {
					tracker.record(attempts[i], false)
				}
				tracker.record(attempts[0], false)
				if tracker.failures["s"].Count != 3 {
					t.Fatalf("lost or duplicated concurrent failures: %+v", tracker.failures["s"])
				}
				if _, r := tracker.begin("s", "same", "same", nil); r == nil {
					t.Fatal("concurrent failures bypassed finite guard")
				}
			case "success-then-failure":
				tracker.record(attempts[0], false)
				tracker.record(attempts[1], true)
				tracker.record(attempts[2], false)
				if tracker.failures["s"].Count != 1 {
					t.Fatal("success did not separate completion streaks")
				}
			case "failure-then-success":
				tracker.record(attempts[2], false)
				tracker.record(attempts[1], false)
				tracker.record(attempts[0], true)
				if tracker.failures["s"].Count != 0 {
					t.Fatal("same-context success did not clear prior completed failures")
				}
			}
		})
	}
}

func TestFailureStreakSourceGenerationRejectsDelayedOldOutcomes(t *testing.T) {
	tracker := NewCommandFailureTracker()
	source := commandSourceIdentity{"old", "root"}
	observe := func() commandSourceIdentity { return source }
	first, _ := tracker.begin("s", "same", "same", observe)
	for i := 0; i < 3; i++ {
		a, _ := tracker.begin("s", "same", "same", observe)
		tracker.record(a, false)
	}
	source.Revision = "new"
	a, r := tracker.begin("s", "same", "same", observe)
	if r != nil {
		t.Fatalf("changed source rejected: %+v", r)
	}
	b, r := tracker.begin("s", "same", "same", observe)
	if r != nil {
		t.Fatalf("concurrent changed source rejected: %+v", r)
	}
	tracker.record(a, false)
	tracker.record(first, true)
	tracker.record(b, false)
	if tracker.failures["s"].Count != 2 {
		t.Fatalf("source generations conflated: %+v", tracker.failures["s"])
	}
}
