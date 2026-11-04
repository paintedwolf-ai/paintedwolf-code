package native

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	surveytools "github.com/lycaon/lycaon/internal/tools/native/survey"
	"github.com/lycaon/lycaon/pkg/api"
)

const branchScopeWorker = sourcebranch.ID("job-1")

// branchScopeFixture drives the tools over a real ledger, so the branch
// predicate itself is under test. Trunk writes are the user's and worker writes
// an agent's, which lets a row's actor prove the branch it came from.
type branchScopeFixture struct {
	nativeWalkFixture
	t       *testing.T
	content map[sourcebranch.ID]string
	writes  int
}

func newBranchScopeFixture(t *testing.T) *branchScopeFixture {
	t.Helper()
	f := &branchScopeFixture{nativeWalkFixture: newNativeWalkFixture(t), t: t, content: map[sourcebranch.ID]string{}}
	testutil.FailErr(t, "write fixture", os.WriteFile(filepath.Join(f.dir, "main.go"), []byte("package main\n"), 0o644))
	return f
}

func (f *branchScopeFixture) record(branch sourcebranch.ID, count int) {
	f.t.Helper()
	for range count {
		f.writes++
		before, seen := f.content[branch]
		if !seen {
			before = f.content[sourcebranch.Trunk]
		}
		after := fmt.Sprintf("write %d\n", f.writes)
		op, origin, job := api.SourceChangeOpWrite, api.SourceChangeOriginUser, ""
		if branch.IsWorker() {
			origin, job = api.SourceChangeOriginAgent, branch.String()
		} else if before == "" {
			op = api.SourceChangeOpCreate
		}
		testutil.FailErr(f.t, "record effect", f.ledger.Record(f.t.Context(), sourceledger.RecordInput{
			RecordLocation: sourceledger.RecordLocation{RootID: "r1", Path: "main.go"},
			ProjectID:      "p1", BranchID: branch, JobID: job, Op: op, Origin: origin,
			Before: []byte(before), After: []byte(after),
		}))
		f.content[branch] = after
	}
}

func (f *branchScopeFixture) interleave(pairs int) {
	f.t.Helper()
	for range pairs {
		f.record(sourcebranch.Trunk, 1)
		f.record(branchScopeWorker, 1)
	}
}

func (f *branchScopeFixture) workerContext() tools.ToolContext {
	tc := f.tc
	tc.Identity.WorkerJobID = branchScopeWorker.String()
	tc.Source.WorkerBranchRoot = f.dir
	tc.Source.SourceWorkspaceKind = api.SourceWorkspaceKindWorker
	return tc
}

type branchScopePage struct {
	Recorded bool `json:"recorded"`
	Effects  []struct {
		Actor   string `json:"actor"`
		Ordinal int64  `json:"ordinal"`
	} `json:"effects"`
	NextBeforeOrdinal int64  `json:"next_before_ordinal"`
	Note              string `json:"note"`
}

func (f *branchScopeFixture) history(tc tools.ToolContext, limit int, before int64) branchScopePage {
	f.t.Helper()
	tool := &surveytools.SourceHistoryTool{Boundary: nativefixture.Boundary(f.t)}
	args := map[string]any{"path": "main.go", "limit": limit}
	if before != 0 {
		args["before_ordinal"] = int(before)
	}
	out, err := tool.Run(f.t.Context(), args, tc)
	testutil.FailErr(f.t, "source_history", err)
	var page branchScopePage
	testutil.FailErr(f.t, "decode source_history", json.Unmarshal([]byte(out), &page))
	return page
}

func assertEffectActors(t *testing.T, page branchScopePage, rows int, actor string) {
	t.Helper()
	if len(page.Effects) != rows {
		t.Fatalf("rows = %d, want %d", len(page.Effects), rows)
	}
	for _, effect := range page.Effects {
		if effect.Actor != actor {
			t.Fatalf("row %+v, want actor %q", effect, actor)
		}
	}
}

func TestSourceHistoryBranchScope(t *testing.T) {
	t.Run("AC-BR-01 coordinator pages skip interleaved worker effects", func(t *testing.T) {
		f := newBranchScopeFixture(t)
		f.interleave(30)
		first := f.history(f.tc, 20, 0)
		assertEffectActors(t, first, 20, "user")
		if first.NextBeforeOrdinal == 0 {
			t.Fatal("first page has no cursor with ten trunk effects remaining")
		}
		rest := f.history(f.tc, 20, first.NextBeforeOrdinal)
		assertEffectActors(t, rest, 10, "user")
		if rest.NextBeforeOrdinal != 0 {
			t.Fatalf("last page cursor = %d, want none", rest.NextBeforeOrdinal)
		}
	})

	t.Run("AC-BR-02 worker-only effects give an empty page with no cursor", func(t *testing.T) {
		f := newBranchScopeFixture(t)
		content := []byte("tracked\n")
		_, err := f.ledger.TrackFile(t.Context(), sourceledger.TrackInput{
			ProjectID: "p1", RootID: "r1", Path: "main.go", BranchID: sourcebranch.Trunk,
			EntryKind: sourceledger.EntryKindFile, Content: content,
			SHA256: sourceblob.ContentSHA(content), Size: int64(len(content)),
		})
		testutil.FailErr(t, "track on trunk", err)
		f.content[sourcebranch.Trunk] = string(content)
		f.record(branchScopeWorker, 25)
		page := f.history(f.tc, 20, 0)
		if !page.Recorded || len(page.Effects) != 0 || page.NextBeforeOrdinal != 0 || page.Note == "" {
			t.Fatalf("page = %+v, want recorded, no rows, no cursor, a note", page)
		}
	})

	t.Run("AC-BR-03 worker leg sees only its own effects", func(t *testing.T) {
		f := newBranchScopeFixture(t)
		f.interleave(5)
		page := f.history(f.workerContext(), 20, 0)
		assertEffectActors(t, page, 5, "agent")
		if page.NextBeforeOrdinal != 0 {
			t.Fatalf("cursor = %d, want none", page.NextBeforeOrdinal)
		}
	})

	t.Run("AC-BR-05 read stamp names the newest effect on the read's branch", func(t *testing.T) {
		f := newBranchScopeFixture(t)
		f.record(sourcebranch.Trunk, 2)
		f.record(branchScopeWorker, 1)
		read := &surveytools.ReadTool{Boundary: nativefixture.Boundary(t)}
		for _, tc := range []struct {
			name  string
			tctx  tools.ToolContext
			actor string
		}{
			{"trunk", f.tc, "user"},
			{"worker", f.workerContext(), "agent"},
		} {
			out, err := read.Run(t.Context(), map[string]any{"path": "main.go"}, tc.tctx)
			testutil.FailErr(t, "read on "+tc.name, err)
			change, _ := receiptSource(t, out)["last_change"].(map[string]any)
			if change["actor"] != tc.actor {
				t.Fatalf("%s last_change = %v, want actor %q", tc.name, change, tc.actor)
			}
		}
	})

	t.Run("AC-BR-06 edit reject counts only same-branch foreign changes", func(t *testing.T) {
		f := newBranchScopeFixture(t)
		f.record(sourcebranch.Trunk, 1)
		_, err := f.ledger.Checkpoints.CreateStructuralCheckpoint(t.Context(), sourceledger.StructuralCheckpointInput{
			ProjectID: "p1", Kind: sourceledger.CheckpointTurn, Label: "Turn start", SessionID: "s1", Turn: 1,
		})
		testutil.FailErr(t, "create session floor", err)
		f.record(sourcebranch.Trunk, 2)
		f.record(branchScopeWorker, 60)
		edit := &EditTool{Boundary: nativefixture.Boundary(t)}
		_, err = edit.Run(t.Context(), map[string]any{
			"path": "main.go", "old_string": "package moved", "new_string": "package main",
		}, f.tc)
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "EDIT_TARGET_CHANGED_BY_OTHERS" {
			t.Fatalf("edit miss = %v, want EDIT_TARGET_CHANGED_BY_OTHERS", err)
		}
		rows, _ := reject.Data["changed_by"].([]map[string]any)
		if reject.Data["change_count"] != 2 || len(rows) != 2 || rows[0]["actor"] != "user" || rows[1]["actor"] != "user" {
			t.Fatalf("change_count = %v, changed_by = %v, want both trunk changes only", reject.Data["change_count"], rows)
		}
	})
}

// A page short of its limit while a cursor is set makes an agent stop early or
// fetch an empty page, whichever branch is reading and however the branches interleave.
func TestSourceHistoryPagesFullWhileCursorSet(t *testing.T) {
	f := newBranchScopeFixture(t)
	trunk, worker := sourcebranch.Trunk, branchScopeWorker
	for _, burst := range []struct {
		branch sourcebranch.ID
		count  int
	}{{trunk, 1}, {worker, 3}, {trunk, 2}, {worker, 5}, {trunk, 1}, {worker, 1}, {trunk, 4}} {
		f.record(burst.branch, burst.count)
	}
	for _, viewer := range []struct {
		name  string
		tctx  tools.ToolContext
		actor string
		total int
	}{
		{"trunk", f.tc, "user", 8},
		{"worker", f.workerContext(), "agent", 9},
	} {
		for _, limit := range []int{1, 3, 8} {
			t.Run(fmt.Sprintf("%s limit %d", viewer.name, limit), func(t *testing.T) {
				seen, before := 0, int64(0)
				for {
					page := f.history(viewer.tctx, limit, before)
					if page.NextBeforeOrdinal != 0 && len(page.Effects) != limit {
						t.Fatalf("page has %d rows with a cursor, want %d", len(page.Effects), limit)
					}
					assertEffectActors(t, page, len(page.Effects), viewer.actor)
					seen += len(page.Effects)
					if page.NextBeforeOrdinal == 0 {
						if len(page.Effects) == 0 {
							t.Fatal("cursor led to an empty page")
						}
						break
					}
					before = page.NextBeforeOrdinal
				}
				if seen != viewer.total {
					t.Fatalf("walked %d rows, want %d", seen, viewer.total)
				}
			})
		}
	}
}

func TestForeignChangesCountsPastTheSessionsOwnEffects(t *testing.T) {
	dir := t.TempDir()
	abs, sha := writeProvenanceFile(t, dir, "main.go", "package main\n")
	var effects []sourceledger.Effect
	for ordinal := int64(500); ordinal > 3; ordinal-- {
		effects = append(effects, sourceledger.Effect{Origin: api.SourceChangeOriginAgent, SessionID: "test-session", Op: api.SourceChangeOpWrite, Ordinal: ordinal})
	}
	effects = append(effects, sourceledger.Effect{Origin: api.SourceChangeOriginUser, Op: api.SourceChangeOpWrite, Ordinal: 3})
	ledger := &fakeSourceLedger{
		floor: 1, floorFound: true,
		heads:   map[string]sourceledger.BranchHead{"r1/main.go": {FileID: "f1", VersionID: "v2", SHA256: sha, State: "content"}},
		effects: map[string][]sourceledger.Effect{"f1": effects},
	}
	changes, total, ok := sourceview.ForeignChanges(t.Context(), provenanceCtx(dir, ledger), abs)
	if !ok || total != 1 || len(changes) != 1 || changes[0].Actor != "user" {
		t.Fatalf("changes = %+v, total = %d, ok = %v, want the one user change", changes, total, ok)
	}

	ledger.effectsErr = errors.New("ledger read failed")
	if _, _, ok := sourceview.ForeignChanges(t.Context(), provenanceCtx(dir, ledger), abs); ok {
		t.Fatal("a failed lookup reported complete foreign changes")
	}
}
