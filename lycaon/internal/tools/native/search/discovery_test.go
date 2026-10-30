package search

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

type discoveryRecords struct {
	mu      sync.Mutex
	records map[string]map[string]int64
}

func (h *discoveryRecords) Enabled(context.Context, slog.Level) bool { return true }
func (h *discoveryRecords) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *discoveryRecords) WithGroup(string) slog.Handler            { return h }
func (h *discoveryRecords) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "command discovery completed" {
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

func discoveryCapture(t *testing.T) *discoveryRecords {
	t.Helper()
	previous := slog.Default()
	h := &discoveryRecords{records: map[string]map[string]int64{}}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return h
}

func discoveryCatalog(t *testing.T) *sourcecatalog.Catalog {
	t.Helper()
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	c := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain discovery", c.Drain(context.Background())) })
	return c
}

func TestFindLearnsAndReusesStructureWithoutStattingNonmatches(t *testing.T) {
	logs := discoveryCapture(t)
	files := map[string]string{"src/target.go": "source", ".hidden/target.go": "hidden", ".git/target.go": "metadata"}
	for i := range 40 {
		files[fmt.Sprintf("src/noise-%02d.txt", i)] = "noise"
	}
	h := newHarness(t, files)
	h.owners.Catalog = discoveryCatalog(t)
	for attempt := range 2 {
		r := h.exec("", "find", ".", "-name", "target.go")
		r.expect(t, "indexed names", "./.git/target.go\n./.hidden/target.go\n./src/target.go\n", "", 0)
		work := logs.records[r.execution]
		if work["entries"] != 47 || work["entry_metadata_reads"] != 7 {
			t.Fatalf("attempt %d work=%v", attempt, work)
		}
		if attempt == 0 && work["directory_preparations"] != 4 || attempt == 1 && (work["directory_reuses"] != 4 || work["directory_live_lists"] != 0 || work["directory_preparations"] != 0) {
			t.Fatalf("attempt %d membership work=%v", attempt, work)
		}
	}
}

func TestFindIndexedAndLiveExpressionsHaveIdenticalResults(t *testing.T) {
	h := newHarness(t, map[string]string{"a.txt": "a", "Z/target.go": "source", "b/other.txt": "", ".hidden/target.go": "hidden"})
	symlink(t, "Z", filepath.Join(h.root, "link"))
	symlink(t, "missing", filepath.Join(h.root, "dangling"))
	catalog := discoveryCatalog(t)
	for _, argv := range [][]string{
		{"find", "."},
		{"find", ".", "-iname", "*TARGET*"},
		{"find", ".", "!", "-name", "*.txt", "-type", "f"},
		{"find", ".", "-path", "*/Z", "-prune", "-o", "-name", "*.go", "-print0"},
		{"find", ".", "-regex", ".*target[.]go", "-size", "+0c"},
		{"find", ".", "-mindepth", "1", "-maxdepth", "1", "-printf", "%p %y %s\\n"},
		{"find", "-d", ".", "-empty"},
		{"find", "-L", ".", "-name", "*.go"},
		{"find", "Z", "a.txt", "-print"},
		{"find", ".", "-name", "target.go", "-exec", "echo", "{}", ";"},
	} {
		t.Run(fmt.Sprint(argv), func(t *testing.T) {
			h.owners.Catalog = nil
			live := h.exec("", argv...)
			h.owners.Catalog = catalog
			indexed := h.exec("", argv...)
			if live.stdout != indexed.stdout || live.stderr != indexed.stderr || live.result != indexed.result || fmt.Sprint(live.err) != fmt.Sprint(indexed.err) {
				t.Fatalf("live=%+v indexed=%+v", live, indexed)
			}
		})
	}
}

func TestFindRefreshesChangedMembershipAndLiveMetadata(t *testing.T) {
	h := newHarness(t, map[string]string{"src/target.go": "old", "src/other.txt": "source"})
	h.owners.Catalog = discoveryCatalog(t)
	h.exec("", "find", "src", "-name", "target.go").expect(t, "initial names", "src/target.go\n", "", 0)
	testutil.FailErr(t, "rename indexed entry", os.Rename(filepath.Join(h.root, "src/target.go"), filepath.Join(h.root, "src/renamed.go")))
	testutil.FailErr(t, "replace other entry with directory", os.Remove(filepath.Join(h.root, "src/other.txt")))
	seed(t, h.root, map[string]string{"src/other.txt/target.go": "replacement"})
	h.exec("", "find", "src", "-name", "target.go").expect(t, "changed structure", "src/other.txt/target.go\n", "", 0)
	h.exec("", "find", "src", "-name", "renamed.go", "-size", "3c").expect(t, "initial metadata", "src/renamed.go\n", "", 0)
	testutil.FailErr(t, "rewrite metadata", os.WriteFile(filepath.Join(h.root, "src/renamed.go"), []byte("longer source"), 0o644))
	h.exec("", "find", "src", "-name", "renamed.go", "-size", "3c").expect(t, "live metadata", "", "", 0)
}

func TestFindChargesEveryNameBeforePredicateElimination(t *testing.T) {
	h := newHarness(t, map[string]string{"a": "source", "b": "source", "c": "source"})
	h.owners.overrides = map[string]int64{"traversal_visits": 2}
	h.owners.Catalog = discoveryCatalog(t)
	h.exec("", "find", ".", "-name", "absent").refused(t, "bounded cold discovery", "COMMAND_LIMIT_EXCEEDED")
	h.owners.overrides["traversal_visits"] = 4
	h.exec("", "find", ".", "-name", "absent").expect(t, "exact traversal allowance", "", "", 0)
	h.owners.overrides["traversal_visits"] = 2
	h.exec("", "find", ".", "-name", "absent").refused(t, "bounded reused discovery", "COMMAND_LIMIT_EXCEEDED")
}

func TestFindCachedMembershipUsesTheCurrentReadBoundary(t *testing.T) {
	h := newHarness(t, map[string]string{"src/target.go": "source", "private/target.go": "private"})
	h.owners.Catalog = discoveryCatalog(t)
	h.exec("", "find", ".", "-name", "target.go").expect(t, "initial scope", "./private/target.go\n./src/target.go\n", "", 0)
	h.boundary = sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: h.tc.ProfileID(), Tools: map[string]bool{"read": true, "find": true}, ReadGlobs: []string{".", "src", "src/**"},
	}})
	h.owners.Revisions.Boundary = h.boundary
	h.exec("", "find", ".", "-name", "target.go").expect(t, "restricted current scope", "./src/target.go\n", "", 0)
}

func TestFindCachedMembershipPreservesDeletionAndActionOrder(t *testing.T) {
	h := newHarness(t, map[string]string{"tree/a.tmp": "remove", "tree/b.keep": "keep", "tree/sub/c.tmp": "remove", "tree/sub/d.keep": "keep"})
	h.owners.Catalog = discoveryCatalog(t)
	h.exec("", "find", "tree", "-false").expect(t, "prepare memberships", "", "", 0)
	r := h.exec("", "find", "tree", "-name", "*.tmp", "-delete", "-o", "-print")
	r.expect(t, "delete action order", "tree/b.keep\ntree/sub/d.keep\ntree/sub\ntree\n", "", 0)
	if fmt.Sprint(h.removals.removed) != "[tree/a.tmp tree/sub/c.tmp]" || r.result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("removed=%v effects=%s", h.removals.removed, r.result.EffectCertainty)
	}
	h.exec("", "find", "tree", "-name", "*.tmp").expect(t, "deleted membership refresh", "", "", 0)
}

func TestIndexedGrepSharesRootsAndImmediateChildMetadata(t *testing.T) {
	logs := discoveryCapture(t)
	h := newHarness(t, map[string]string{"src/a": "source", "src/b": "other", "deep/child/c": "source"})
	h.owners.Catalog = discoveryCatalog(t)
	for attempt := range 2 {
		r := h.exec("", "grep", "-rl", "source", ".")
		r.expect(t, "indexed grep", "./deep/child/c\n./src/a\n", "", 0)
		work := logs.records[r.execution]
		if work["shared_root_opens"] != 1 || work["directory_handles"] != 4 || work["relative_child_stats"] != 6 || work["entry_metadata_reads"] != 7 {
			t.Fatalf("attempt %d resources=%v", attempt, work)
		}
		if attempt == 1 && (work["directory_reuses"] != 4 || work["directory_live_lists"] != 0) {
			t.Fatalf("warm membership=%v", work)
		}
	}
}

func TestFindNamespaceExclusionsDoNotSpendTraversalAllowance(t *testing.T) {
	h := newHarness(t, map[string]string{"visible": "source", ".paintedwolf/recovery/only": "recovery"})
	h.owners.overrides = map[string]int64{"traversal_visits": 2}
	h.exec("", "find", ".").expect(t, "live exact allowance", ".\n./visible\n", "", 0)
	h.owners.Catalog = discoveryCatalog(t)
	for attempt := range 2 {
		h.exec("", "find", ".").expect(t, "indexed exact allowance", ".\n./visible\n", "", 0)
	}
	h.owners.overrides["traversal_visits"] = 3
	h.exec("", "find", ".", "-false").expect(t, "prepare complete namespace", "", "", 0)
	h.owners.overrides["traversal_visits"] = 2
	h.exec("", "find", ".").expect(t, "complete cached allowance", ".\n./visible\n", "", 0)
}
