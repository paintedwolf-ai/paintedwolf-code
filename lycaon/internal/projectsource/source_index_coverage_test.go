package projectsource

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/backgroundwork"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceIndexRetainsReadyRootsAlongsideUnavailableRoots(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "warming", true: "failed"}[failed], func(t *testing.T) {
			catalog := sourceIndexTestCatalog(t)
			ready := Root{ID: "ready", Path: t.TempDir(), IsPrimary: true}
			testutil.FailErr(t, "write ready file", os.WriteFile(filepath.Join(ready.Path, "target.go"), []byte("source"), 0600))
			project := &Project{ID: "p", Roots: []Root{ready}}
			warmSourceIndex(t, catalog, project)
			other := Root{ID: "other", Path: t.TempDir()}
			if failed {
				other.Path = filepath.Join(other.Path, "absent")
				reader, _, _ := catalog.Trees.OpenIndex(t.Context(), project.ID, sourcecatalog.Root{ID: other.ID, Path: other.Path}, time.Second)
				if reader != nil {
					_ = reader.Close()
				}
			} else {
				release, err := backgroundwork.Process().Acquire(t.Context(), backgroundwork.Request{Lane: other.Path, Resources: []backgroundwork.Resource{backgroundwork.ResourceMetadata}})
				testutil.FailErr(t, "hold cold metadata", err)
				defer release()
				testutil.FailErr(t, "write cold file", os.WriteFile(filepath.Join(other.Path, "late.go"), []byte("source"), 0600))
			}
			project.Roots = append(project.Roots, other)
			snapshot := (&SourceIndexCache{catalog: catalog}).Snapshot(t.Context(), project)
			defer snapshot.Close()
			matches, err := SearchSourceIndex(t.Context(), snapshot, SourceQuery{Path: "target"}, SourcePathStyle{}, "", nil, 10)
			testutil.FailErr(t, "search ready root", err)
			if snapshot.State != SourceIndexReady || len(matches) != 1 || matches[0].RootID != ready.ID || len(snapshot.Coverage) != 2 {
				t.Fatalf("snapshot=%+v matches=%+v", snapshot, matches)
			}
			for _, coverage := range snapshot.Coverage {
				if coverage.RootID != other.ID {
					continue
				}
				if failed && (coverage.State != SourceIndexFailed || coverage.Error == "") {
					t.Fatalf("missing failed root: %+v", coverage)
				}
				if !failed && !coverage.Pending() {
					t.Fatalf("missing pending root: %+v", coverage)
				}
			}
		})
	}
}
