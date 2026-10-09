package contract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lyexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestGitSurveysOutsideRepositoryReturnTypedRefusal(t *testing.T) {
	for _, name := range []string{"git_diff", "git_status"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			executor := toolfixture.ContractToolExecutor(t)
			out, err := executor.Invoke(t.Context(), name, map[string]any{}, tools.ToolContext{
				Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
				ActiveRootID: "r1", Agent: "explore_readonly",
			})
			var refusal *toolrejection.ToolReject
			if !errors.As(err, &refusal) || refusal.Code != "TOOL_OWNER_FAILED" {
				t.Fatalf("%s outside repository = %q, %v; want typed owner refusal", name, out, err)
			}
			if strings.Contains(out, "usage: git") {
				t.Fatalf("CLI usage leaked: %s", out)
			}
		})
	}
}

// The default status page fits the model's tool-result window.
func TestGitStatusPagesLargeTreeContract(t *testing.T) {
	dir := t.TempDir()
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	contractcheck.FailErr(t, "mkdir fixtures", os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755))
	contractcheck.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	const fixtureFiles = 150
	for i := range fixtureFiles {
		rel := filepath.Join("fixtures", fmt.Sprintf("f%03d.txt", i))
		contractcheck.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte("x\n"), 0o644))
	}
	contractcheck.FailErr(t, "write src/main.go", os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644))

	execTool := toolfixture.ContractToolExecutor(t)
	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "coordinator",
	}
	call := func(args map[string]any) map[string]any {
		t.Helper()
		out, err := execTool.Invoke(context.Background(), "git_status", args, tctx)
		contractcheck.FailErr(t, "git_status invoke", err)
		var obj map[string]any
		contractcheck.FailErr(t, "decode git_status", json.Unmarshal([]byte(out), &obj))
		if obj["available"] != true {
			t.Fatalf("git_status unavailable: %s", out)
		}
		return obj
	}
	pagePaths := func(obj map[string]any) []string {
		files, _ := obj["files"].([]any)
		out := make([]string, 0, len(files))
		for _, f := range files {
			out = append(out, f.(map[string]any)["path"].(string))
		}
		return out
	}

	first := call(map[string]any{})
	if got := len(pagePaths(first)); got != toolhost.GitPageFiles {
		t.Fatalf("default page = %d files want %d", got, toolhost.GitPageFiles)
	}
	if first["files_total"].(float64) != fixtureFiles+1 || first["untracked_count"].(float64) != fixtureFiles+1 {
		t.Fatalf("receipt = total %v untracked %v want %d", first["files_total"], first["untracked_count"], fixtureFiles+1)
	}
	seen := map[string]struct{}{}
	obj := first
	for pages := 0; ; pages++ {
		for _, p := range pagePaths(obj) {
			if _, dup := seen[p]; dup {
				t.Fatalf("path %q returned twice", p)
			}
			seen[p] = struct{}{}
		}
		next, more := obj["next_offset"].(float64)
		if !more {
			break
		}
		if pages > 5 {
			t.Fatal("paging did not terminate")
		}
		obj = call(map[string]any{"offset": next})
	}
	if len(seen) != fixtureFiles+1 {
		t.Fatalf("paged %d distinct files want %d", len(seen), fixtureFiles+1)
	}
	if _, collapsed := seen["fixtures/"]; collapsed {
		t.Fatal("untracked directory was collapsed instead of listed per file")
	}

	narrowed := call(map[string]any{"paths": []any{"src"}})
	if got := pagePaths(narrowed); len(got) != 1 || got[0] != "src/main.go" {
		t.Fatalf("paths=[src] selected %v", got)
	}
	if narrowed["files_total"].(float64) != 1 || narrowed["files_truncated"] != false {
		t.Fatalf("narrowed receipt = %v", narrowed)
	}
}

// contractGitRepo initialises a repository with one commit so diffs have a HEAD.
func contractGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = lyexec.LocalGitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v %s", args, err, out)
		}
	}
	initCmd := exec.CommandContext(t.Context(), "git", "init", dir)
	initCmd.Env = lyexec.LocalGitEnv()
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	git("config", "user.email", "test@example.com")
	git("config", "user.name", "Test")
	contractcheck.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	for i := range 12 {
		rel := filepath.Join("src", fmt.Sprintf("f%02d.go", i))
		contractcheck.FailErr(t, "write "+rel, os.WriteFile(filepath.Join(dir, rel), []byte("package src\n\nvar v = 0\n"), 0o644))
	}
	git("add", "-A")
	git("commit", "-m", "init")
	return dir
}

// The producer retains selected hunks and their byte counts until screening.
func TestGitDiffRetainsSelectedHunksUntilScreenedProjectionContract(t *testing.T) {
	dir := contractGitRepo(t)
	// Each modified file carries ~1.2 KiB of hunk text, so twelve of them
	// overflow the 5 KiB default page several times over.
	body := "package src\n\n" + strings.Repeat("// a line of commentary that pads the hunk to a realistic width\n", 18) + "var v = 1\n"
	for i := range 12 {
		rel := filepath.Join("src", fmt.Sprintf("f%02d.go", i))
		contractcheck.FailErr(t, "modify "+rel, os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644))
	}
	contractcheck.FailErr(t, "write untracked", os.WriteFile(filepath.Join(dir, "src", "new.go"), []byte("package src\n\nvar fresh = true\n"), 0o644))

	execTool := toolfixture.ContractToolExecutor(t)
	tctx := tools.ToolContext{
		Roots:        []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
		ActiveRootID: "r1",
		Agent:        "coordinator",
	}
	call := func(args map[string]any) (map[string]any, string) {
		t.Helper()
		out, err := execTool.Invoke(context.Background(), "git_diff", args, tctx)
		contractcheck.FailErr(t, "git_diff invoke", err)
		var obj map[string]any
		contractcheck.FailErr(t, "decode git_diff", json.Unmarshal([]byte(out), &obj))
		if obj["available"] != true {
			t.Fatalf("git_diff unavailable: %s", out)
		}
		return obj, out
	}
	entries := func(obj map[string]any) []map[string]any {
		files, _ := obj["files"].([]any)
		out := make([]map[string]any, 0, len(files))
		for _, f := range files {
			out = append(out, f.(map[string]any))
		}
		return out
	}

	first, raw := call(map[string]any{})
	if tokenest.EstimateDefault(raw) < compaction.DefaultCompactionConfig().ChunkTokenThreshold {
		t.Fatal("fixture must exercise retention before model compaction")
	}
	if first["files_total"].(float64) != 12 || first["max_bytes"].(float64) != git.DefaultGitDiffPageBytes {
		t.Fatalf("receipt = %v", first)
	}
	page := entries(first)
	if len(page) != 12 {
		t.Fatalf("producer must retain every selected file before screening, got %d", len(page))
	}
	used := 0
	for _, e := range page {
		hunk, _ := e["diff"].(string)
		if e["diff_bytes"] != float64(len(hunk)) {
			t.Fatalf("entry lost original byte count: %v", e)
		}
		if !strings.HasPrefix(hunk, "diff --git ") || e["insertions"].(float64) < 18 {
			t.Fatalf("entry lacks whole hunks: %v", e)
		}
		if e["diff_truncated"] == true {
			t.Fatalf("whole files must not be cut on a multi-file page: %v", e)
		}
		used += len(hunk)
	}
	if used <= git.DefaultGitDiffPageBytes {
		t.Fatalf("fixture must exceed the model hunk budget, got %d bytes", used)
	}
	seen := map[string]struct{}{}
	obj := first
	for pages := 0; ; pages++ {
		for _, e := range entries(obj) {
			p := e["path"].(string)
			if _, dup := seen[p]; dup {
				t.Fatalf("path %q returned twice", p)
			}
			seen[p] = struct{}{}
		}
		next, more := obj["next_offset"].(float64)
		if !more {
			if obj["files_truncated"] != false {
				t.Fatalf("last page must not be truncated: %v", obj)
			}
			break
		}
		if pages > 12 {
			t.Fatal("paging did not terminate")
		}
		obj, _ = call(map[string]any{"offset": next})
	}
	if len(seen) != 12 {
		t.Fatalf("paged %d distinct files want 12", len(seen))
	}

	stat, _ := call(map[string]any{"stat": true})
	if got := entries(stat); len(got) != 12 || got[0]["diff"] != nil || stat["files_truncated"] != false {
		t.Fatalf("stat mode must list every file without hunks: %v", stat)
	}

	untracked, _ := call(map[string]any{"untracked": true, "paths": []any{"src/new.go"}})
	if got := entries(untracked); len(got) != 1 || got[0]["untracked"] != true || got[0]["insertions"].(float64) != 3 || !strings.Contains(got[0]["diff"].(string), "+var fresh = true") {
		t.Fatalf("untracked file must appear as an addition with counts: %v", untracked)
	}

	base, _ := call(map[string]any{"base_ref": "HEAD", "paths": []any{"src/f00.go"}})
	if got := entries(base); len(got) != 1 || base["base_ref"] != "HEAD" || got[0]["insertions"].(float64) < 18 {
		t.Fatalf("base_ref HEAD must diff the worktree against HEAD: %v", base)
	}
	if _, err := execTool.Invoke(context.Background(), "git_diff", map[string]any{"base_ref": "--output=/tmp/x"}, tctx); err == nil {
		out, _ := execTool.Invoke(context.Background(), "git_diff", map[string]any{"base_ref": "--output=/tmp/x"}, tctx)
		if !strings.Contains(out, `"available":false`) {
			t.Fatalf("an option-shaped base_ref must be refused, got %s", out)
		}
	}
}
