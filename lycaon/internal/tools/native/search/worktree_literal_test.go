package search

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/textfile"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

type contentSearchRecords struct {
	mu      sync.Mutex
	records map[string]map[string]int64
}

func (h *contentSearchRecords) Enabled(context.Context, slog.Level) bool { return true }
func (h *contentSearchRecords) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *contentSearchRecords) WithGroup(string) slog.Handler            { return h }
func (h *contentSearchRecords) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "command content search completed" {
		return nil
	}
	var execution string
	values := map[string]int64{}
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "execution_id" {
			execution = attr.Value.String()
		} else if attr.Value.Kind() == slog.KindInt64 {
			values[attr.Key] = attr.Value.Int64()
		}
		return true
	})
	h.mu.Lock()
	h.records[execution] = values
	h.mu.Unlock()
	return nil
}

func contentSearchCapture(t *testing.T) *contentSearchRecords {
	t.Helper()
	previous := slog.Default()
	h := &contentSearchRecords{records: map[string]map[string]int64{}}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return h
}

type draftTestDocuments struct {
	docs map[string]tools.EditorDocumentText
}

func (d *draftTestDocuments) FreezeAgentReads(string, string) map[string]documentcore.ReadBasis {
	return nil
}
func (d *draftTestDocuments) RememberAgentRead(string, string, tools.EditorDocumentText) {}
func (d *draftTestDocuments) RememberAgentReadSpans(string, string, tools.EditorDocumentText, []documentcore.LineSpan) {
}
func (d *draftTestDocuments) AdvanceAgentRead(string, string, *tools.AgentReadBases, tools.EditorDocumentEdit, tools.EditorDocumentText) {
}
func (d *draftTestDocuments) AgentReadBase(context.Context, string, string, string, *tools.AgentReadBases) (tools.EditorDocumentText, error) {
	return tools.EditorDocumentText{}, tools.ErrEditorReadRequired
}
func (d *draftTestDocuments) PreviewAgentEdits(context.Context, string, []tools.EditorDocumentEdit) ([]tools.EditorDocumentPreview, error) {
	return nil, nil
}
func (d *draftTestDocuments) OpenDocument(_ context.Context, _ string, _ sourcebranch.ID, rootID, path string) (tools.EditorDocumentText, bool, error) {
	doc, ok := d.docs[rootID+"/"+path]
	return doc, ok, nil
}
func (d *draftTestDocuments) VisitUnsavedDocuments(_ context.Context, _ string, _ sourcebranch.ID, rootID, path string, descendants bool, visit func(tools.EditorDocumentState) error) error {
	for key, doc := range d.docs {
		if !doc.Dirty {
			continue
		}
		prefix := rootID + "/"
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rel := strings.TrimPrefix(key, prefix)
		if !descendants && rel != path {
			continue
		}
		if descendants && path != "" && rel != path && !strings.HasPrefix(rel, path+"/") {
			continue
		}
		if err := visit(tools.EditorDocumentState{ID: doc.ID, RootID: rootID, Path: rel, Dirty: true}); err != nil {
			return err
		}
	}
	return nil
}
func (d *draftTestDocuments) RetainRevision(context.Context, string, string, int64) error { return nil }
func (d *draftTestDocuments) SaveForAgentOperation(context.Context, string, sourcebranch.ID, string, string, bool, tools.EditorAgentCall) error {
	return nil
}
func (d *draftTestDocuments) SaveForAgentLaunch(context.Context, string, sourcebranch.ID, tools.EditorAgentCall, func(string, int64) bool) (tools.EditorLaunchSave, error) {
	return tools.EditorLaunchSave{}, nil
}
func (d *draftTestDocuments) DirtyDocuments(_ context.Context, _ string, _ sourcebranch.ID) ([]tools.EditorDocumentPath, error) {
	return nil, nil
}
func (d *draftTestDocuments) VisitAgentPublications(context.Context, string, sourcebranch.ID, string, int, string, func(tools.EditorDocumentPublicationCandidate) error) error {
	return nil
}
func (d *draftTestDocuments) ApplyAgentEdit(context.Context, string, tools.EditorDocumentEdit) (tools.EditorDocumentApplied, error) {
	return tools.EditorDocumentApplied{}, nil
}
func (d *draftTestDocuments) ApplyAgentEdits(context.Context, string, []tools.EditorDocumentEdit) ([]tools.EditorDocumentApplied, error) {
	return nil, nil
}

func TestWorktreeLiteralLearningPrunesNegativesAndPreservesSemantics(t *testing.T) {
	stats := contentSearchCapture(t)
	h := newHarness(t, map[string]string{"main.go": "package main\n"})
	initGitCheckout(t, h.root, nil)

	extBase := externalNonTempDir(t)
	wt1 := filepath.Join(extBase, "wt-1")
	wt2 := filepath.Join(extBase, "wt-2")
	gitCmd(t, h.root, "worktree", "add", "-q", "-b", "feat/lit-1", wt1)
	gitCmd(t, h.root, "worktree", "add", "-q", "-b", "feat/lit-2", wt2)
	installTreeGrants(t, wt1, wt2)

	corpus := map[string]string{
		"src/match_a.go":      "package src\n// AlphaUniqueNeedle\n// SharedSecondQuery\n",
		"src/deep/match_b.go": "package deep\n// AlphaUniqueNeedle\n",
		"src/.env":            "SECRET_KEY=protected-value\n",
	}
	for i := range 25 {
		corpus[fmt.Sprintf("src/neg_%02d.go", i)] = fmt.Sprintf("package src\n// filler negative %02d without target grams\n", i)
	}
	seed(t, wt1, corpus)
	seed(t, wt2, map[string]string{
		"src/match_a.go": "package src\n// SiblingTwoOnlyToken\n",
	})

	catalog := discoveryCatalog(t)
	h.owners.Catalog = catalog
	h.cwd = wt1

	// Direct named file (depth 0) must not count as a recursive eligible file.
	direct := h.exec("", "grep", "AlphaUniqueNeedle", "src/match_a.go")
	direct.expect(t, "direct named file", "// AlphaUniqueNeedle\n", "", 0)
	if stats.records[direct.execution]["eligible_files"] != 0 {
		t.Fatalf("direct file counted as recursive eligible: %v", stats.records[direct.execution])
	}

	// Cold recursive search learns all 27 eligible files while leaving .env unlearned.
	cold := h.exec("", "grep", "-rn", "AlphaUniqueNeedle", "src")
	wantLines := "src/deep/match_b.go:2:// AlphaUniqueNeedle\nsrc/match_a.go:2:// AlphaUniqueNeedle\n"
	cold.expect(t, "cold recursive grep", wantLines, "", 0)
	coldStats := stats.records[cold.execution]
	if coldStats["eligible_files"] != 27 || coldStats["learned_files"] != 27 || coldStats["pruned_files"] != 0 || coldStats["captured_files"] != 28 {
		t.Fatalf("unexpected cold stats: %v", coldStats)
	}

	// Warm recursive search prunes the known negatives and emits identical pins and output.
	warm := h.exec("", "grep", "-rn", "AlphaUniqueNeedle", "src")
	warm.expect(t, "warm recursive grep", wantLines, "", 0)
	warmStats := stats.records[warm.execution]
	if warmStats["eligible_files"] != 27 || warmStats["pruned_files"] != 25 || warmStats["captured_files"] != 3 || warmStats["learned_files"] != 0 {
		t.Fatalf("unexpected warm stats: %v", warmStats)
	}
	warmPins := h.pins.paths(warm.execution)
	if !slices.Contains(warmPins, filepath.Join(wt1, "src/match_a.go")) || !slices.Contains(warmPins, filepath.Join(wt1, "src/deep/match_b.go")) {
		t.Fatalf("warm pins missing matched files: %v", warmPins)
	}

	// Second query reuses learned filters without recapturing negatives.
	second := h.exec("", "grep", "-rl", "SharedSecondQuery", "src")
	second.expect(t, "second query on warm checkout", "src/match_a.go\n", "", 0)
	if stats.records[second.execution]["pruned_files"] < 25 {
		t.Fatalf("second query did not prune known negatives: %v", stats.records[second.execution])
	}

	// Mode parity across count, files-without-match, fixed-string, and rg.
	for _, argv := range [][]string{
		{"grep", "-rc", "AlphaUniqueNeedle", "src"},
		{"grep", "-rL", "AlphaUniqueNeedle", "src"},
		{"grep", "-rF", "AlphaUniqueNeedle", "src"},
		{"rg", "AlphaUniqueNeedle", "src"},
		{"rg", "-c", "AlphaUniqueNeedle", "src"},
	} {
		h.owners.Catalog = nil
		liveRun := h.exec("", argv...)
		h.owners.Catalog = catalog
		idxRun := h.exec("", argv...)
		if liveRun.stdout != idxRun.stdout || liveRun.stderr != idxRun.stderr || liveRun.result != idxRun.result {
			t.Fatalf("mode %v diverged:\nlive %+v\nidx  %+v", argv, liveRun, idxRun)
		}
	}

	// Divergent sibling checkout wt2 uses its own physical checkout coordinates.
	h.cwd = wt2
	wt2None := h.exec("", "grep", "-rl", "AlphaUniqueNeedle", "src")
	wt2None.expect(t, "wt2 has no wt1 token", "", "", 1)
	wt2Own := h.exec("", "grep", "-rl", "SiblingTwoOnlyToken", "src")
	wt2Own.expect(t, "wt2 finds its own token", "src/match_a.go\n", "", 0)
}

func TestWorktreeLiteralRevisionCorrectnessAndEditorDrafts(t *testing.T) {
	stats := contentSearchCapture(t)
	h := newHarness(t, map[string]string{"main.go": "package main\n"})
	initGitCheckout(t, h.root, nil)

	extBase := externalNonTempDir(t)
	wt := filepath.Join(extBase, "wt-rev")
	gitCmd(t, h.root, "worktree", "add", "-q", "-b", "feat/lit-rev", wt)
	installTreeGrants(t, wt)

	origText := "package src\n// ZZZZ_PLACEHOLDER_TOKEN_HERE\n"
	editText := "package src\n// AlphaMatchTokenInsertedHere\n"
	if len(origText) != len(editText) {
		t.Fatalf("fixture lengths must match: %d vs %d", len(origText), len(editText))
	}
	seed(t, wt, map[string]string{
		"src/inplace.go":  origText,
		"src/atomic.go":   origText,
		"src/hardlink.go": origText,
		"src/draft.go":    origText,
	})

	catalog := discoveryCatalog(t)
	h.owners.Catalog = catalog
	h.cwd = wt

	// Prime filters on cold pass.
	h.exec("", "grep", "-rl", "AlphaMatchTokenInsertedHere", "src").expect(t, "cold none", "", "", 1)

	// 1. In-place edit with identical size and restored mtime.
	inplacePath := filepath.Join(wt, "src/inplace.go")
	info, err := os.Stat(inplacePath)
	testutil.FailErr(t, "stat inplace", err)
	testutil.FailErr(t, "write inplace", os.WriteFile(inplacePath, []byte(editText), 0o644))
	testutil.FailErr(t, "restore mtime", os.Chtimes(inplacePath, info.ModTime(), info.ModTime()))
	h.exec("", "grep", "-rl", "AlphaMatchTokenInsertedHere", "src").expect(t, "in-place same size restored mtime", "src/inplace.go\n", "", 0)

	// 2. Atomic replacement.
	atomicPath := filepath.Join(wt, "src/atomic.go")
	testutil.FailErr(t, "atomic replace", fseffect.WriteFile(atomicPath, []byte(editText), 0o644))
	h.exec("", "grep", "-rl", "AlphaMatchTokenInsertedHere", "src").expect(t, "atomic replace", "src/atomic.go\nsrc/inplace.go\n", "", 0)

	// 3. Hard-link then modify (Links changes from 1 to 2, invalidating FileObservation).
	hardPath := filepath.Join(wt, "src/hardlink.go")
	linkPeer := filepath.Join(wt, "src/hardlink_peer.go")
	testutil.FailErr(t, "hardlink", os.Link(hardPath, linkPeer))
	testutil.FailErr(t, "modify through link", os.WriteFile(linkPeer, []byte(editText), 0o644))
	h.exec("", "grep", "-rl", "AlphaMatchTokenInsertedHere", "src").expect(t, "hardlink modify", "src/atomic.go\nsrc/hardlink.go\nsrc/hardlink_peer.go\nsrc/inplace.go\n", "", 0)

	// 4. Held unsaved editor document overrides cached disk negative even with unchanged disk stat.
	draftAbs := filepath.Join(wt, "src/draft.go")
	outsideRel, ok := projectroot.OutsideRel(draftAbs)
	if !ok {
		t.Fatalf("expected outside rel for %s", draftAbs)
	}
	const outsideID = "outside-root-1"
	docs := &draftTestDocuments{
		docs: map[string]tools.EditorDocumentText{
			outsideID + "/" + outsideRel: {
				ID:       "doc-draft",
				Revision: 2,
				Text:     editText,
				SHA256:   textfile.StringSHA256(editText),
				Dirty:    true,
			},
		},
	}
	h.tc.Identity.ProjectID = "proj-1"
	h.tc.Source.SourceWorkspaceKind = api.SourceWorkspaceKindProject
	h.tc.Source.OutsideRoot = projectroot.OutsideRef(outsideID)
	h.tc.Source.EditorDocuments = docs

	withDraft := h.exec("", "grep", "-rl", "AlphaMatchTokenInsertedHere", "src")
	withDraft.expect(t, "unsaved editor draft overrides disk negative", "src/atomic.go\nsrc/draft.go\nsrc/hardlink.go\nsrc/hardlink_peer.go\nsrc/inplace.go\n", "", 0)
	if stats.records[withDraft.execution]["learned_files"] != 0 {
		t.Fatalf("unsaved draft capture must not publish disk bloom: %v", stats.records[withDraft.execution])
	}
}
