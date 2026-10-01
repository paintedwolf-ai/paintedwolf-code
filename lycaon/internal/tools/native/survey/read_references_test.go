package survey

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools/fileage"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

type stubSessionLedger struct {
	ledger evidence.Ledger
}

func (s *stubSessionLedger) LoadLedger(_ context.Context, _ string) (evidence.Ledger, error) {
	return s.ledger, nil
}

func ledgerWithReadPaths(paths ...string) guidance.EvidenceLedgerReader {
	records := make([]evidence.Record, 0, len(paths))
	for i, path := range paths {
		records = append(records, evidence.Record{
			Handle:     evidence.FormatHandle("read", i+1),
			Kind:       "read",
			Shape:      evidence.ShapeFileRegion,
			SourceTool: "read",
			Path:       path,
			LineRanges: []evidence.LineRange{{Start: 1, End: 1}},
			Body:       []string{"ok"},
		})
	}
	return &stubSessionLedger{ledger: evidence.AssembleLedger(records)}
}

// refsRepo creates references with distinct commit dates.
func refsRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gittest.Init(t, dir)
	commitAt := func(rel, body string, when time.Time) {
		testutil.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644))
		gittest.Run(t, dir, "add", rel)
		gittest.CommitAt(t, dir, rel, when)
	}
	commitAt("doc.md", "Contract reconciled with `code.go`. See also [gone](missing.go).\n", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	commitAt("code.go", "package code\n", time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
	commitAt("plain.go", "package plain // mentions nothing linkable\n", time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
	return dir
}

func TestReadAttachesReferences(t *testing.T) {
	dir := refsRepo(t)
	prov := fileage.New(git.NewManager())
	prov.Prewarm(context.Background(), dir)
	tool := &ReadTool{
		Boundary: nativefixture.Boundary(t),
		Age:      prov,
		Ledger:   ledgerWithReadPaths(),
	}

	out, err := tool.Run(context.Background(), map[string]any{"path": "doc.md"}, nativefixture.Context(dir))
	testutil.FailErr(t, "read doc.md", err)
	r, ok := surveyreceipt.Parse(out)
	if !ok {
		t.Fatalf("no receipt: %s", out)
	}
	if r.References == nil {
		t.Fatal("expected references context on a doc that names code")
	}
	if len(r.References.Named) != 1 {
		t.Fatalf("Named = %#v, want exactly code.go (missing.go must drop)", r.References.Named)
	}
	ref := r.References.Named[0]
	if ref.Path != "code.go" {
		t.Fatalf("ref.Path = %q, want code.go", ref.Path)
	}
	if !ref.Newer {
		t.Fatal("code.go (2023) changed after doc.md (2020); Newer should be true")
	}
	if ref.Read == nil || *ref.Read {
		t.Fatalf("code.go should be unread this session, got Read=%v", ref.Read)
	}
	if r.References.NewerCount != 1 {
		t.Fatalf("NewerCount = %d, want 1", r.References.NewerCount)
	}
	if r.References.UnreadCount != 1 {
		t.Fatalf("UnreadCount = %d, want 1", r.References.UnreadCount)
	}
	if !strings.Contains(r.References.Summary, "changed more recently than this file") {
		t.Fatalf("summary = %q", r.References.Summary)
	}
	if !strings.Contains(r.References.Summary, "not read this session") {
		t.Fatalf("summary = %q, want unread fact", r.References.Summary)
	}
	if low := strings.ToLower(r.References.Summary); strings.Contains(low, "stale") || strings.Contains(low, "outdated") {
		t.Fatalf("summary must not editorialize: %q", r.References.Summary)
	}
}

func TestReadReferencesMarksSessionRead(t *testing.T) {
	dir := refsRepo(t)
	prov := fileage.New(git.NewManager())
	prov.Prewarm(context.Background(), dir)
	tool := &ReadTool{
		Boundary: nativefixture.Boundary(t),
		Age:      prov,
		Ledger:   ledgerWithReadPaths("code.go"),
	}

	out, err := tool.Run(context.Background(), map[string]any{"path": "doc.md"}, nativefixture.Context(dir))
	testutil.FailErr(t, "read doc.md", err)
	r, ok := surveyreceipt.Parse(out)
	if !ok {
		t.Fatalf("no receipt: %s", out)
	}
	if r.References == nil || len(r.References.Named) != 1 {
		t.Fatalf("References = %#v", r.References)
	}
	ref := r.References.Named[0]
	if ref.Read == nil || !*ref.Read {
		t.Fatalf("code.go should be marked read this session, got Read=%v", ref.Read)
	}
	if r.References.UnreadCount != 0 {
		t.Fatalf("UnreadCount = %d, want 0", r.References.UnreadCount)
	}
	if strings.Contains(r.References.Summary, "not read this session") {
		t.Fatalf("summary should omit unread fact when all named refs were read: %q", r.References.Summary)
	}
}

func TestReadAttachesReferencesBackslashPaths(t *testing.T) {
	dir := refsRepo(t)
	subdir := filepath.Join(dir, "pkg")
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(subdir, 0o755))
	testutil.FailErr(t, "write pkg/code.go", os.WriteFile(filepath.Join(subdir, "code.go"), []byte("package pkg\n"), 0o644))
	gittest.Run(t, dir, "add", "pkg/code.go")
	gittest.CommitAt(t, dir, "pkg", time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC))
	testutil.FailErr(t, "write doc-win", os.WriteFile(filepath.Join(dir, "doc-win.md"), []byte("Uses `pkg\\code.go`.\n"), 0o644))

	prov := fileage.New(git.NewManager())
	prov.Prewarm(context.Background(), dir)
	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Age: prov}

	out, err := tool.Run(context.Background(), map[string]any{"path": "doc-win.md"}, nativefixture.Context(dir))
	testutil.FailErr(t, "read doc-win.md", err)
	r, ok := surveyreceipt.Parse(out)
	if !ok || r.References == nil || len(r.References.Named) != 1 {
		t.Fatalf("references = %#v ok=%v", r.References, ok)
	}
	if r.References.Named[0].Path != "pkg/code.go" {
		t.Fatalf("ref.Path = %q, want pkg/code.go", r.References.Named[0].Path)
	}
}

func TestReadCodeReferencesNothing(t *testing.T) {
	dir := refsRepo(t)
	prov := fileage.New(git.NewManager())
	prov.Prewarm(context.Background(), dir)
	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Age: prov}

	out, err := tool.Run(context.Background(), map[string]any{"path": "plain.go"}, nativefixture.Context(dir))
	testutil.FailErr(t, "read plain.go", err)
	r, ok := surveyreceipt.Parse(out)
	if !ok {
		t.Fatalf("no receipt: %s", out)
	}
	if r.References != nil {
		t.Fatalf("code that references nothing should carry no references, got %#v", r.References)
	}
}
