package mutation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/sourceeffect"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestOversizeDeleteRejectionLeavesTreeIntact(t *testing.T) {
	h := newHarness(t, map[string]string{
		"oversize/a.txt": strings.Repeat("a", 32),
		"oversize/b.txt": strings.Repeat("b", 32),
	})
	h.service.History.SetRecoveryQuotaBytes(16)
	var notes []string
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, ch := range changes {
			if !ch.RecoveryLoss || ch.Preview.BeforeBytes != 64 {
				t.Fatalf("inaccurate permanent removal facts: %+v", ch)
			}
			notes = append(notes, ch.Preview.PreviewNote)
			t.Logf("permanent-removal preview: %s", ch.Preview.PreviewNote)
		}
		return errors.New("person declined permanent deletion")
	}
	r := h.run("", "rm", "-rf", "oversize")
	if r.err == nil || h.absent("oversize/a.txt") || h.absent("oversize/b.txt") {
		t.Fatalf("declined oversize delete should leave tree intact: %+v %v", r.result, r.err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected one permanent-removal review, got %d", len(notes))
	}
}

func TestDirectoryDeletesRespectQuotaAndPruneRetainedTrees(t *testing.T) {
	h := newHarness(t, nil)
	// Measure the real filesystem allocation before setting this fixture
	// quota. Retained trees must be reclaimed without per-file blob capture.

	for round := 0; round < 5; round++ {
		chunk := fmt.Sprintf("round %d\n", round) + strings.Repeat("retained-build-artifact-line\n", 48)
		for _, dir := range []string{"node_modules/pkg", "dist/assets"} {
			full := h.path(filepath.Join(dir, fmt.Sprintf("bundle-%d.js", round)))
			testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(filepath.Dir(full), 0o755))
			testutil.FailErr(t, "write "+full, os.WriteFile(full, []byte(chunk), 0o644))
		}
		if round == 0 {
			measured, err := h.service.InspectRemoval(t.Context(), fseffect.Location{Root: h.root, Rel: "node_modules"})
			testutil.FailErr(t, "measure retained tree allocation", err)
			if !measured.RecoverySizeKnown || measured.RecoveryBytes < 1 {
				t.Fatalf("unavailable fixture allocation: %+v", measured)
			}
			h.service.History.SetRecoveryQuotaBytes(2 * measured.RecoveryBytes)
		}
		if r := h.run("", "rm", "-rf", "node_modules"); r.err != nil || r.result.Status != 0 || !h.absent("node_modules") {
			t.Fatalf("round %d rm node_modules: %+v %v", round, r.result, r.err)
		}
		if r := h.run("", "rm", "-rf", "dist"); r.err != nil || r.result.Status != 0 || !h.absent("dist") {
			t.Fatalf("round %d rm dist: %+v %v", round, r.result, r.err)
		}
	}

	var historyCount int
	testutil.FailErr(t, "count history entries", h.db.QueryRowContext(t.Context(), `SELECT count(*) FROM source_history_entries WHERE project_id = ?`, h.project.ID).Scan(&historyCount))
	if historyCount > 2 {
		t.Fatalf("expected directory history to stay bounded (<= 2 entries), got %d", historyCount)
	}
	var totalRecoveryBytes int64
	testutil.FailErr(t, "sum recovery bytes", h.db.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(size), 0) FROM source_blob_objects`).Scan(&totalRecoveryBytes))
	if totalRecoveryBytes != 0 {
		t.Fatalf("tree removal captured per-file blobs: %d bytes", totalRecoveryBytes)
	}
	ledger := h.tc.Source.SourceLedger.(*sourceledger.Store)
	slots, err := os.ReadDir(ledger.Retention.RecoveryTreeRoot())
	testutil.FailErr(t, "inspect retained recovery slots", err)
	if len(slots) != historyCount {
		t.Fatalf("physical custody not reclaimed with history: slots=%d history=%d", len(slots), historyCount)
	}
}

func TestOversizeDeleteWithoutReviewerLeavesTreeIntact(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/a": "content"})
	h.service.History.SetRecoveryQuotaBytes(1)
	h.tc.Files.FileChangeReview = nil
	r := h.run("", "rm", "-rf", "tree")
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != "SOURCE_RECOVERY_APPROVAL_UNAVAILABLE" || h.absent("tree/a") {
		t.Fatalf("permanent removal escaped review: result=%+v error=%v", r.result, r.err)
	}
}

func TestRecursiveRemoveWithoutPolicyReviewerKeepsNestedInstructions(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/AGENTS.md": "instructions", "tree/ordinary": "data"})
	h.tc.Files.FileChangeReview = nil
	r := h.run("", "rm", "-rf", "tree")
	var reject *toolrejection.ToolReject
	if !errors.As(r.err, &reject) || reject.Code != "FILE_CHANGE_APPROVAL_UNAVAILABLE" || r.result.Terminal != toolcontract.ProducerRefused || r.result.EffectCertainty != toolcontract.NoEffect || h.absent("tree") {
		t.Fatalf("nested instructions removed without policy review: result=%+v error=%v", r.result, r.err)
	}
	if got := h.read("tree/AGENTS.md"); got != "instructions" {
		t.Fatalf("instructions changed: %q", got)
	}
}

func TestRecursiveRemoveReviewsNestedInstructionsAndKeepsOneUndo(t *testing.T) {
	for _, deny := range []bool{false, true} {
		t.Run(map[bool]string{false: "approved", true: "declined"}[deny], func(t *testing.T) {
			h := newHarness(t, map[string]string{"tree/AGENTS.md": "instructions", "tree/ordinary": "data"})
			reviewed := false
			declined := errors.New("person declined the instruction change")
			h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
				for _, change := range changes {
					if change.Path != h.path("tree/AGENTS.md") {
						continue
					}
					reviewed = true
					if change.Preview.Operation != "delete" || change.Preview.Before != "instructions" || change.Preview.BeforeBytes != 12 {
						t.Fatalf("instruction review did not carry actual deletion: %+v", change)
					}
					if deny {
						return declined
					}
				}
				return nil
			}
			result := h.run("", "rm", "-rf", "tree")
			if !reviewed {
				t.Fatal("instruction change was not reviewed")
			}
			if deny {
				if !errors.Is(result.err, declined) || h.absent("tree") {
					t.Fatalf("declined tree removed: %+v error=%v", result.result, result.err)
				}
				return
			}
			if result.err != nil || result.result.Status != 0 || !h.absent("tree") {
				t.Fatalf("approved removal=%+v error=%v", result.result, result.err)
			}
			h.undoAll(1)
			if h.read("tree/AGENTS.md") != "instructions" || h.read("tree/ordinary") != "data" {
				t.Fatal("one undo did not restore reviewed tree")
			}
		})
	}
}

type policyChangeBeforeRemoval struct {
	sourceeffect.Journal
	change func()
}

func (j policyChangeBeforeRemoval) RemoveEntry(ctx context.Context, removal sourceeffect.Removal) (string, error) {
	j.change()
	return j.Journal.RemoveEntry(ctx, removal)
}

func TestRecursiveRemoveRevalidatesInstructionsAfterReview(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/AGENTS.md": "instructions", "tree/ordinary": "data"})
	reviewed := false
	h.tc.Files.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		for _, change := range changes {
			if change.Path == h.path("tree/AGENTS.md") {
				reviewed = true
			}
		}
		return nil
	}
	h.tc.Source.SourceMutations = policyChangeBeforeRemoval{Journal: h.service, change: func() {
		if !reviewed {
			t.Fatal("removal started before instruction review")
		}
		testutil.FailErr(t, "edit instructions after approval", os.WriteFile(h.path("tree/AGENTS.md"), []byte("later edit"), 0o644))
	}}
	result := h.run("", "rm", "-rf", "tree")
	if result.err == nil || h.absent("tree") || h.read("tree/AGENTS.md") != "later edit" {
		t.Fatalf("stale instruction approval removed changed tree: %+v error=%v", result.result, result.err)
	}
}

func TestRecursivePolicyReviewUsesAndReleasesInvocationMemory(t *testing.T) {
	for _, limits := range []toolcontract.ProgramMemoryLimits{{Working: 1, State: 64 << 20}, {Working: 32 << 20, State: 1}} {
		h := newHarness(t, map[string]string{"tree/AGENTS.md": "instructions"})
		memory, err := toolcontract.NewProgramMemory(limits)
		testutil.FailErr(t, "constrain invocation memory", err)
		h.program.Memory = memory
		result := h.run("", "rm", "-rf", "tree")
		var limit *toolcontract.ProgramMemoryLimitError
		if !errors.As(result.err, &limit) || h.absent("tree") {
			t.Fatalf("unadmitted policy capture: %+v error=%v", result.result, result.err)
		}
		if use := memory.InUse(); use.Working != 0 || use.State != 0 {
			t.Fatalf("policy review leaked admission: %+v", use)
		}
	}
}

func TestRecursiveRemovalTraversalUsesInvocationState(t *testing.T) {
	for _, depth := range []int{0, 4} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			h := newHarness(t, nil)
			path := "tree"
			for range depth {
				path = filepath.Join(path, "nested")
			}
			testutil.FailErr(t, "seed directories", os.MkdirAll(h.path(path), 0o755))
			testutil.FailErr(t, "seed leaf", os.WriteFile(h.path(filepath.Join(path, "leaf")), []byte("body"), 0o644))
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 512 << 10})
			testutil.FailErr(t, "create invocation budget", err)
			h.program.Memory = memory
			result := h.run("", "rm", "-rf", "tree")
			if depth == 0 {
				if result.err != nil || !h.absent("tree") {
					t.Fatalf("narrow removal: %+v error=%v", result.result, result.err)
				}
				h.undoAll(1)
			} else {
				var limit *toolcontract.ProgramMemoryLimitError
				if !errors.As(result.err, &limit) || limit.Account != "state" || h.absent("tree") {
					t.Fatalf("unadmitted depth changed tree: %+v error=%v", result.result, result.err)
				}
			}
			if h.read(filepath.Join(path, "leaf")) != "body" {
				t.Fatal("leaf bytes changed")
			}
			if use := memory.InUse(); use.Working != 0 || use.State != 0 {
				t.Fatalf("traversal leaked: %+v", use)
			}
		})
	}
}
