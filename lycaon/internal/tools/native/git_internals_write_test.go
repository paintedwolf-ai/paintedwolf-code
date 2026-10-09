package native

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestWriteEditGitInternalsDenied(t *testing.T) {
	cases := []struct {
		path  string
		class string
	}{
		{".git/hooks/pre-commit", "hooks"},
		{".git/hooks/post-checkout", "hooks"},
		{".git/config", "config"},
	}
	for _, tc := range cases {
		t.Run("write/"+tc.path, func(t *testing.T) {
			dir := t.TempDir()
			testutil.FailErr(t, "mkdir .git/hooks", os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
			tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
			_, err := tool.Run(context.Background(), map[string]any{
				"path":    tc.path,
				"content": "#!/bin/sh\n",
			}, nativefixture.Context(dir))
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
				t.Fatalf("err = %v, want GIT_INTERNALS_WRITE_DENIED", err)
			}
			if reject.Data["class"] != tc.class {
				t.Fatalf("class = %v, want %s", reject.Data["class"], tc.class)
			}
		})
		t.Run("edit/"+tc.path, func(t *testing.T) {
			dir := t.TempDir()
			abs := filepath.Join(dir, filepath.FromSlash(tc.path))
			testutil.FailErr(t, "mkdir parent", os.MkdirAll(filepath.Dir(abs), 0o755))
			testutil.FailErr(t, "seed file", os.WriteFile(abs, []byte("old\n"), 0o644))
			tool := &EditTool{Boundary: nativefixture.Boundary(t)}
			_, err := tool.Run(context.Background(), map[string]any{
				"path":       tc.path,
				"old_string": "old",
				"new_string": "new",
			}, nativefixture.Context(dir))
			var reject *toolrejection.ToolReject
			if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
				t.Fatalf("err = %v, want GIT_INTERNALS_WRITE_DENIED", err)
			}
			if reject.Data["class"] != tc.class {
				t.Fatalf("class = %v, want %s", reject.Data["class"], tc.class)
			}
		})
	}
}

func TestWriteGitIndexRequiresNativeGitRoute(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.MkdirAll(filepath.Join(dir, ".git"), 0o755))
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"path": ".git/index", "content": "not-a-real-index",
	}, nativefixture.Context(dir))
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" || reject.Data["class"] != "metadata" {
		t.Fatalf("index write did not require the native Git route: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "index")); !os.IsNotExist(err) {
		t.Fatalf("index write had an effect: %v", err)
	}
}
