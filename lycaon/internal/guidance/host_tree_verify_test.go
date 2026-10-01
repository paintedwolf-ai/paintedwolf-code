package guidance_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/guidance/ledgertest"
	"github.com/lycaon/lycaon/internal/projectroot"
)

func writeTreeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// A worker that surveyed a file via outline read / list_dir map / truncated grep has no
// citable body in the ledger, but a verbatim excerpt it quotes is still true on disk.
func TestHostVerifyGroundsTrueCitationWithoutSessionEvidence(t *testing.T) {
	dir := t.TempDir()
	writeTreeFile(t, dir, "internal/foo/bar.go", "package foo\n\nfunc Bar() error {\n\treturn nil\n}\n")

	ev := ledgertest.BuildFromMessages(dir, nil) // empty ledger: nothing captured in-session
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{ProjectDir: dir}, []guidance.WorkerFindingInput{
		{Path: "internal/foo/bar.go", Line: 3, Excerpt: "func Bar() error {"},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != "" {
		t.Fatalf("eval.Code = %q want grounded via working tree", eval.Code)
	}
	if len(eval.Resolutions) != 1 || eval.Resolutions[0].Verdict != evidence.VerdictMatched {
		t.Fatalf("resolved = %+v want matched", eval.Resolutions)
	}
}

// The tree check is verbatim: an excerpt that is not on disk stays unverifiable, so
// host verification cannot launder a fabricated citation.
func TestHostVerifyRejectsFabricatedExcerpt(t *testing.T) {
	dir := t.TempDir()
	writeTreeFile(t, dir, "internal/foo/bar.go", "package foo\n\nfunc Bar() error {\n\treturn nil\n}\n")

	ev := ledgertest.BuildFromMessages(dir, nil)
	eval := guidance.EvaluateWorkerCitations(evidence.CitationRoots{ProjectDir: dir}, []guidance.WorkerFindingInput{
		{Path: "internal/foo/bar.go", Line: 3, Excerpt: `func Baz() { panic("never written") }`},
	}, nil, guidance.WorkerNarrativeInput{}, ev)
	if eval.Code != guidance.WorkerEvidenceHandleUnknownCode {
		t.Fatalf("eval.Code = %q want WORKER_EVIDENCE_HANDLE_UNKNOWN for fabricated excerpt", eval.Code)
	}
}

func TestHostVerifyFindingAgainstTreeContainment(t *testing.T) {
	dir := t.TempDir()
	writeTreeFile(t, dir, "in.go", "package in\n\nconst Secret = 1\n")

	if !guidance.HostVerifyFindingAgainstTree(evidence.CitationRoots{ProjectDir: dir}, "in.go", 3, "const Secret = 1") {
		t.Fatal("expected in-project true excerpt to verify")
	}
	if guidance.HostVerifyFindingAgainstTree(evidence.CitationRoots{ProjectDir: dir}, "../escape.go", 1, "const Secret = 1") {
		t.Fatal("expected path escape to be refused")
	}
	if guidance.HostVerifyFindingAgainstTree(evidence.CitationRoots{}, "in.go", 3, "const Secret = 1") {
		t.Fatal("expected empty projectDir to disable tree verify")
	}
	if guidance.HostVerifyFindingAgainstTree(evidence.CitationRoots{ProjectDir: dir}, "in.go", 99, "const Secret = 1") {
		t.Fatal("expected a line past end-of-file to be refused")
	}
}

func TestHostVerifyFindingAgainstTree_WrongLineFails(t *testing.T) {
	dir := t.TempDir()
	writeTreeFile(t, dir, "in.go", "package in\n\n// gap\n\nconst Secret = 1\n")

	if !guidance.HostVerifyFindingAgainstTree(evidence.CitationRoots{ProjectDir: dir}, "in.go", 5, "const Secret = 1") {
		t.Fatal("expected true excerpt at cited line to verify")
	}
	if guidance.HostVerifyFindingAgainstTree(evidence.CitationRoots{ProjectDir: dir}, "in.go", 1, "const Secret = 1") {
		t.Fatal("expected wrong line to fail line-bound verification")
	}
}

func TestHostVerifyFindingAgainstTree_MultiRootLabel(t *testing.T) {
	primary := t.TempDir()
	secondary := t.TempDir()
	writeTreeFile(t, secondary, "in.go", "package in\n\nconst Secret = 1\n")
	roots := []projectroot.RootRef{
		{ID: "p", Label: "primary", Path: primary, IsPrimary: true},
		{ID: "s", Label: "side", Path: secondary, IsPrimary: false},
	}
	pathRoots := evidence.CitationRoots{Roots: roots, ActiveRootID: "p", ProjectDir: primary}
	if !guidance.HostVerifyFindingAgainstTree(pathRoots, "@side/in.go", 3, "const Secret = 1") {
		t.Fatal("expected @label path to verify against secondary root")
	}
	if guidance.HostVerifyFindingAgainstTree(evidence.CitationRoots{ProjectDir: primary}, "@side/in.go", 3, "const Secret = 1") {
		t.Fatal("single-root HostVerify must not treat @label as a primary subdirectory")
	}
}
