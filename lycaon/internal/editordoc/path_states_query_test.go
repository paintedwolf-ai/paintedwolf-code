package editordoc

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

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
