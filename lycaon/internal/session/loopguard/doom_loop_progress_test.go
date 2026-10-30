package loopguard

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTerminalProgressRewordingStillRepeats(t *testing.T) {
	g := NewMemoryDoomLoopGuard()
	for i := range DoomLoopMaxAttempts {
		args := map[string]any{"content": fmt.Sprintf("## Progress\n- [x] Finished %d", i)}
		testutil.FailErr(t, "record completed checklist", g.RecordAttempt(t.Context(), "session", uuid.NewString(), "update_progress", args, nil, false))
	}
	args := map[string]any{"content": "## Progress\n- [x] Reworded completion"}
	allowed, count, _, err := g.Check(t.Context(), "session", uuid.NewString(), "update_progress", args, false, nil)
	testutil.FailErr(t, "check repeated completion", err)
	if allowed || count != DoomLoopMaxAttempts {
		t.Fatalf("allowed=%v count=%d", allowed, count)
	}
	for _, tool := range []string{"read", "update_progress"} {
		testutil.FailErr(t, "record inspection or bookkeeping", g.RecordAttempt(t.Context(), "session", uuid.NewString(), tool, map[string]any{"content": "- [ ] Unrelated scope"}, nil, false))
		allowed, _, _, err = g.Check(t.Context(), "session", uuid.NewString(), "update_progress", args, false, nil)
		testutil.FailErr(t, "check completion after nonadvancement", err)
		if allowed {
			t.Fatal("inspection or bookkeeping erased completed-checklist repetition")
		}
	}
	testutil.FailErr(t, "record real work", g.RecordAttempt(t.Context(), "session", uuid.NewString(), "write", map[string]any{"path": "main.go"}, nil, true))
	allowed, _, _, err = g.Check(t.Context(), "session", uuid.NewString(), "update_progress", args, false, nil)
	testutil.FailErr(t, "check completion after work", err)
	if !allowed {
		t.Fatal("real intervening work must permit another completed checklist")
	}
}

func TestProgressFingerprintKeepsPendingContent(t *testing.T) {
	for _, content := range []string{"", "## Progress\n- [ ] Pending", "## Progress\n- [x] Done\n- [ ] Next"} {
		got := doomLoopUpdateProgressArgs(map[string]any{"content": content})
		if got["content"] != content {
			t.Fatalf("pending or empty content lost: %+v", got)
		}
	}
	if got := doomLoopUpdateProgressArgs(nil); len(got) != 0 {
		t.Fatalf("nil args = %+v", got)
	}
}

func TestTerminalProgressKeepsDistinctFinishAcknowledgements(t *testing.T) {
	guard := NewMemoryDoomLoopGuard()
	first := strings.Repeat("a", 64)
	second := strings.Repeat("b", 64)
	for n := range DoomLoopMaxAttempts {
		args := map[string]any{"content": fmt.Sprintf("- [x] Label %d", n), "reconciliation_token": first}
		testutil.FailErr(t, "record repeated acknowledgement", guard.RecordAttempt(t.Context(), "session", fmt.Sprint(n), "update_progress", args, nil, false))
	}
	for _, tc := range []struct {
		token   string
		allowed bool
	}{{first, false}, {second, true}} {
		allowed, _, _, err := guard.Check(t.Context(), "session", "next", "update_progress", map[string]any{"content": "- [x] Different label", "reconciliation_token": tc.token}, true, nil)
		testutil.FailErr(t, "check finish identity", err)
		if allowed != tc.allowed {
			t.Fatalf("finish identity allowed=%v want=%v", allowed, tc.allowed)
		}
	}
}

func TestHostIssuedRevisionRecoversAnExhaustedTerminalBucket(t *testing.T) {
	for _, mode := range []string{"missing", "stale"} {
		t.Run(mode, func(t *testing.T) {
			store := progress.NewMemoryStore()
			testutil.FailErr(t, "seed current work", store.Set("session", "- [ ] Deliver"))
			current, err := store.Snapshot(t.Context(), "session")
			testutil.FailErr(t, "observe current revision", err)
			update, err := progress.PrepareAuthorUpdate("- [x] Deliver")
			testutil.FailErr(t, "prepare completion", err)
			args := map[string]any{"content": "- [x] Deliver"}
			attemptedRevision := ""
			if mode == "stale" {
				attemptedRevision = strings.Repeat("0", 64)
				args["expected_revision"] = attemptedRevision
			}
			guard := NewMemoryDoomLoopGuard()
			code := ""
			for n := range DoomLoopMaxAttempts {
				_, err = update.ExpectRevision(attemptedRevision).Apply(t.Context(), store, "session", nil)
				var rejected *progress.AuthorUpdateRejection
				if !errors.As(err, &rejected) {
					t.Fatalf("owner did not refuse missing/stale basis: %v", err)
				}
				code = rejected.Code
				testutil.FailErr(t, "retain actual owner failure", guard.RecordAttempt(t.Context(), "session", fmt.Sprint(n), "update_progress", args, &api.InvocationFailure{Code: code, Details: rejected.Data}, false))
			}
			wrong := map[string]any{"content": "- [x] Reworded", "expected_revision": strings.Repeat("f", 64)}
			allowed, _, _, err := guard.Check(t.Context(), "session", "wrong", "update_progress", wrong, true, nil)
			testutil.FailErr(t, "check unoffered basis", err)
			if allowed {
				t.Fatal("invented revision cleared host rejection")
			}
			corrected := map[string]any{"content": "- [x] Deliver", "expected_revision": current.Revision}
			allowed, count, _, err := guard.Check(t.Context(), "session", "recovered", "update_progress", corrected, true, nil)
			testutil.FailErr(t, "check host-issued remedy", err)
			if !allowed || count != 0 {
				t.Fatalf("trusted remedy remained blocked: %v count=%d", allowed, count)
			}
			result, err := update.ExpectRevision(current.Revision).Apply(t.Context(), store, "session", nil)
			testutil.FailErr(t, "retry real progress owner", err)
			if !result.StateChanged || !result.DeliverableClosed {
				t.Fatalf("owner retry lost transition: %+v", result)
			}
			if guard.CodeRejectResponses("session", "update_progress", code) != DoomLoopMaxCodeRepeats {
				t.Fatal("recovery erased diagnostic history")
			}
		})
	}
}

func TestProgressRecoveryUsesOnlyStructuredHostRemedies(t *testing.T) {
	revision := strings.Repeat("a", 64)
	token := strings.Repeat("b", 64)
	snapshot := progress.Snapshot{Revision: revision, Reconciliation: &progress.Reconciliation{Token: token}}
	for _, value := range []any{snapshot, map[string]any{"revision": revision, "reconciliation": map[string]any{"token": token}}} {
		recovery := progressRecoveryFromFailure(&api.InvocationFailure{Code: "PROGRESS_RECONCILIATION_CHANGED", Details: map[string]any{"snapshot": value}})
		for _, tc := range []struct {
			revision, token string
			matches         bool
		}{{revision, token, true}, {revision, "", false}, {"other", token, false}, {revision, "other", false}} {
			if recovery.matches(map[string]any{"expected_revision": tc.revision, "reconciliation_token": tc.token}) != tc.matches {
				t.Fatalf("reconciliation remedy mismatch: %+v", tc)
			}
		}
	}
	for _, failure := range []*api.InvocationFailure{nil, {Code: "UNRELATED", Details: map[string]any{"snapshot": snapshot}}, {Code: "PROGRESS_REVISION_CHANGED", Details: map[string]any{"snapshot": make(chan int)}}, {Code: "PROGRESS_REVISION_CHANGED", Details: map[string]any{"snapshot": "not a snapshot"}}, {Code: "PROGRESS_REVISION_CHANGED"}} {
		if progressRecoveryFromFailure(failure).matches(map[string]any{"expected_revision": revision}) {
			t.Fatalf("missing owner remedy authorized recovery: %+v", failure)
		}
	}
	recovery := progressRecoveryFromFailure(&api.InvocationFailure{Code: "PROGRESS_RECONCILIATION_CHANGED", Details: map[string]any{"snapshot": progress.Snapshot{Revision: revision}}})
	if !recovery.matches(map[string]any{"expected_revision": revision}) || recovery.matches(map[string]any{"expected_revision": revision, "reconciliation_token": token}) {
		t.Fatal("settled finish set did not require removing obsolete acknowledgement")
	}
}
