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
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/sourceeffect"
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
	h.service.Effects.SetSameFilesystem(func(string, string) (bool, error) { return false, nil })
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
		t.Fatalf("declined cross-filesystem delete should leave tree intact: %+v %v", r.result, r.err)
	}
	if len(notes) != 1 {
		t.Fatalf("expected one permanent-removal review, got %d", len(notes))
	}
}

func TestDirectoryDeletesRespectQuotaAndPruneRetainedTrees(t *testing.T) {
	h := newHarness(t, nil)
	t.Cleanup(func() { testutil.FailErr(t, "close service", h.service.Close()) })
	// Measure the real filesystem allocation before setting this fixture
	// quota. Moved-aside trees must be reclaimed in the background without per-file blob capture.

	for round := 0; round < 5; round++ {
		chunk := fmt.Sprintf("round %d\n", round) + strings.Repeat("retained-build-artifact-line\n", 48)
		for _, dir := range []string{"node_modules/pkg", "dist/assets"} {
			full := h.path(filepath.Join(dir, fmt.Sprintf("bundle-%d.js", round)))
			testutil.FailErr(t, "mkdir "+dir, os.MkdirAll(filepath.Dir(full), 0o755))
			testutil.FailErr(t, "write "+full, os.WriteFile(full, []byte(chunk), 0o644))
		}
		if round == 0 {
			measured, err := h.service.InspectRemoval(t.Context(), fseffect.Location{Root: h.root, Rel: "node_modules"}, h.owners.Revisions.Memory)
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

	testutil.FailErr(t, "wait background reclamation", h.service.WaitReclamation(t.Context()))

	var totalRecoveryBytes int64
	testutil.FailErr(t, "sum recovery bytes", h.db.QueryRowContext(t.Context(), `SELECT COALESCE(SUM(size), 0) FROM source_blob_objects`).Scan(&totalRecoveryBytes))
	if totalRecoveryBytes != 0 {
		t.Fatalf("tree removal captured per-file blobs: %d bytes", totalRecoveryBytes)
	}
	slots, err := os.ReadDir(settingsoverlay.RecoveryDir(h.root))
	testutil.FailErr(t, "inspect retained recovery slots", err)
	if len(slots) == 0 || len(slots) > 2 {
		t.Fatalf("expected physical recovery slots to stay bounded within quota (1..2 slots), got %d", len(slots))
	}
}

func TestOversizeDeleteWithoutReviewerLeavesTreeIntact(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/a": "content"})
	h.service.Effects.SetSameFilesystem(func(string, string) (bool, error) { return false, nil })
	h.tc.Files.FileChangeReview = nil
	r := h.run("", "rm", "-rf", "tree")
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != "SOURCE_RECOVERY_APPROVAL_UNAVAILABLE" || h.absent("tree/a") {
		t.Fatalf("permanent removal escaped review: result=%+v error=%v", r.result, r.err)
	}
}

func TestRecoveryAreaHiddenFromCommandsFilesAndGitExclude(t *testing.T) {
	h := newHarness(t, map[string]string{
		"visible.txt":      "visible-line\n",
		"secret/token.txt": "moved-aside-secret-payload\n",
	})
	t.Cleanup(func() { testutil.FailErr(t, "close service", h.service.Close()) })
	testutil.FailErr(t, "mkdir .git/info", os.MkdirAll(h.path(".git/info"), 0o755))

	if r := h.run("", "rm", "-rf", "secret"); r.err != nil || r.result.Status != 0 || !h.absent("secret") {
		t.Fatalf("rm secret: %+v %v", r.result, r.err)
	}
	slots, err := os.ReadDir(settingsoverlay.RecoveryDir(h.root))
	testutil.FailErr(t, "read recovery dir", err)
	if len(slots) != 1 {
		t.Fatalf("expected 1 recovery slot on disk, got %d", len(slots))
	}
	excludeBytes, err := os.ReadFile(h.path(".git/info/exclude"))
	testutil.FailErr(t, "read .git/info/exclude", err)
	if !strings.Contains(string(excludeBytes), ".paintedwolf/recovery/") {
		t.Fatalf("expected .git/info/exclude to contain .paintedwolf/recovery/, got %q", string(excludeBytes))
	}
	if !h.absent(".gitignore") {
		t.Fatal("recovery exclusion must never touch .gitignore")
	}

	if r := h.run("", "rm", "-rf", ".paintedwolf/recovery"); r.err == nil && r.result.Status == 0 {
		t.Fatalf("rm -rf .paintedwolf/recovery was admitted: %+v", r.result)
	}
	slotsAfterRm, err := os.ReadDir(settingsoverlay.RecoveryDir(h.root))
	testutil.FailErr(t, "re-read recovery dir after rm attempt", err)
	if len(slotsAfterRm) != 1 {
		t.Fatalf("recovery slot was affected by rm attempt: slots=%d", len(slotsAfterRm))
	}
	browse, err := projectsource.BrowseProjectSource(h.project, "", ".")
	testutil.FailErr(t, "browse root", err)
	for _, entry := range browse.Entries {
		if strings.Contains(entry.Name, "recovery") || entry.Name == ".paintedwolf" {
			t.Fatalf("Files browse exposed recovery area: %+v", entry)
		}
	}
	if _, err := projectsource.BrowseProjectSource(h.project, "", ".paintedwolf"); !errors.Is(err, projectsource.ErrSourceNotFound) {
		t.Fatalf("expected .paintedwolf with only recovery to be hidden in Files browse, got %v", err)
	}
	if _, err := projectsource.BrowseProjectSource(h.project, "", ".paintedwolf/recovery"); !errors.Is(err, projectsource.ErrSourceNotFound) {
		t.Fatalf("expected .paintedwolf/recovery to be hidden in Files browse, got %v", err)
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
		h.owners.Revisions.Memory = memory
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
			memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 32 << 20, State: 768 << 10})
			testutil.FailErr(t, "create invocation budget", err)
			h.owners.Revisions.Memory = memory
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

func TestMoveReleasesSourceSnapshotWhenDestinationAdmissionFails(t *testing.T) {
	h := newHarness(t, map[string]string{"source": "new", "destination": "old"})
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 128 << 10, State: 64 << 10})
	testutil.FailErr(t, "bound move snapshots", err)
	h.owners.Revisions.Memory = memory
	result := h.run("", "mv", "source", "destination")
	var limit *toolcontract.ProgramMemoryLimitError
	if !errors.As(result.err, &limit) || limit.Account != "state" {
		t.Fatalf("unadmitted move: %+v error=%v", result.result, result.err)
	}
	if h.read("source") != "new" || h.read("destination") != "old" {
		t.Fatal("refused move changed files")
	}
	if use := memory.InUse(); use.Working != 0 || use.State != 0 {
		t.Fatalf("move retained source after refusal: %+v", use)
	}
}
