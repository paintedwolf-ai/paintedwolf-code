package repomap

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBuildExtractsGoDefinitions(t *testing.T) {
	dir := t.TempDir()
	src := `package sample

func Exported(a int) int { return a + 1 }

type Widget struct{ Name string }

func (w *Widget) Method() string { return w.Name }

func unexported() {}
`
	testutil.FailErr(t, "write sample", os.WriteFile(filepath.Join(dir, "sample.go"), []byte(src), 0o644))

	snap, err := Build(context.Background(), Options{Root: dir})
	testutil.FailErr(t, "Build failed", err)

	if snap.FilesScanned == 0 {
		t.Fatal("expected at least one scanned file")
	}
	if snap.FilesParsed == 0 {
		t.Fatal("expected at least one parsed file")
	}
	if len(snap.Tags) == 0 {
		t.Fatal("expected definitions to be tagged")
	}

	names := map[string]bool{}
	for _, tag := range snap.Tags {
		names[tag.Name] = true
		if tag.File != "sample.go" {
			t.Errorf("unexpected file path %q", tag.File)
		}
		if tag.Language != "go" {
			t.Errorf("expected language=go, got %q", tag.Language)
		}
		if tag.Line < 1 {
			t.Errorf("expected 1-indexed line, got %d", tag.Line)
		}
	}
	for _, want := range []string{"Exported", "Widget"} {
		if !names[want] {
			t.Errorf("missing definition %q (got %v)", want, names)
		}
	}
}

func TestBuildIncludesProjectSourceAtAnyPath(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write top",
		os.WriteFile(filepath.Join(dir, "top.go"), []byte("package x\nfunc Top() {}\n"), 0o644))
	nm := filepath.Join(dir, "node_modules", "pkg")
	testutil.FailErr(t, "mkdir node_modules", os.MkdirAll(nm, 0o755))
	testutil.FailErr(t, "write nm",
		os.WriteFile(filepath.Join(nm, "ignored.go"), []byte("package x\nfunc Ignored() {}\n"), 0o644))
	hidden := filepath.Join(dir, ".project", "generated")
	testutil.FailErr(t, "mkdir hidden", os.MkdirAll(hidden, 0o755))
	testutil.FailErr(t, "write hidden",
		os.WriteFile(filepath.Join(hidden, "schema.go"), []byte("package generated\nfunc Schema() {}\n"), 0o644))

	snap, err := Build(context.Background(), Options{Root: dir})
	testutil.FailErr(t, "Build failed", err)

	names := map[string]bool{}
	for _, tag := range snap.Tags {
		names[tag.Name] = true
	}
	for _, want := range []string{"Top", "Ignored", "Schema"} {
		if !names[want] {
			t.Errorf("missing definition %q (got %v)", want, names)
		}
	}
}

func TestBuildFromInventoryIncludesHiddenAndGeneratedPaths(t *testing.T) {
	dir := t.TempDir()
	paths := []string{".project/generated/schema.go", "node_modules/pkg/ignored.go"}
	source := []byte("package x\nfunc Included() {}\n")
	entries := make([]InventoryEntry, 0, len(paths)+1)
	for _, rel := range paths {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Dir(abs), 0o755))
		testutil.FailErr(t, "write source", os.WriteFile(abs, source, 0o644))
		entries = append(entries, InventoryEntry{Path: rel, Name: filepath.Base(rel), Size: int64(len(source))})
	}
	unknownPath := "scratch/authoritative.xyz"
	unknown := []byte("primary-record = yes\n")
	unknownAbs := filepath.Join(dir, filepath.FromSlash(unknownPath))
	testutil.FailErr(t, "mkdir unknown", os.MkdirAll(filepath.Dir(unknownAbs), 0o755))
	testutil.FailErr(t, "write unknown", os.WriteFile(unknownAbs, unknown, 0o644))
	entries = append(entries, InventoryEntry{Path: unknownPath, Name: filepath.Base(unknownPath), Size: int64(len(unknown))})

	snap, err := BuildFromInventory(context.Background(), Options{Root: dir}, entries)
	testutil.FailErr(t, "build inventory", err)
	if snap.FilesParsed != len(paths) {
		t.Fatalf("files parsed = %d, want %d", snap.FilesParsed, len(paths))
	}
	if snap.SourceFiles != len(entries) {
		t.Fatalf("source files = %d, want %d", snap.SourceFiles, len(entries))
	}
	if snap.View != "map" || !mapContainsPath(snap.Tree, unknownPath) {
		t.Fatalf("unknown-language file missing from map: %+v", snap.Tree)
	}
}

func mapContainsPath(node *Node, want string) bool {
	if node == nil {
		return false
	}
	if node.Path == want {
		return true
	}
	for _, child := range node.Children {
		if mapContainsPath(child, want) {
			return true
		}
	}
	return false
}

func TestBuildSkipsEngineMetadata(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write top",
		os.WriteFile(filepath.Join(dir, "top.go"), []byte("package x\nfunc Top() {}\n"), 0o644))
	engineMeta := filepath.Join(dir, settingsoverlay.DirName(), "rules")
	testutil.FailErr(t, "mkdir overlay", os.MkdirAll(engineMeta, 0o755))
	testutil.FailErr(t, "write meta",
		os.WriteFile(filepath.Join(engineMeta, "config.go"), []byte("package rules\nfunc MetaRule() {}\n"), 0o644))
	// Ordinary directories remain visible.
	product := filepath.Join(dir, "lycaon", "internal")
	testutil.FailErr(t, "mkdir lycaon", os.MkdirAll(product, 0o755))
	testutil.FailErr(t, "write product",
		os.WriteFile(filepath.Join(product, "api.go"), []byte("package internal\nfunc Serve() {}\n"), 0o644))

	snap, err := Build(context.Background(), Options{Root: dir})
	testutil.FailErr(t, "Build failed", err)

	names := map[string]bool{}
	for _, tag := range snap.Tags {
		if strings.Contains(tag.File, settingsoverlay.DirName()) {
			t.Errorf("overlay tag leaked: %+v", tag)
		}
		names[tag.Name] = true
	}
	if !names["Top"] {
		t.Fatal("expected top-level symbol Top")
	}
	if !names["Serve"] {
		t.Fatal("expected product lycaon/ symbol Serve")
	}
	if names["MetaRule"] {
		t.Fatal("symbol from overlay must not appear")
	}
}

// writeManyDefs lays out N directories each holding a Go file with several
// exported symbols, enough to overflow a small byte budget.
func writeManyDefs(t *testing.T, dir string, dirs, defsPer int) {
	t.Helper()
	for d := 0; d < dirs; d++ {
		sub := filepath.Join(dir, "lycaon", "internal", "pkg"+strings.Repeat("x", d+1))
		testutil.FailErr(t, "mkdir", os.MkdirAll(sub, 0o755))
		var b strings.Builder
		b.WriteString("package pkg\n")
		for i := 0; i < defsPer; i++ {
			b.WriteString("func Fn")
			b.WriteString(strings.Repeat("A", d+1))
			b.WriteString(strconv.Itoa(i))
			b.WriteString("() {}\n")
		}
		testutil.FailErr(t, "write defs",
			os.WriteFile(filepath.Join(sub, "defs.go"), []byte(b.String()), 0o644))
	}
}

// Above ParseThreshold the scope returns a structural map and reads nothing.
func TestBuildMapViewWhenOverThreshold(t *testing.T) {
	dir := t.TempDir()
	writeManyDefs(t, dir, 6, 8)

	snap, err := Build(context.Background(), Options{Root: dir, ParseThreshold: 3})
	testutil.FailErr(t, "Build failed", err)

	if snap.View != "map" {
		t.Fatalf("view = %q want map", snap.View)
	}
	if snap.Tree == nil || len(snap.Tree.Children) == 0 {
		t.Fatalf("tree = %+v want children", snap.Tree)
	}
	if snap.Tags != nil {
		t.Fatalf("map view must not carry flat tags, got %d", len(snap.Tags))
	}
	if snap.FilesParsed != 0 {
		t.Fatalf("map view must read no files, files_parsed=%d", snap.FilesParsed)
	}
	if snap.SourceFiles != 6 {
		t.Fatalf("source_files = %d want 6", snap.SourceFiles)
	}
	// File counts roll up by directory from the cheap structural walk.
	if snap.Tree.Files != 6 {
		t.Fatalf("root files = %d want 6", snap.Tree.Files)
	}
	if snap.Diagnostics == nil || snap.Diagnostics.Hint == "" {
		t.Fatal("expected a drill-down hint on the map view")
	}
}

func TestBuildEmptyRootStatesEmpty(t *testing.T) {
	dir := t.TempDir()

	snap, err := Build(context.Background(), Options{Root: dir})
	testutil.FailErr(t, "Build failed", err)
	if snap.View != "tags" {
		t.Fatalf("view = %q want tags", snap.View)
	}
	if snap.Diagnostics == nil || snap.Diagnostics.HintCode != noFilesHintCode {
		t.Fatalf("diagnostics = %+v want hint_code %s", snap.Diagnostics, noFilesHintCode)
	}
	if snap.FilesScanned != 0 || snap.SourceFiles != 0 || snap.TotalDefs != 0 {
		t.Fatalf("empty view has nonzero observations: %+v", snap)
	}

	shallow, err := Build(context.Background(), Options{Root: dir, ShallowRootMap: true})
	testutil.FailErr(t, "shallow Build failed", err)
	if shallow.Diagnostics == nil || shallow.Diagnostics.HintCode != noFilesHintCode {
		t.Fatalf("shallow diagnostics = %+v want hint_code %s", shallow.Diagnostics, noFilesHintCode)
	}

	inv, err := BuildFromInventory(context.Background(), Options{Root: dir}, nil)
	testutil.FailErr(t, "inventory Build failed", err)
	if inv.Diagnostics == nil || inv.Diagnostics.HintCode != noFilesHintCode {
		t.Fatalf("inventory diagnostics = %+v want hint_code %s", inv.Diagnostics, noFilesHintCode)
	}
}

func TestBuildTagsViewUnderThreshold(t *testing.T) {
	dir := t.TempDir()
	writeManyDefs(t, dir, 3, 4)

	snap, err := Build(context.Background(), Options{Root: dir})
	testutil.FailErr(t, "Build failed", err)

	if snap.View != "tags" {
		t.Fatalf("view = %q want tags", snap.View)
	}
	if snap.FilesParsed != 3 {
		t.Fatalf("files_parsed = %d want 3", snap.FilesParsed)
	}
	if len(snap.Tags) != snap.TotalDefs || snap.TotalDefs != 12 {
		t.Fatalf("tags=%d total_defs=%d want 12 each", len(snap.Tags), snap.TotalDefs)
	}
	if snap.Tree != nil {
		t.Fatal("tags view must not carry a tree")
	}
}

func TestBuildMapViewWhenSymbolsOverflowBudget(t *testing.T) {
	dir := t.TempDir()
	writeManyDefs(t, dir, 4, 60)

	snap, err := Build(context.Background(), Options{Root: dir, MaxBytes: 600})
	testutil.FailErr(t, "Build failed", err)

	if snap.View != "map" {
		t.Fatalf("view = %q want map", snap.View)
	}
	if snap.Tree == nil || snap.Tree.Files != 4 {
		t.Fatalf("tree = %+v want all four files", snap.Tree)
	}
	if snap.TotalDefs != 240 {
		t.Fatalf("total_defs = %d want 240", snap.TotalDefs)
	}
	if snap.Truncated {
		t.Fatal("map must represent the full scope")
	}
	if snap.Diagnostics == nil || !strings.Contains(snap.Diagnostics.Hint, "Scope to") {
		t.Fatalf("expected a scope-down hint, got %+v", snap.Diagnostics)
	}

	// Explicit paging still gets the flat tag list — the agent asked for symbols.
	paged, err := Build(context.Background(), Options{Root: dir, MaxBytes: 600, Offset: 10})
	testutil.FailErr(t, "Build paged failed", err)
	if paged.View != "tags" {
		t.Fatalf("offset paging view = %q want tags", paged.View)
	}
}

func TestBuildZoomDepthCapsExpansion(t *testing.T) {
	dir := t.TempDir()
	writeManyDefs(t, dir, 6, 8)

	snap, err := Build(context.Background(), Options{Root: dir, Subpath: "lycaon/internal", ParseThreshold: 3, MaxBytes: 4000, Depth: 1})
	testutil.FailErr(t, "Build failed", err)

	if snap.View != "map" {
		t.Fatalf("view = %q want map", snap.View)
	}
	if len(snap.Tree.Children) == 0 {
		t.Fatal("expected the scoped root to expand its package children")
	}
	// Depth 1 expands the scope root's immediate children but none of their files.
	for _, child := range snap.Tree.Children {
		if len(child.Children) != 0 {
			t.Fatalf("depth 1 expanded grandchildren under %s", child.Path)
		}
	}
}

func TestBuildFileScopedReturnsTags(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write sample",
		os.WriteFile(filepath.Join(dir, "sample.go"), []byte("package x\nfunc Alpha() {}\nfunc Beta() {}\n"), 0o644))

	snap, err := Build(context.Background(), Options{Root: dir, Subpath: "sample.go"})
	testutil.FailErr(t, "Build failed", err)

	if snap.View != "tags" {
		t.Fatalf("view = %q want tags", snap.View)
	}
	names := map[string]bool{}
	for _, tag := range snap.Tags {
		names[tag.Name] = true
	}
	if !names["Alpha"] || !names["Beta"] {
		t.Fatalf("missing file symbols, got %v", names)
	}
}

func TestBuildUnknownLanguageReturnsMap(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write notes", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain notes\n"), 0o644))

	snap, err := Build(context.Background(), Options{Root: dir})
	testutil.FailErr(t, "Build failed", err)

	if snap.FilesParsed != 0 {
		t.Fatalf("expected files_parsed=0, got %d", snap.FilesParsed)
	}
	if snap.View != "map" || !mapContainsPath(snap.Tree, "notes.txt") {
		t.Fatalf("plain-text file missing from map: %+v", snap.Tree)
	}
	if snap.Diagnostics == nil || snap.Diagnostics.SkipReasons.NoGrammar != 1 {
		t.Fatalf("diagnostics = %+v", snap.Diagnostics)
	}
}

func TestBuildMapViewWhenWalkMassExceedsThresholdDespiteLangFilter(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 401; i++ {
		name := filepath.Join(dir, "noise", "file"+strconv.Itoa(i)+".dat")
		testutil.FailErr(t, "mkdir noise", os.MkdirAll(filepath.Dir(name), 0o755))
		testutil.FailErr(t, "write noise", os.WriteFile(name, []byte("x\n"), 0o644))
	}
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	testutil.FailErr(t, "write swift",
		os.WriteFile(filepath.Join(dir, "src", "App.swift"), []byte("struct App {}\n"), 0o644))

	snap, err := Build(context.Background(), Options{
		Root:      dir,
		Languages: []string{"swift"},
	})
	testutil.FailErr(t, "Build failed", err)

	if snap.View != "map" {
		t.Fatalf("view = %q want map when walk mass exceeds threshold", snap.View)
	}
	if snap.FilesScanned <= parseThreshold {
		t.Fatalf("files_scanned = %d want > %d", snap.FilesScanned, parseThreshold)
	}
	if snap.SourceFiles != 1 {
		t.Fatalf("source_files = %d want 1 swift candidate", snap.SourceFiles)
	}
	if snap.FilesParsed != 0 {
		t.Fatalf("map view must not parse, files_parsed=%d", snap.FilesParsed)
	}
}

func TestEmptySymbolViewRetainsSkipReasons(t *testing.T) {
	snapshot := &Snapshot{FilesScanned: 3, SourceFiles: 3}
	skip := SkipStats{NoGrammar: 1, Oversized: 1, ParseIncomplete: 1}
	got := tagsSnapshot(t.Context(), snapshot, nil, Options{Offset: 1}, 1024, skip)
	if got.Diagnostics == nil || got.Diagnostics.HintCode != emptyRepoMapHintCode || got.Diagnostics.SkipReasons != skip {
		t.Fatalf("empty symbols lost observed omissions: %+v", got.Diagnostics)
	}
	if got.FilesScanned != 3 || len(got.Tags) != 0 {
		t.Fatalf("empty symbols changed file observations: %+v", got)
	}
}
