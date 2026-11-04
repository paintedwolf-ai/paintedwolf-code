package survey

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSourceHistoryDiffACDF12RecreatedPathKeepsItsBaseline(t *testing.T) {
	seeded := func(t *testing.T) (*historyHarness, string) {
		t.Helper()
		h := newHistoryHarness(t)
		h.record(effectSeed{path: "a.go", content: "baseline\n", session: "s0", op: api.SourceChangeOpCreate})
		baseline := h.head("a.go").VersionID
		h.beginSession(historySession)
		return h, baseline
	}
	t.Run("rewritten with the same bytes", func(t *testing.T) {
		h, baseline := seeded(t)
		h.remove("a.go", sourcebranch.Trunk)
		h.record(effectSeed{path: "a.go", content: "baseline\n", op: api.SourceChangeOpCreate})
		result := h.sessionDiff("a.go")
		if !result.Recorded || result.BaseVersionID != baseline || result.Diff != "" {
			t.Fatalf("diff = %+v, want an empty diff from %s", result, baseline)
		}
	})
	t.Run("rewritten with new bytes", func(t *testing.T) {
		h, baseline := seeded(t)
		h.remove("a.go", sourcebranch.Trunk)
		h.record(effectSeed{path: "a.go", content: "changed\n", op: api.SourceChangeOpCreate})
		result := h.sessionDiff("a.go")
		if result.BaseVersionID != baseline {
			t.Fatalf("diff = %+v, want base %s", result, baseline)
		}
		expectSingleChange(t, result.Diff, "baseline", "changed")
	})
	t.Run("deleted again after the recreate", func(t *testing.T) {
		h, baseline := seeded(t)
		h.remove("a.go", sourcebranch.Trunk)
		h.record(effectSeed{path: "a.go", content: "second\n", op: api.SourceChangeOpCreate})
		h.remove("a.go", sourcebranch.Trunk)
		result, err := h.diff(map[string]any{"path": "a.go"})
		testutil.FailErr(t, "diff deleted path", err)
		removed, added := diffChanges(result.Diff)
		if result.BaseVersionID != baseline || result.State != "absent" || len(removed) != 1 || removed[0] != "baseline" || len(added) != 0 {
			t.Fatalf("diff = %+v, want the baseline removed", result)
		}
	})
	t.Run("renamed away with a new file at the old path", func(t *testing.T) {
		h, baseline := seeded(t)
		h.record(effectSeed{path: "moved.go", fromPath: "a.go", op: api.SourceChangeOpRename})
		h.record(effectSeed{path: "a.go", content: "replacement\n", op: api.SourceChangeOpCreate})
		result := h.sessionDiff("a.go")
		if result.BaseVersionID != baseline {
			t.Fatalf("diff = %+v, want base %s", result, baseline)
		}
		expectSingleChange(t, result.Diff, "baseline", "replacement")
		if moved := h.sessionDiff("moved.go"); moved.BaseVersionID != baseline || moved.Diff != "" {
			t.Fatalf("moved file diff = %+v, want it unchanged from %s", moved, baseline)
		}
	})
	t.Run("created this session stays a diff from absence", func(t *testing.T) {
		h, _ := seeded(t)
		h.record(effectSeed{path: "new.go", content: "first\n", op: api.SourceChangeOpCreate})
		h.remove("new.go", sourcebranch.Trunk)
		h.record(effectSeed{path: "new.go", content: "second\n", op: api.SourceChangeOpCreate})
		result := h.sessionDiff("new.go")
		_, added := diffChanges(result.Diff)
		if result.BaseVersionID != "" || len(added) != 1 || added[0] != "second" {
			t.Fatalf("diff = %+v, want absence to head", result)
		}
	})
	t.Run("a file moved onto an empty path brought its own baseline", func(t *testing.T) {
		h, _ := seeded(t)
		h.record(effectSeed{path: "elsewhere.go", content: "visitor\n", session: "s0", branch: sourcebranch.Trunk, op: api.SourceChangeOpCreate})
		h.record(effectSeed{path: "fresh.go", fromPath: "elsewhere.go", op: api.SourceChangeOpRename})
		h.remove("fresh.go", sourcebranch.Trunk)
		h.record(effectSeed{path: "fresh.go", content: "new\n", op: api.SourceChangeOpCreate})
		result := h.sessionDiff("fresh.go")
		if result.BaseVersionID != "" {
			t.Fatalf("diff = %+v, want absence: the path held nothing at the baseline", result)
		}
	})
	t.Run("another branch's recreate does not supply a baseline", func(t *testing.T) {
		h, _ := seeded(t)
		h.record(effectSeed{path: "peer.go", content: "peer\n", branch: "job-1", session: "child", job: "job-1", op: api.SourceChangeOpCreate})
		h.remove("peer.go", "job-1")
		h.record(effectSeed{path: "peer.go", content: "mine\n", op: api.SourceChangeOpCreate})
		if result := h.sessionDiff("peer.go"); result.BaseVersionID != "" {
			t.Fatalf("diff = %+v, want absence on the caller's branch", result)
		}
	})
}

func TestSourceHistoryDiffACDF13DeletedBeforeTheSessionSaysSo(t *testing.T) {
	h := newHistoryHarness(t)
	h.record(effectSeed{path: "gone.go", content: "old\n", session: "s0", op: api.SourceChangeOpCreate})
	h.remove("gone.go", sourcebranch.Trunk)
	h.record(effectSeed{path: "kept.go", content: "kept\n", session: "s0", op: api.SourceChangeOpCreate})
	h.beginSession(historySession)

	gone, err := h.diff(map[string]any{"path": "gone.go"})
	testutil.FailErr(t, "diff deleted path", err)
	if !gone.Recorded || gone.Diff != "" || gone.State != "absent" || gone.VersionID != "" || gone.BaseVersionID != "" || gone.Note == "" {
		t.Fatalf("deleted before the session = %+v", gone)
	}
	kept := h.sessionDiff("kept.go")
	if kept.Note == "" || kept.Note == gone.Note {
		t.Fatalf("notes = %q and %q, want distinct facts", kept.Note, gone.Note)
	}
}

// The ledger's own comparison classifies each side here, not a fake.
func TestSourceHistoryDiffACDF08RealComparisonClassifiesEachSide(t *testing.T) {
	calls := func(h *historyHarness, path string) map[string]map[string]any {
		return map[string]map[string]any{
			"default":  {"path": path},
			"explicit": {"path": path, "version_id": h.head(path).VersionID},
		}
	}
	t.Run("unresolved", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.beginSession(historySession)
		h.record(effectSeed{path: "a.bin"})
		h.writeFile("a.bin", "x")
		for name, call := range calls(h, "a.bin") {
			_, err := h.diff(call)
			reject := expectReject(t, err, "SOURCE_VERSION_UNAVAILABLE")
			if reject.Data["side"] != "version" || reject.Data["state"] != string(sourceledger.ContentUnresolved) {
				t.Fatalf("%s reject = %v", name, reject.Data)
			}
		}
	})
	t.Run("not captured", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.beginSession(historySession)
		h.nextOp++
		testutil.FailErr(t, "record metadata only", h.store.Record(t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: "r1", Path: "big.bin"},
			ProjectID:      historyProject, Op: api.SourceChangeOpWrite, Origin: api.SourceChangeOriginAgent,
			SessionID: historySession, Turn: 1, OperationID: "op-metadata",
			AfterSHA256: "0000000000000000000000000000000000000000000000000000000000000000", AfterSize: 4,
		}))
		h.writeFile("big.bin", "data")
		for name, call := range calls(h, "big.bin") {
			_, err := h.diff(call)
			reject := expectReject(t, err, "SOURCE_VERSION_UNAVAILABLE")
			if reject.Data["side"] != "version" || reject.Data["state"] != string(sourceledger.ContentNotCaptured) {
				t.Fatalf("%s reject = %v", name, reject.Data)
			}
		}
	})
	t.Run("retained bytes lost", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.record(effectSeed{path: "a.go", content: "before\n", session: "s0", op: api.SourceChangeOpCreate})
		baseline := h.head("a.go").VersionID
		h.beginSession(historySession)
		h.record(effectSeed{path: "a.go", content: "after\n"})
		h.writeFile("a.go", "after\n")
		testutil.FailErr(t, "drop retained bytes", filepath.WalkDir(h.blobs, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			return os.Remove(path)
		}))
		_, err := h.diff(map[string]any{"path": "a.go"})
		reject := expectReject(t, err, "SOURCE_VERSION_UNAVAILABLE")
		if reject.Data["state"] != string(sourceledger.ContentUnavailable) || reject.Data["side"] != "version" {
			t.Fatalf("reject = %v, want the head's bytes reported first", reject.Data)
		}
		_, err = h.diff(map[string]any{"path": "a.go", "version_id": baseline})
		reject = expectReject(t, err, "SOURCE_VERSION_UNAVAILABLE")
		if reject.Data["state"] != string(sourceledger.ContentUnavailable) {
			t.Fatalf("explicit reject = %v", reject.Data)
		}
	})
	t.Run("a directory diffs empty", func(t *testing.T) {
		h := newHistoryHarness(t)
		h.beginSession(historySession)
		h.record(effectSeed{path: "pkg", kind: sourceledger.EntryKindDirectory, op: api.SourceChangeOpCreate})
		testutil.FailErr(t, "create directory", os.Mkdir(filepath.Join(h.dir, "pkg"), 0o755))
		result, err := h.diff(map[string]any{"path": "pkg"})
		testutil.FailErr(t, "diff directory", err)
		if !result.Recorded || result.Diff != "" || result.VersionID == "" || result.Note != "" {
			t.Fatalf("directory diff = %+v, want an empty diff with no note", result)
		}
	})
}
