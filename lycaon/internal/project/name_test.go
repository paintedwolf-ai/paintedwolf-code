package project

import (
	"context"
	"path/filepath"
	"testing"
)

type recordingNamer struct {
	calls  int
	text   string
	system string
	user   string
}

func (r *recordingNamer) Name(_ context.Context, system, user string) (string, error) {
	r.calls++
	r.system = system
	r.user = user
	return r.text, nil
}

func TestNameProjectCallsNamerWhenNotMock(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "")
	ctx := context.Background()
	rec := &recordingNamer{text: "Todo App"}
	got := NameProject(ctx, rec, "Build a CLI todo tracker with SQLite persistence")
	if rec.calls != 1 {
		t.Fatalf("Name calls = %d want 1", rec.calls)
	}
	if got != "Todo App" {
		t.Fatalf("name = %q want %q", got, "Todo App")
	}
	if rec.system == "" {
		t.Fatal("system prompt was empty")
	}
	if rec.user != "Build a CLI todo tracker with SQLite persistence" {
		t.Fatalf("user prompt = %q", rec.user)
	}
}

func TestNameProjectSkipsNamerUnderMock(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	ctx := context.Background()
	rec := &recordingNamer{text: "ignored"}
	got := NameProject(ctx, rec, "Build a CLI todo tracker with SQLite persistence")
	if rec.calls != 0 {
		t.Fatalf("Name calls = %d want 0 under mock", rec.calls)
	}
	want := limitProjectNameWords("Build a CLI todo tracker with SQLite persistence")
	if got != want {
		t.Fatalf("name = %q want %q", got, want)
	}
}

func TestDefaultNameForCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-app")
	if got := DefaultNameForCreate(CreateParams{
		Roots: []AttachRootParams{{Path: dir}},
	}); got != "my-app" {
		t.Fatalf("folder create name = %q want my-app", got)
	}
	if got := DefaultNameForCreate(CreateParams{Draft: true}); got != "" {
		t.Fatalf("draft name = %q want empty", got)
	}
	if got := DefaultNameForCreate(CreateParams{
		Draft: true,
		Name:  "Custom",
	}); got != "Custom" {
		t.Fatalf("explicit name = %q want Custom", got)
	}
	primary := true
	if got := DefaultNameForCreate(CreateParams{
		Roots: []AttachRootParams{
			{Path: filepath.Join(t.TempDir(), "secondary")},
			{Path: filepath.Join(t.TempDir(), "primary"), IsPrimary: &primary},
		},
	}); got != "primary" {
		t.Fatalf("primary root name = %q want primary", got)
	}
}

func TestNameFromFolderPath(t *testing.T) {
	if got := NameFromFolderPath("/tmp/projects/widget"); got != "widget" {
		t.Fatalf("NameFromFolderPath = %q want widget", got)
	}
}

func TestLimitProjectNameWords(t *testing.T) {
	if got := limitProjectNameWords("Build a CLI todo tracker"); got != "Build a" {
		t.Fatalf("limitProjectNameWords = %q want Build a", got)
	}
	if got := limitProjectNameWords("Notes"); got != "Notes" {
		t.Fatalf("limitProjectNameWords = %q want Notes", got)
	}
}
