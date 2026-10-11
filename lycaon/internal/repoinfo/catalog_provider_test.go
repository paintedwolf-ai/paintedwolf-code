package repoinfo_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/internal/repoinfo"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

type boundedBriefScope struct{}

func (boundedBriefScope) Catalog(_ context.Context, root string) *sourcescope.Scope {
	return sourcescope.New(root, sourcescope.Options{Plane: sourcescope.Plane{Budgets: sandbox.SurveyBudgets{DirectoryEntries: 2}}})
}

func TestBoundedBriefSettlesWithoutEstablishingAnEmptyRepository(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		testutil.FailErr(t, "write omitted source", os.WriteFile(filepath.Join(root, name), []byte("package source\n"), 0600))
	}
	catalog := sourcecatalog.New()
	catalog.SetScopes(boundedBriefScope{})
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	provider := repoinfo.NewProvider(catalog.Trees, func(context.Context, string) (repoinfo.CatalogRoot, bool, error) {
		return repoinfo.CatalogRoot{ProjectID: "p", RootID: "r"}, true, nil
	}, "")
	defer func() { _ = provider.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	brief, err := repoinfo.AwaitBrief(ctx, provider, root)
	testutil.FailErr(t, "await bounded brief", err)
	if brief.FileCount != 0 || !brief.Partial || brief.Refreshing {
		t.Fatalf("bounded brief: %+v", brief)
	}
	empty, err := provider.KnownEmpty(ctx, root)
	testutil.FailErr(t, "check bounded emptiness", err)
	if empty {
		t.Fatal("omitted files established empty repository")
	}
	board := (&repoinfo.MultiRootBrief{Roots: []repoinfo.RootBrief{{Brief: brief}}}).PrimaryRepoBrief()
	if !board.Incomplete || board.Refreshing || packboard.RepoKnownEmpty(board) {
		t.Fatalf("bounded orientation: %+v", board)
	}
}

func TestCatalogProviderProjectsSharedGeneration(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(root, "pkg"), 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, "pkg", "main.go"), []byte("package pkg\n"), 0o644))
	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	_, err := catalog.Snapshot(context.Background(), "p1", []sourcecatalog.Root{{ID: "r1", Path: root}})
	testutil.FailErr(t, "warm catalog", err)
	provider := repoinfo.NewProvider(catalog.Trees, func(context.Context, string) (repoinfo.CatalogRoot, bool, error) {
		return repoinfo.CatalogRoot{ProjectID: "p1", RootID: "r1"}, true, nil
	}, "")
	defer func() { _ = provider.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	brief, err := repoinfo.AwaitBrief(ctx, provider, root)
	testutil.FailErr(t, "await brief", err)
	if brief.FileCount != 1 || len(brief.Languages) != 1 || brief.Languages[0] != "Go" {
		t.Fatalf("brief = %+v", brief)
	}
}

func TestBriefRevokesEmptyClaimWhenItsIndexRefreshFails(t *testing.T) {
	t.Setenv("LYCAON_CONFIG_DIR", t.TempDir())
	root := t.TempDir()
	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	provider := repoinfo.NewProvider(catalog.Trees, func(context.Context, string) (repoinfo.CatalogRoot, bool, error) {
		return repoinfo.CatalogRoot{ProjectID: "p", RootID: "r"}, true, nil
	}, "")
	defer func() { _ = provider.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	brief, err := repoinfo.AwaitBrief(ctx, provider, root)
	testutil.FailErr(t, "measure empty repository", err)
	if brief.Partial || brief.FileCount != 0 {
		t.Fatalf("initial brief: %+v", brief)
	}
	testutil.FailErr(t, "remove fixture root", os.Remove(root))
	catalog.InvalidateRoot(root)
	testutil.WaitFor(t, 5*time.Second, func() bool {
		status, statusErr := catalog.Trees.IndexStatus(ctx, "p", sourcecatalog.Root{ID: "r", Path: root})
		return statusErr != nil || status.Error != ""
	})
	brief, err = provider.Brief(ctx, root)
	testutil.FailErr(t, "read failed refresh", err)
	if !brief.Partial || brief.Refreshing {
		t.Fatalf("failed refresh did not revoke completeness: %+v", brief)
	}
}

func TestSharedProviderMeasuresEachRootIndependently(t *testing.T) {
	primary, branch := t.TempDir(), t.TempDir()
	testutil.FailErr(t, "write primary", os.WriteFile(filepath.Join(primary, "main.go"), []byte("package main\n"), 0o644))
	for i := range 2 {
		testutil.FailErr(t, "write branch",
			os.WriteFile(filepath.Join(branch, fmt.Sprintf("models%d.go", i)), []byte("package models\n"), 0o644))
	}
	catalog := sourcecatalog.New()
	t.Cleanup(func() { testutil.FailErr(t, "drain catalog", catalog.Drain(context.Background())) })
	provider := repoinfo.NewProvider(catalog.Trees, func(_ context.Context, path string) (repoinfo.CatalogRoot, bool, error) {
		return repoinfo.CatalogRoot{ProjectID: "project", RootID: "root"}, path == primary, nil
	}, "")
	t.Cleanup(func() { _ = provider.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	branchBrief, err := repoinfo.AwaitBrief(ctx, provider, branch)
	testutil.FailErr(t, "measure branch", err)
	mainBrief, err := repoinfo.AwaitBrief(ctx, provider, primary)
	testutil.FailErr(t, "measure primary", err)
	if branchBrief.FileCount != 2 || mainBrief.FileCount != 1 {
		t.Fatalf("branch=%+v primary=%+v", branchBrief, mainBrief)
	}
	if branchBrief.Partial || mainBrief.Partial {
		t.Fatalf("partial briefs: branch=%v primary=%v", branchBrief.Partial, mainBrief.Partial)
	}
}
