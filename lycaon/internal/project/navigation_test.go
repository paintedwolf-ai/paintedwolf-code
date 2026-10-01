package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNavigationExactHiddenPathSurvivesOrdinaryEdits(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "hidden folder", os.Mkdir(filepath.Join(root, ".hidden"), 0700))
	name := filepath.Join(root, ".hidden", "ignored.txt")
	testutil.FailErr(t, "hidden file", os.WriteFile(name, []byte("first"), 0600))
	p := &Project{ID: "p", Roots: []Root{{ID: "r", Path: root}}}
	refs := []api.NavigationReference{{Mention: ".hidden/ignored.txt", Path: ".hidden/ignored.txt", ProjectID: "p", RootID: "r", Explicit: true, Status: api.NavigationPending}}
	out := ResolveNavigation(t.Context(), p, refs)
	if out[0].Status != api.NavigationResolved {
		t.Fatalf("explicit: %+v", out)
	}
	testutil.FailErr(t, "edit target", os.WriteFile(name, []byte("different bytes"), 0600))
	changed := ResolveNavigation(t.Context(), p, out)
	if changed[0].Status != api.NavigationResolved || len(changed[0].Candidates) != 0 {
		t.Fatalf("changed binding: %+v", changed)
	}
	testutil.FailErr(t, "delete target", os.Remove(name))
	missing := ResolveNavigation(t.Context(), p, changed)
	if missing[0].Status != api.NavigationMissing || missing[0].Path != out[0].Path || len(missing[0].Candidates) != 0 {
		t.Fatalf("deleted binding: %+v", missing)
	}
	p.Roots = []Root{{ID: "replacement", Path: root}}
	unavailable := ResolveNavigation(t.Context(), p, out)
	if unavailable[0].Status != api.NavigationUnavailable || unavailable[0].RootID != "r" {
		t.Fatalf("detached root=%+v", unavailable)
	}
}

func TestNavigationCancellationDoesNotValidateOldBinding(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	refs := []api.NavigationReference{{Mention: "a.go", ProjectID: "p", RootID: "r", Path: "a.go", Status: api.NavigationResolved}}
	out := ResolveNavigation(ctx, &Project{ID: "p"}, refs)
	if out[0].Status != api.NavigationPending {
		t.Fatalf("canceled binding=%+v", out)
	}
}

func BenchmarkNavigationExactRepository(b *testing.B) {
	root := navigationBenchmarkRepository(b)
	config := b.TempDir()
	b.Setenv("LYCAON_CONFIG_DIR", config)
	p := &Project{ID: "p", Roots: []Root{{ID: "r", Path: root, IsPrimary: true}}}
	var refs []api.NavigationReference
	for _, name := range []string{"packages/pkg-0000/file-000.txt", "packages/pkg-0999/file-099.txt", ".git/HEAD", "README.md"} {
		refs = append(refs, api.NavigationReference{ProjectID: "p", RootID: "r", Path: name})
	}
	for _, ref := range ResolveNavigation(b.Context(), p, refs) {
		if ref.Status != api.NavigationResolved {
			b.Fatalf("fixture unavailable: %+v", ref)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, ref := range ResolveNavigation(b.Context(), p, refs) {
			if ref.Status != api.NavigationResolved {
				b.Fatalf("destination unavailable: %+v", ref)
			}
		}
	}
	b.StopTimer()
	files, err := os.ReadDir(config)
	testutil.FailErr(b, "inspect navigation cache directory", err)
	if len(files) != 0 {
		b.Fatal("exact navigation created repository discovery state")
	}
}

func navigationBenchmarkRepository(b *testing.B) string {
	b.Helper()
	root := b.TempDir()
	payload := filepath.Join(root, "README.md")
	testutil.FailErr(b, "write navigation fixture payload", os.WriteFile(payload, []byte("fixture\n"), 0600))
	testutil.FailErr(b, "create hidden navigation folder", os.Mkdir(filepath.Join(root, ".git"), 0700))
	testutil.FailErr(b, "write hidden navigation target", os.Link(payload, filepath.Join(root, ".git", "HEAD")))
	for directory := range 1000 {
		folder := filepath.Join(root, "packages", fmt.Sprintf("pkg-%04d", directory))
		testutil.FailErr(b, "create navigation fixture folder", os.MkdirAll(folder, 0700))
		for file := range 100 {
			testutil.FailErr(b, "create navigation fixture file", os.Link(payload, filepath.Join(folder, fmt.Sprintf("file-%03d.txt", file))))
		}
	}
	return root
}
