package editordoc

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUnsavedPathMetadataIncludesDeletedDraftWithoutOpeningText(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "base", "clean.txt": "clean"})
	d := f.open(t, "a.txt")
	_ = f.open(t, "clean.txt")
	typed, err := f.service.ReplaceSnapshot(t.Context(), d.ID, f.project.ID, SnapshotReplacement{DocumentCommand: DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: d.Revision}, Content: "human", EOL: "lf"})
	testutil.FailErr(t, "human draft", err)
	testutil.FailErr(t, "remove saved file", os.Remove(filepath.Join(f.root, "a.txt")))
	testutil.FailErr(t, "observe deletion", f.service.ObserveExternal(t.Context(), f.project, []PathRef{{RootID: f.rootID, Path: "a.txt"}}))
	stored, err := f.store.Get(t.Context(), typed.ID)
	testutil.FailErr(t, "accepted absent identity", err)
	f.service.replicas.mu.Lock()
	for id := range f.service.replicas.entries {
		f.service.replicas.evict(t.Context(), id)
	}
	f.service.replicas.mu.Unlock()
	var states []DocumentPathState
	testutil.FailErr(t, "unsaved metadata", f.service.VisitUnsavedDocuments(t.Context(), f.project.ID, f.project.SourceBranch, f.rootID, "", true, func(state DocumentPathState) error {
		states = append(states, state)
		return nil
	}))
	if len(states) != 1 || states[0].ID != typed.ID || !states[0].Absent || states[0].Revision != stored.Revision {
		t.Fatalf("unsaved metadata=%+v", states)
	}
	f.service.replicas.mu.Lock()
	defer f.service.replicas.mu.Unlock()
	if len(f.service.replicas.entries) != 0 {
		t.Fatal("metadata lookup opened a replica")
	}
}

func TestUnsavedExactPathUsesCompleteIdentityLookup(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"source": "base"})
	for _, path := range []string{"source", "missing", "nested/source"} {
		query, args := unsavedPathStatesQuery(f.project.ID, sourcebranch.Trunk, f.rootID, path, "", false)
		rows, err := f.store.db.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+query, args...)
		testutil.FailErr(t, "inspect exact path query", err)
		var details []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			testutil.FailErr(t, "read query plan", rows.Scan(&id, &parent, &unused, &detail))
			details = append(details, detail)
		}
		testutil.FailErr(t, "finish query plan", rows.Err())
		testutil.FailErr(t, "close query plan", rows.Close())
		plan := strings.Join(details, "\n")
		if !strings.Contains(plan, "project_id=? AND branch_id=? AND root_id=? AND path=?") || strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("exact lookup is not bounded by one identity: %s", plan)
		}
	}
}

func TestUnsavedPathPagesMatchScopeModel(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"seed": "base"})
	seed := f.open(t, "seed")
	otherRoot := testdbseed.InsertProjectRoot(t, f.store.db, f.project.ID, t.TempDir())
	branches := []sourcebranch.ID{sourcebranch.Trunk, sourcebranch.ForWorktree(uuid.NewString())}
	var documents []Document
	for _, root := range []string{f.rootID, otherRoot} {
		for _, branch := range branches {
			for i := 0; i < documentStatusPageSize+3; i++ {
				d := *seed
				d.ID, d.RootID, d.BranchID = uuid.NewString(), root, branch
				d.Path = fmt.Sprintf("scope/%03d", i)
				d.Dirty, d.Diverged = i%3 != 0, i%3 == 0
				testutil.FailErr(t, "seed scoped metadata", f.store.Insert(t.Context(), &d))
				documents = append(documents, d)
			}
		}
	}
	for _, path := range []string{"scope", "scope0/sibling", "scope-extra", "scope/é", "scope/%_", "scope//nested", "elsewhere", "clean"} {
		d := *seed
		d.ID, d.Path, d.Dirty = uuid.NewString(), path, path != "clean"
		testutil.FailErr(t, "seed path boundary", f.store.Insert(t.Context(), &d))
		documents = append(documents, d)
	}
	for _, root := range []string{"", f.rootID, otherRoot, "missing"} {
		for _, branch := range branches {
			for _, path := range []string{"", "scope", "scope/002", "scope/%_", "missing"} {
				for _, descendants := range []bool{false, true} {
					var want, got []string
					for _, d := range documents {
						if (!d.Dirty && !d.Diverged) || root != "" && (d.RootID != root || d.BranchID != branch) {
							continue
						}
						if path == "" || d.Path == path || descendants && strings.HasPrefix(d.Path, path+"/") {
							want = append(want, d.ID)
						}
					}
					slices.Sort(want)
					after := ""
					for {
						page, err := f.service.unsavedPathStatesPage(t.Context(), f.project.ID, branch, root, path, after, descendants)
						testutil.FailErr(t, "read scope page", err)
						if len(page) > documentStatusPageSize {
							t.Fatalf("unbounded page: %d", len(page))
						}
						for _, d := range page {
							got = append(got, d.ID)
						}
						if len(page) < documentStatusPageSize {
							break
						}
						after = page[len(page)-1].ID
					}
					if !slices.Equal(got, want) {
						t.Fatalf("root=%q branch=%q path=%q descendants=%v: got %d identities, want %d", root, branch, path, descendants, len(got), len(want))
					}
				}
			}
		}
	}
}
