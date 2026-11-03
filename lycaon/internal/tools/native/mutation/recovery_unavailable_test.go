package mutation

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// Only the unavailable capability is injected; measurement, permanent removal,
// source attribution, and history use the real production owners.
type unavailableRecovery struct {
	sourceeffect.Journal
	calls int
}

func (j *unavailableRecovery) RemoveEntry(ctx context.Context, removal sourceeffect.Removal) (string, error) {
	j.calls++
	if j.calls == 1 {
		return "", &sourceeffect.RecoveryUnavailableError{Cause: syscall.EXDEV}
	}
	return j.Journal.RemoveEntry(ctx, removal)
}

func TestUnavailableRecoveryRequiresFreshPermanentRemovalReview(t *testing.T) {
	for _, decision := range []string{"approve", "decline", "no reviewer", "changed during review"} {
		t.Run(decision, func(t *testing.T) {
			h := newHarness(t, map[string]string{"tree/file": "original"})
			journal := &unavailableRecovery{Journal: h.service}
			h.tc.Source.SourceMutations = journal
			reviews := 0
			h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
				for _, change := range changes {
					if !change.RecoveryLoss {
						continue
					}
					reviews++
					if change.Preview.BeforeBytes != 8 {
						t.Fatalf("review did not name measured bytes: %+v", change)
					}
					t.Logf("unavailable-recovery preview: %s", change.Preview.PreviewNote)
					if decision == "decline" {
						return errors.New("test person declined")
					}
					if decision == "changed during review" {
						testutil.FailErr(t, "change reviewed tree", os.WriteFile(h.path("tree/file"), []byte("new content"), 0o600))
					}
				}
				return nil
			}
			if decision == "no reviewer" {
				h.tc.Files.FileChangeReview = nil
			}
			result := h.run("", "rm", "-rf", "tree")
			if decision == "approve" {
				if result.err != nil || result.result.Status != 0 || !h.absent("tree") || reviews != 1 || journal.calls != 2 {
					t.Fatalf("approved permanent removal: calls=%d reviews=%d result=%+v error=%v", journal.calls, reviews, result.result, result.err)
				}
				state, err := h.service.History.State(t.Context(), h.project.ID)
				testutil.FailErr(t, "read permanent removal history", err)
				if state.Undo != nil {
					t.Fatal("permanent removal invented recovery")
				}
			} else {
				if result.err == nil || h.absent("tree/file") {
					t.Fatalf("unapproved or changed entry was deleted: %+v error=%v", result.result, result.err)
				}
				if decision == "no reviewer" {
					reject := toolrejection.AsToolReject(result.err)
					if reject == nil || reject.Code != "SOURCE_RECOVERY_APPROVAL_UNAVAILABLE" {
						t.Fatalf("unavailable reviewer result: %v", result.err)
					}
				}
			}
		})
	}
}
