package inboundwrite

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
)

func testBoundary() *sandbox.Boundary {
	return sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"http_request": true},
	}})
}

func projectContext(root string) tools.ToolContext {
	return tools.ToolContext{
		Agent: toolprofiles.DefaultToolProfileID,
		Roots: []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}}, ActiveRootID: "root",
	}
}

func TestWriteLandsBytesUnderTheProject(t *testing.T) {
	root := t.TempDir()
	receipt, err := Write(t.Context(), testBoundary(), projectContext(root), "assets/logo.svg", []byte("<svg/>"), "DENIED")
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if receipt.Path != "assets/logo.svg" || receipt.Bytes != 6 {
		t.Fatalf("receipt = %+v", receipt)
	}
	if landed, err := os.ReadFile(filepath.Join(root, "assets", "logo.svg")); err != nil || string(landed) != "<svg/>" {
		t.Fatalf("landed = %q err=%v", landed, err)
	}
}

func TestWriteRefusesProtectedAndEscapingPaths(t *testing.T) {
	for _, dest := range []string{"", "../out", ".git/HEAD", ".env.local", "id_rsa.key", "/etc/hosts"} {
		_, err := Write(t.Context(), testBoundary(), projectContext(t.TempDir()), dest, []byte("x"), "DENIED")
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "DENIED" {
			t.Fatalf("dest %q error = %v, want the caller's denied code", dest, err)
		}
	}
}

func TestDestDenied(t *testing.T) {
	for path, want := range map[string]bool{
		".git":            true,
		".git/objects/ab": true,
		"src/.env":        true,
		"src/.env.prod":   true,
		"certs/a.pem":     true,
		"certs/a.key":     true,
		"src/env.go":      false,
		"docs/key.md":     false,
		"tmp/tree.json":   false,
	} {
		if got := DestDenied(path); got != want {
			t.Fatalf("DestDenied(%q) = %v want %v", path, got, want)
		}
	}
}

func TestFetchedInstructionsRequireReviewOfActualResponse(t *testing.T) {
	root := t.TempDir()
	tc := projectContext(root)
	target := filepath.Join(root, "AGENTS.md")
	declined := errors.New("declined")
	calls := 0
	tc.FileChangeReview = func(_ context.Context, changes []tools.FileChange) error {
		calls++
		if len(changes) != 1 || changes[0].Preview.After != "fetched instructions\n" {
			t.Fatalf("response preview = %+v", changes)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("response landed before approval: %v", err)
		}
		return declined
	}
	_, err := Write(t.Context(), testBoundary(), tc, "AGENTS.md", []byte("fetched instructions\n"), "DENIED")
	if !errors.Is(err, declined) || calls != 1 {
		t.Fatalf("review result=%v calls=%d", err, calls)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("unapproved response exists: %v", err)
	}
}
