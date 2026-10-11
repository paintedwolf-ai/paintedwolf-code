package sourceapi

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	catalogtest "github.com/lycaon/lycaon/internal/testsetup/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSymbolFederationDoesNotCycleBeyondRetainedProjectCapacity(t *testing.T) {
	e, first, leg := symbolProgressFixture(t, 49)
	projects := []*project.Project{first}
	for range symbolProgressLimit {
		root := t.TempDir()
		for i := range 49 {
			testutil.FailErr(t, "write project declaration", os.WriteFile(filepath.Join(root, fmt.Sprintf("f%03d.go", i)), []byte("package p\nfunc Target() {}\n"), 0600))
		}
		p, err := project.CreateWithRoot(t.Context(), e.registry, root)
		testutil.FailErr(t, "attach project", err)
		projects = append(projects, p)
		testutil.FailErr(t, "prepare project index", catalogtest.AwaitIndex(t.Context(), sourcecatalog.Process(), p.ID, sourcecatalog.Root{ID: p.Roots[0].ID, Path: p.Roots[0].Path}))
		leg.Roots = append(leg.Roots, search.CodeRoot{ProjectID: p.ID, RootID: p.Roots[0].ID, Path: p.Roots[0].Path})
	}
	filter, err := search.CompileSymbolFilter(leg)
	testutil.FailErr(t, "compile federated query", err)
	for attempt := range 2 {
		pending, terminal := 0, 0
		for _, target := range symbolProjects(leg.Roots) {
			result := e.searchProject(t.Context(), leg, filter, target)
			testutil.FailErr(t, "search project slice", result.err)
			for _, gap := range result.coverage.Gaps {
				switch gap.Reason {
				case projectsource.DeclarationPending:
					pending++
				case projectsource.DeclarationSymbolBudget:
					if gap.Limit != symbolProgressLimit {
						t.Fatalf("retention bound has wrong units: %+v", gap)
					}
					terminal++
				case projectsource.DeclarationTimeBudget, projectsource.DeclarationFilesSkipped,
					projectsource.DeclarationCatalogWarming, projectsource.DeclarationCatalogIncomplete,
					projectsource.DeclarationCatalogRefreshing, projectsource.DeclarationIndexWarming:
					t.Fatalf("prepared project has unexpected coverage gap: %+v", gap)
				}
			}
		}
		if attempt == 1 && (pending != 0 || terminal != 1) {
			t.Fatalf("stable %d-project query restarted slices instead of completing retained progress and reporting one capacity bound: pending=%d terminal=%d", len(projects), pending, terminal)
		}
	}
}

func TestSymbolUnretainedForegroundCompletionAndCancellation(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 1)
	filter, err := search.CompileSymbolFilter(leg)
	testutil.FailErr(t, "compile overflow query", err)
	target := symbolProject{id: p.ID, rootIDs: []string{p.Roots[0].ID}}
	result := e.searchProject(t.Context(), leg, filter, target)
	testutil.FailErr(t, "complete unretained foreground search", result.err)
	if len(result.hits) != 1 || len(result.coverage.Gaps) != 0 || len(e.progress.entries) != 0 {
		t.Fatalf("complete unretained search acquired a false coverage gap or retained state: %+v", result)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result = e.searchProject(ctx, leg, filter, target)
	if !errors.Is(result.err, context.Canceled) || len(e.progress.entries) != 0 {
		t.Fatalf("unretained cancellation=%+v", result)
	}
}

func TestSymbolActiveCacheCapacityDoesNotAdvertiseLostProgress(t *testing.T) {
	e, p, leg := symbolProgressFixture(t, 49)
	for i := range symbolProgressLimit {
		leg.Name = fmt.Sprintf("active%d", i)
		_, release, err := e.progress.acquire(t.Context(), p, leg, nil, true)
		testutil.FailErr(t, "hold active query", err)
		t.Cleanup(release)
	}
	leg.Name = "Target"
	filter, err := search.CompileSymbolFilter(leg)
	testutil.FailErr(t, "compile capacity query", err)
	result := e.searchProject(t.Context(), leg, filter, symbolProject{id: p.ID, rootIDs: []string{p.Roots[0].ID}, retainProgress: true})
	testutil.FailErr(t, "search without retained capacity", result.err)
	if len(result.hits) != 48 || len(result.coverage.Gaps) != 1 || result.coverage.Gaps[0].Reason != projectsource.DeclarationSymbolBudget || result.coverage.Gaps[0].Limit != symbolProgressLimit {
		t.Fatalf("unretained slice advertised resumable progress: %+v", result)
	}
}
