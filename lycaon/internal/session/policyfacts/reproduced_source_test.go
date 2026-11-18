package policyfacts

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/touchedpaths"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type recordedOpen struct {
	rootID   string
	relPath  string
	maxBytes int
}

type fakeReproducedDeps struct {
	roots       []projectroot.RootRef
	rootsCalls  int
	ledger      evidence.Ledger
	ledgerCalls int
	authored    map[string][]string
	files       map[string][]byte
	opens       []recordedOpen
}

func (f *fakeReproducedDeps) Get(context.Context, string) (*api.Session, error) {
	return nil, nil
}

func (f *fakeReproducedDeps) SettingsPath(context.Context, *api.Session) string {
	return ""
}

func (f *fakeReproducedDeps) Roots(context.Context, *api.Session) ([]projectroot.RootRef, error) {
	f.rootsCalls++
	return f.roots, nil
}

func newTestReproducedService(deps *fakeReproducedDeps, withAuthorship bool, reader candidateReader) *Service {
	svc := New(nil)
	svc.SetSources(deps, deps, deps, nil, nil)
	if withAuthorship {
		svc.SetSourceAuthorship(deps)
	}
	svc.candidateReader = reader
	return svc
}

func (f *fakeReproducedDeps) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	f.ledgerCalls++
	return f.ledger, nil
}

func (f *fakeReproducedDeps) SessionAuthoredPaths(_ context.Context, _, _, rootID string) ([]string, error) {
	return f.authored[rootID], nil
}

func (f *fakeReproducedDeps) Read(_ context.Context, root projectroot.RootRef, relPath string, maxBytes int) ([]byte, bool, error) {
	f.opens = append(f.opens, recordedOpen{
		rootID:   root.ID,
		relPath:  relPath,
		maxBytes: maxBytes,
	})
	raw, ok := f.files[root.ID+":"+relPath]
	if !ok {
		return nil, false, nil
	}
	return raw, true, nil
}

// Candidates are deduplicated session-touched (rootID, relPath) pairs; untouched files stay unopened.
func TestReproducedSourceOpensOnlyDeduplicatedTouchedCandidates(t *testing.T) {
	untouchedBody := strings.Join([]string{
		"package untouched",
		"func SecretUntouchedHelper() error {",
		"\tfor i := 0; i < 10; i++ {",
		"\t\tprocessUntouchedItem(i)",
		"\t}",
		"\treturn nil",
		"}",
	}, "\n")
	guideBody := strings.Join([]string{
		"# Architecture",
		"",
		"```go",
		"func BuildRouter(cfg RouterConfig) *Router {",
		"\tr := newBaseRouter(cfg)",
		"\tr.MountHealthChecks()",
		"\treturn r",
		"}",
		"```",
	}, "\n")

	deps := &fakeReproducedDeps{
		roots: []projectroot.RootRef{
			{ID: "r1", Label: "app", Path: "/ws/app", IsPrimary: true},
			{ID: "r2", Label: "docs", Path: "/ws/docs"},
		},
		authored: map[string][]string{
			"r1": {"cmd/main.go"},
			"r2": {"guide.md"},
		},
		ledger: evidence.AssembleLedger([]evidence.Record{
			{Handle: "read#1", Kind: "read", RootID: "r2", Path: "guide.md"},
		}),
		files: map[string][]byte{
			"r1:untouched.go": []byte(untouchedBody),
			"r1:cmd/main.go":  []byte("package main\nfunc main() {}\n"),
			"r2:guide.md":     []byte(guideBody),
		},
	}

	svc := newTestReproducedService(deps, true, deps.Read)
	sess := &api.Session{ID: "sess-1", ProjectID: "proj-1", WorkspaceRootID: "r1"}

	answer := strings.Join([]string{
		"```go",
		"func SecretUntouchedHelper() error {",
		"\tfor i := 0; i < 10; i++ {",
		"\t\tprocessUntouchedItem(i)",
		"\t}",
		"\treturn nil",
		"}",
		"```",
		"",
		"```go",
		"func BuildRouter(cfg RouterConfig) *Router {",
		"\tr := newBaseRouter(cfg)",
		"\tr.MountHealthChecks()",
		"\treturn r",
		"}",
		"```",
	}, "\n")

	gc := oar.NewGuardContext()
	svc.RegisterReproducedSource(t.Context(), gc, sess, answer)

	for _, name := range reproducedSourceFactNames {
		testutil.FailErr(t, "ensure "+name, gc.Ensure(name))
	}

	if len(deps.opens) != 2 {
		t.Fatalf("opens = %#v, want 2 deduplicated touched candidates", deps.opens)
	}
	if deps.opens[0] != (recordedOpen{rootID: "r1", relPath: "cmd/main.go", maxBytes: MaxReproducedCandidateBytes}) {
		t.Fatalf("opens[0] = %+v", deps.opens[0])
	}
	if deps.opens[1] != (recordedOpen{rootID: "r2", relPath: "guide.md", maxBytes: MaxReproducedCandidateBytes}) {
		t.Fatalf("opens[1] = %+v", deps.opens[1])
	}
	if gc.Source.ReproducedSourcePath != "@docs/guide.md" {
		t.Fatalf("ReproducedSourcePath = %q, want @docs/guide.md", gc.Source.ReproducedSourcePath)
	}
	if gc.Source.ReproducedSourceLines != 5 || gc.Source.ReproducedSourceStartLine != 4 || gc.Source.ReproducedSourceEndLine != 8 {
		t.Fatalf("Source facts = %+v", gc.Source)
	}
}

// Measurement reads nothing without a rule reference or measurable fence, and caps at 64 files / 256 KiB.
func TestReproducedSourceShortCircuitsAndEnforcesCaps(t *testing.T) {
	t.Run("unreferenced_facts_never_run_provider", func(t *testing.T) {
		deps := &fakeReproducedDeps{
			roots:  []projectroot.RootRef{{ID: "r1", Label: "app", Path: "/ws/app", IsPrimary: true}},
			ledger: evidence.AssembleLedger([]evidence.Record{{Handle: "read#1", Kind: "read", RootID: "r1", Path: "a.go"}}),
			files:  map[string][]byte{"r1:a.go": []byte("package a\nfunc AlphaWork() {}\n")},
		}
		svc := newTestReproducedService(deps, true, deps.Read)
		sess := &api.Session{ID: "sess-1", ProjectID: "proj-1", WorkspaceRootID: "r1"}

		rule := &oar.Rule{
			SpecVersion: "1.0",
			ID:          "UNRELATED_POST_TURN",
			Kind:        oar.KindPolicy,
			Anchor:      oar.AnchorCoordinatorPostTurn,
			Effect:      oar.EffectNudge,
			When:        "paintedwolf.workers_idle == false",
			Enforcement: "enforce",
		}
		pipeline := oar.NewGuardPipeline(oar.NewRuleSet([]*oar.Rule{rule}), nil, oar.NewCounterStore())
		pipeline.EnableAnchor(oar.AnchorCoordinatorPostTurn)

		gc := oar.NewGuardContext()
		gc.Workers.WorkersIdle = true
		svc.RegisterReproducedSource(t.Context(), gc, sess, "```go\npackage a\nfunc AlphaWork() {}\n```\n")

		_, err := pipeline.EvaluateBlock(t.Context(), oar.AnchorCoordinatorPostTurn, gc)
		testutil.FailErr(t, "evaluate unrelated rule", err)

		if len(deps.opens) != 0 || deps.rootsCalls != 0 || deps.ledgerCalls != 0 {
			t.Fatalf("provider ran without fact reference: opens=%d roots=%d ledger=%d", len(deps.opens), deps.rootsCalls, deps.ledgerCalls)
		}
	})

	t.Run("fenceless_or_excluded_fence_answer_reads_zero_files", func(t *testing.T) {
		for _, content := range []string{
			"Plain prose answer with `inlineCode()` and no fences.",
			"```diff\n-oldLine()\n+newLine()\n```\n",
			"Done.\n\n```json\n{\"summary\":\"ok\"}\n```\n",
		} {
			deps := &fakeReproducedDeps{
				roots:  []projectroot.RootRef{{ID: "r1", Label: "app", Path: "/ws/app", IsPrimary: true}},
				ledger: evidence.AssembleLedger([]evidence.Record{{Handle: "read#1", Kind: "read", RootID: "r1", Path: "a.go"}}),
				files:  map[string][]byte{"r1:a.go": []byte("package a\nfunc AlphaWork() {}\n")},
			}
			svc := newTestReproducedService(deps, true, deps.Read)
			sess := &api.Session{ID: "sess-1", ProjectID: "proj-1", WorkspaceRootID: "r1"}

			gc := oar.NewGuardContext()
			svc.RegisterReproducedSource(t.Context(), gc, sess, content)
			testutil.FailErr(t, "ensure lines", gc.Ensure("paintedwolf.reproduced_source_lines"))

			if len(deps.opens) != 0 || deps.rootsCalls != 0 || deps.ledgerCalls != 0 {
				t.Fatalf("content %q triggered I/O: opens=%d roots=%d ledger=%d", content, len(deps.opens), deps.rootsCalls, deps.ledgerCalls)
			}
		}
	})

	t.Run("caps_at_64_files_and_256_kib_prefix", func(t *testing.T) {
		var records []evidence.Record
		files := make(map[string][]byte, 90)
		for i := range 89 {
			rel := fmt.Sprintf("pkg/f%02d.go", i)
			records = append(records, evidence.Record{
				Handle: fmt.Sprintf("read#%d", i+1),
				Kind:   "read",
				RootID: "r1",
				Path:   rel,
			})
			files["r1:"+rel] = []byte(fmt.Sprintf("package pkg\nfunc FileFunc%02d() {}\n", i))
		}

		// Build a 400 KiB file with a distinct block near the top and another past 256 KiB.
		var big bytes.Buffer
		big.WriteString("package huge\nfunc EarlyPrefixMatch() error {\n\treturn runEarlyPrefix()\n}\n")
		for big.Len() < MaxReproducedCandidateBytes+32*1024 {
			big.WriteString("// filler padding line to push tail past 256 KiB cap\n")
		}
		big.WriteString("func BeyondCapTailMatch() error {\n\treturn runBeyondCapTail()\n}\n")

		records = append(records, evidence.Record{
			Handle: "read#90",
			Kind:   "read",
			RootID: "r1",
			Path:   "pkg/huge.go",
		})
		files["r1:pkg/huge.go"] = big.Bytes()

		deps := &fakeReproducedDeps{
			roots:  []projectroot.RootRef{{ID: "r1", Label: "app", Path: "/ws/app", IsPrimary: true}},
			ledger: evidence.AssembleLedger(records),
			files:  files,
		}
		svc := newTestReproducedService(deps, true, deps.Read)
		sess := &api.Session{ID: "sess-1", ProjectID: "proj-1", WorkspaceRootID: "r1"}

		// Match inside the first 256 KiB succeeds while opening at most 64 files.
		gcHead := oar.NewGuardContext()
		svc.RegisterReproducedSource(t.Context(), gcHead, sess, "```go\nfunc EarlyPrefixMatch() error {\n\treturn runEarlyPrefix()\n}\n```\n")
		testutil.FailErr(t, "ensure head match", gcHead.Ensure("paintedwolf.reproduced_source_lines"))
		if len(deps.opens) != touchedpaths.MaxCandidates {
			t.Fatalf("opens = %d, want %d", len(deps.opens), touchedpaths.MaxCandidates)
		}
		for _, op := range deps.opens {
			if op.maxBytes != MaxReproducedCandidateBytes {
				t.Fatalf("maxBytes = %d, want %d", op.maxBytes, MaxReproducedCandidateBytes)
			}
		}
		if gcHead.Source.ReproducedSourceLines != 3 || gcHead.Source.ReproducedSourcePath != "pkg/huge.go" {
			t.Fatalf("head match facts = %+v", gcHead.Source)
		}

		// Lines strictly past 256 KiB are outside the read slice and do not match.
		gcTail := oar.NewGuardContext()
		svc.RegisterReproducedSource(t.Context(), gcTail, sess, "```go\nfunc BeyondCapTailMatch() error {\n\treturn runBeyondCapTail()\n}\n```\n")
		testutil.FailErr(t, "ensure tail match", gcTail.Ensure("paintedwolf.reproduced_source_lines"))
		if gcTail.Source.ReproducedSourceLines != 0 || gcTail.Source.ReproducedSourcePath != "" {
			t.Fatalf("tail beyond 256 KiB matched: %+v", gcTail.Source)
		}
	})
}

func TestReadConfinedCandidateReadsPrefixAndSkipsOutsideSymlinks(t *testing.T) {
	wsDir := t.TempDir()
	outsideDir := t.TempDir()
	outsideSecret := filepath.Join(outsideDir, "outside.go")
	testutil.FailErr(t, "write outside file", os.WriteFile(outsideSecret, []byte("package out\nfunc OutsideLeak() {\n\trunOutsideLeak()\n}\n"), 0o600))
	testutil.FailErr(t, "symlink outside", os.Symlink(outsideSecret, filepath.Join(wsDir, "escape.go")))
	testutil.FailErr(t, "mkdir subdir", os.MkdirAll(filepath.Join(wsDir, "subdir"), 0o755))

	var realFile bytes.Buffer
	realFile.WriteString("package local\nfunc ConfinedMatch() error {\n\treturn verifyConfinedRead()\n}\n")
	for realFile.Len() < MaxReproducedCandidateBytes+8192 {
		realFile.WriteString("// local padding line\n")
	}
	testutil.FailErr(t, "write local file", os.WriteFile(filepath.Join(wsDir, "local.go"), realFile.Bytes(), 0o600))

	deps := &fakeReproducedDeps{
		roots: []projectroot.RootRef{{ID: "r1", Label: "app", Path: wsDir, IsPrimary: true}},
		ledger: evidence.AssembleLedger([]evidence.Record{
			{Handle: "read#1", Kind: "read", RootID: "r1", Path: "missing.go"},
			{Handle: "read#2", Kind: "read", RootID: "r1", Path: "subdir"},
			{Handle: "read#3", Kind: "read", RootID: "r1", Path: "escape.go"},
			{Handle: "read#4", Kind: "read", RootID: "r1", Path: "local.go"},
		}),
	}
	svc := newTestReproducedService(deps, false, nil)
	sess := &api.Session{ID: "sess-1", ProjectID: "proj-1", WorkspaceRootID: "r1"}

	gcEscape := oar.NewGuardContext()
	svc.RegisterReproducedSource(t.Context(), gcEscape, sess, "```go\nfunc OutsideLeak() {\n\trunOutsideLeak()\n}\n```\n")
	testutil.FailErr(t, "ensure escape", gcEscape.Ensure("paintedwolf.reproduced_source_lines"))
	if gcEscape.Source.ReproducedSourceLines != 0 {
		t.Fatalf("escaping symlink was read: %+v", gcEscape.Source)
	}

	gcLocal := oar.NewGuardContext()
	svc.RegisterReproducedSource(t.Context(), gcLocal, sess, "```go\nfunc ConfinedMatch() error {\n\treturn verifyConfinedRead()\n}\n```\n")
	testutil.FailErr(t, "ensure local", gcLocal.Ensure("paintedwolf.reproduced_source_lines"))
	if gcLocal.Source.ReproducedSourceLines != 3 || gcLocal.Source.ReproducedSourcePath != "local.go" {
		t.Fatalf("local confined match = %+v", gcLocal.Source)
	}
}

func BenchmarkMeasureReproducedSourceAtCap(b *testing.B) {
	wsDir := b.TempDir()
	records := make([]evidence.Record, 0, touchedpaths.MaxCandidates)
	for i := range touchedpaths.MaxCandidates {
		var buf bytes.Buffer
		fmt.Fprintf(&buf, "package bench\nfunc Candidate%02dStart() error {\n\treturn step%02d()\n}\n", i, i)
		for line := 0; buf.Len() < MaxReproducedCandidateBytes; line++ {
			fmt.Fprintf(&buf, "func filler%02d_%04d(x int) int { return x + %d }\n", i, line, line)
		}
		rel := fmt.Sprintf("file%02d.go", i)
		if err := os.WriteFile(filepath.Join(wsDir, rel), buf.Bytes()[:MaxReproducedCandidateBytes], 0o600); err != nil {
			testutil.FailErr(b, "write file", err)
		}
		records = append(records, evidence.Record{
			Handle: fmt.Sprintf("read#%d", i+1),
			Kind:   "read",
			RootID: "r1",
			Path:   rel,
		})
	}
	deps := &fakeReproducedDeps{
		roots:  []projectroot.RootRef{{ID: "r1", Label: "app", Path: wsDir, IsPrimary: true}},
		ledger: evidence.AssembleLedger(records),
	}
	svc := newTestReproducedService(deps, false, nil)
	sess := &api.Session{ID: "sess-1", ProjectID: "proj-1", WorkspaceRootID: "r1"}
	answer := "Summary.\n\n```go\nfunc Candidate00Start() error {\n\treturn step00()\n}\n```\n"

	b.ResetTimer()
	for range b.N {
		res := svc.measureReproducedSource(context.Background(), sess, answer)
		if res.Lines != 3 {
			b.Fatalf("res = %+v", res)
		}
	}
}
