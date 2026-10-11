package toolcommand

import (
	"reflect"
	"testing"
)

func TestGitOperationCommandReplacements(t *testing.T) {
	tests := []struct {
		command, tool string
		args          map[string]any
	}{
		{"git log --oneline -6 benchmark-page", "git_log", map[string]any{"ref": "benchmark-page", "limit": 6}},
		{"git diff main dev", "git_diff", map[string]any{"base_ref": "main", "head_ref": "dev"}},
		{"git log main..feature -- src/main.go", "git_log", map[string]any{"ref": "main..feature", "path": "src/main.go"}},
		{"git checkout main", "git_checkout", map[string]any{"branch": "main"}},
		{"git switch -c feature main", "git_checkout", map[string]any{"branch": "feature", "create": true, "ref": "main"}},
		{"git merge --ff-only feature", "git_merge", map[string]any{"ref": "feature", "mode": "ff_only"}},
		{"git merge --abort", "git_merge", map[string]any{"action": "abort"}},
		{"git merge --no-commit --no-ff main", "git_merge", map[string]any{"ref": "main", "mode": "no_ff", "commit": false}},
		{"git checkout -- AGENTS.md", "git_restore", map[string]any{"paths": []any{"AGENTS.md"}}},
		{"git checkout HEAD -- AGENTS.md docs/AGENTS.md", "git_restore", map[string]any{"paths": []any{"AGENTS.md", "docs/AGENTS.md"}, "source": "HEAD", "staged": true, "worktree": true}},
		{"git stash push -u -m 'Save work' -- 'file name.txt'", "git_stash", map[string]any{"action": "save", "include_untracked": true, "message": "Save work", "paths": []any{"file name.txt"}}},
		{"git stash apply --index stash@{1}", "git_stash", map[string]any{"action": "apply", "reinstate_index": true, "ref": "stash@{1}"}},
		{"git stash list", "git_stash_list", map[string]any{}},
		{"git log --all", "git_log", map[string]any{"all": true}},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got, ok := ExactCommandReplacement(t.Context(), tt.command, t.TempDir(), "")
			if !ok || len(got) != 1 || got[0].Tool != tt.tool || !reflect.DeepEqual(got[0].Args, tt.args) {
				t.Fatalf("replacement=%+v matched=%v", got, ok)
			}
		})
	}
	for _, command := range []string{"git checkout --", "git checkout HEAD -- '*.md'", "git checkout main dev -- file.txt", "git checkout -f -- file.txt", "git restore -- '*.md'", "git checkout -f main", "git switch -C main", "git merge --squash main", "git stash push", "git stash pop", "git stash clear", "git stash push -- .//", "git stash push -- '*.md'", "git log -- '*.md'", "git log --format=%B", "git log -1000"} {
		t.Run(command, func(t *testing.T) {
			if calls, ok := ExactCommandReplacement(t.Context(), command, t.TempDir(), ""); ok {
				t.Fatalf("inexact command redirected: %+v", calls)
			}
		})
	}
}
