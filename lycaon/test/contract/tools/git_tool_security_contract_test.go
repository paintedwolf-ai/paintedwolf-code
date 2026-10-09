package contract

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestGitShowPathEscapeSecurity(t *testing.T) {
	t.Parallel()
	b := contractcheck.ProdToolBoundary(t)
	show := &native.GitShowTool{Git: nil, Boundary: b}
	// An absolute path outside the root is a ResolveRead escape; assertGitReadPath
	// passes that structured reject through instead of GIT_PATH_DENIED.
	_, err := show.Run(context.Background(), map[string]any{
		"path": "/etc/passwd",
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v want SURVEY_PATH_ESCAPE", err)
	}
}

func TestGitRestoreOutsideScopeSecurity(t *testing.T) {
	t.Parallel()
	b := contractcheck.ProdToolBoundary(t)
	restore := &native.GitRestoreTool{Boundary: b}
	_, err := restore.Run(context.Background(), map[string]any{
		"paths": []any{".git/HEAD"},
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	if err == nil {
		t.Fatal("expected restore path denial")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
		t.Fatalf("err = %v want GIT_INTERNALS_WRITE_DENIED", err)
	}
}

func TestGitCommitOutsideScopeSecurity(t *testing.T) {
	t.Parallel()
	b := contractcheck.ProdToolBoundary(t)
	commit := &native.GitCommitTool{Git: git.NewManager(), Boundary: b}
	_, err := commit.Run(context.Background(), map[string]any{
		"message": "nope",
		"paths":   []any{".git/HEAD"},
	}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: t.TempDir(), IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "implement"},
	})
	if err == nil {
		t.Fatal("expected commit path denial")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "GIT_INTERNALS_WRITE_DENIED" {
		t.Fatalf("err = %v want GIT_INTERNALS_WRITE_DENIED", err)
	}
}

func TestGitBlameBinarySkipSecurity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	gittest.Init(t, dir)
	binPath := filepath.Join(dir, "blob.bin")
	testutil.FailErr(t, "write binary", os.WriteFile(binPath, []byte{0x00, 0x01, 0x02}, 0o644))
	gittest.CommitAll(t, dir, "bin")

	b := contractcheck.ProdToolBoundary(t)
	blame := &native.GitBlameTool{Git: git.NewManager(), Boundary: b}
	out, err := blame.Run(ctx, map[string]any{"path": "blob.bin"}, tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: "explore_readonly"},
	})
	if err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary blame should fail without fabricated attribution: output=%q err=%v", out, err)
	}
	var payload map[string]any
	testutil.FailErr(t, "decode binary blame refusal", json.Unmarshal([]byte(out), &payload))
	if payload["available"] != false || payload["lines"] != nil {
		t.Fatalf("binary blame fabricated attribution: %v", payload)
	}
}
