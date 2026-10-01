//go:build unix

package native

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

func TestChownToolSetsCurrentOwnership(t *testing.T) {
	tmpDir := t.TempDir()
	script := filepath.Join(tmpDir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n"), 0o644); err != nil {
		testutil.FailErr(t, "write script", err)
	}
	tool := &ChownTool{Boundary: nativefixture.Boundary(t)}
	out, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"run.sh"},
		"owner": "current",
		"group": "current",
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "chown current", err)
	uid := strconv.Itoa(os.Getuid())
	gid := strconv.Itoa(os.Getgid())
	if !strings.Contains(out, `"uid_after":`+uid) || !strings.Contains(out, `"gid_after":`+gid) {
		t.Fatalf("out = %q", out)
	}
}

func TestChownToolAcceptsUsername(t *testing.T) {
	u, err := user.Current()
	testutil.FailErr(t, "user.Current", err)
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "bin"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ChownTool{Boundary: nativefixture.Boundary(t)}
	_, err = tool.Run(context.Background(), map[string]any{
		"paths": []any{"bin"},
		"owner": u.Username,
	}, nativefixture.Context(tmpDir))
	testutil.FailErr(t, "chown username", err)
}

func TestChownToolRejectsForeignUID(t *testing.T) {
	if os.Getuid() == 65534 {
		t.Skip("effective uid is nobody")
	}
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ChownTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"a.txt"},
		"owner": "65534",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CHOWN_TARGET_DENIED" {
		t.Fatalf("err = %v want CHOWN_TARGET_DENIED", err)
	}
}

func TestChownToolRejectsRootWhenNotRoot(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("running as root")
	}
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ChownTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"a.txt"},
		"owner": "0",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CHOWN_ROOT_DENIED" {
		t.Fatalf("err = %v want CHOWN_ROOT_DENIED", err)
	}
}

func TestChownToolRejectsRecursive(t *testing.T) {
	tmpDir := t.TempDir()
	tool := &ChownTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths":     []any{"a.txt"},
		"owner":     "current",
		"recursive": true,
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CHOWN_RECURSIVE_DENIED" {
		t.Fatalf("err = %v want CHOWN_RECURSIVE_DENIED", err)
	}
}

func TestChownToolRejectsGitPath(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
		testutil.FailErr(t, "mkdir .git", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "config"), []byte("x"), 0o644); err != nil {
		testutil.FailErr(t, "write config", err)
	}
	tool := &ChownTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{".git/config"},
		"owner": "current",
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
		t.Fatalf("err = %v want GIT_INTERNALS_WRITE_DENIED", err)
	}
}

func TestChownToolRejectsUnknownUser(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("a"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	tool := &ChownTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), map[string]any{
		"paths": []any{"a.txt"},
		"owner": fmt.Sprintf("lycaon-no-such-user-%d", os.Getpid()),
	}, nativefixture.Context(tmpDir))
	var reject *tools.ToolReject
	if !errors.As(err, &reject) || reject.Code != "CHOWN_USER_UNKNOWN" {
		t.Fatalf("err = %v want CHOWN_USER_UNKNOWN", err)
	}
}
