package survey

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcecatalog"
	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestListDirUnknownCountUsesShallowMap(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(dir, "pkg"), 0o755))
	mustWrite(t, filepath.Join(dir, "pkg", "x.go"), "package x\n")
	tool := &ListDirTool{
		Boundary: nativefixture.Boundary(t),
	}
	tctx := nativefixture.Context(dir)
	tctx.Source.RepoFileCount = 0
	tctx.Source.RepoFileCountKnown = false
	out, err := tool.Run(context.Background(), map[string]any{"path": "."}, tctx)
	testutil.FailErr(t, "list_dir unknown", err)
	var resp listDirResponse
	testutil.FailErr(t, "decode list_dir unknown", json.Unmarshal([]byte(surveyJSONBody(out)), &resp))
	if resp.View != "map" {
		t.Fatalf("view = %q want map", resp.View)
	}
	if resp.Tree == nil {
		t.Fatal("expected shallow tree")
	}
}

func TestListDirDenseRootShallowMapOnly(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"browser", "dom", "js"} {
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Join(dir, name, "deep"), 0o755))
		mustWrite(t, filepath.Join(dir, name, "deep", "x.go"), "package x\n")
	}
	tool := &ListDirTool{Boundary: nativefixture.Boundary(t)}
	tctx := nativefixture.Context(dir)
	tctx.Source.RepoFileCount = 200_000
	out, err := tool.Run(context.Background(), map[string]any{"path": "."}, tctx)
	testutil.FailErr(t, "list_dir dense", err)
	var resp listDirResponse
	testutil.FailErr(t, "decode list_dir dense", json.Unmarshal([]byte(surveyJSONBody(out)), &resp))
	if resp.View != "map" {
		t.Fatalf("view = %q want map", resp.View)
	}
	if resp.Tree == nil || len(resp.Tree.Children) == 0 {
		t.Fatal("expected shallow top-level children")
	}
	for _, child := range resp.Tree.Children {
		if child.Type == "dir" && !child.ZoomIn {
			t.Fatalf("map child %q should ZoomIn", child.Path)
		}
		if child.Type == "dir" && len(child.Children) > 0 {
			t.Fatalf("shallow map must not nest under %q", child.Path)
		}
	}
	if resp.Coverage == nil || resp.Coverage.EntriesReturned != 3 {
		t.Fatalf("expected shallow-map diagnostics, got %#v", resp.Diagnostics)
	}
}

func TestListDirUsesCatalogCountWhenPublishedCountIsStale(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"browser", "dom", "js"} {
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Join(dir, name, "deep"), 0o755))
		mustWrite(t, filepath.Join(dir, name, "deep", "x.go"), "package x\n")
	}
	tool := &ListDirTool{
		Boundary: nativefixture.Boundary(t),
	}
	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID = "p1"
	tctx.Source.RepoFileCount = 1

	out, err := tool.Run(context.Background(), map[string]any{"path": "."}, tctx)
	testutil.FailErr(t, "list_dir stale count", err)
	var resp listDirResponse
	testutil.FailErr(t, "decode list_dir stale", json.Unmarshal([]byte(surveyJSONBody(out)), &resp))
	if resp.Tree == nil || len(resp.Tree.Children) != 3 {
		t.Fatalf("shallow tree = %#v, want three top-level directories", resp.Tree)
	}
	for _, child := range resp.Tree.Children {
		if len(child.Children) != 0 {
			t.Fatalf("catalog map recursed below %q", child.Path)
		}
	}
}

func TestListDirReadyDenseCatalogProjectsOnlyImmediateChildren(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"browser", "dom", "js"} {
		testutil.FailErr(t, "mkdir "+name, os.MkdirAll(filepath.Join(dir, name, "deep"), 0o755))
		for i := range 3 {
			mustWrite(t, filepath.Join(dir, name, "deep", string(rune('a'+i))+".go"), "package x\n")
		}
	}
	catalog := sourcecatalog.New()
	_, err := catalog.Snapshot(context.Background(), "p-ready", []sourcecatalog.Root{{ID: "r1", Path: dir}})
	testutil.FailErr(t, "warm source catalog", err)
	tool := &ListDirTool{
		Boundary: nativefixture.Boundary(t), Catalog: catalog,
	}
	tctx := nativefixture.Context(dir)
	tctx.Identity.ProjectID = "p-ready"
	tctx.Source.RepoFileCount = 1
	out, err := tool.Run(context.Background(), map[string]any{"path": "."}, tctx)
	testutil.FailErr(t, "list_dir ready catalog", err)
	var resp listDirResponse
	testutil.FailErr(t, "decode list_dir ready", json.Unmarshal([]byte(surveyJSONBody(out)), &resp))
	if resp.Tree == nil || len(resp.Tree.Children) != 3 {
		t.Fatalf("tree = %#v, want only three immediate directories", resp.Tree)
	}
	for _, child := range resp.Tree.Children {
		if len(child.Children) != 0 || !child.ZoomIn {
			t.Fatalf("child = %#v, want a shallow drill target", child)
		}
	}
}

func surveyJSONBody(out string) string {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start < 0 || end < start {
		return out
	}
	return out[start : end+1]
}
