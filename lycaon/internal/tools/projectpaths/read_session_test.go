package projectpaths_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func readSessionContext(t *testing.T) (tools.ToolContext, string) {
	t.Helper()
	ws := t.TempDir()
	testutil.FailErr(t, "mkdir pkg", os.MkdirAll(filepath.Join(ws, "pkg"), 0o755))
	testutil.FailErr(t, "write a.go", os.WriteFile(filepath.Join(ws, "a.go"), []byte("package a\n"), 0o600))
	testutil.FailErr(t, "write pkg/b.go", os.WriteFile(filepath.Join(ws, "pkg", "b.go"), []byte("package pkg\n"), 0o600))
	return tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "r1", Label: "ws", Path: ws, IsPrimary: true}},
			ActiveRootID: "r1"},
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID},
	}, ws
}

func readThroughSession(t *testing.T, s *projectpaths.ReadSession, resolved projectpaths.Resolved) (string, error) {
	t.Helper()
	f, err := s.Open(resolved)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(f)
	testutil.FailErr(t, "read session file", err)
	return string(b), nil
}

// Beneath an admitted root the session answers exactly what ResolveRead does.
func TestReadSessionResolvesLikeResolveRead(t *testing.T) {
	tctx, ws := readSessionContext(t)
	ctx := context.Background()
	session := projectpaths.NewReadSession(nil, tctx)
	defer session.Close()
	for path, want := range map[string]string{
		"a.go": "package a\n", "pkg/b.go": "package pkg\n",
		filepath.Join(ws, "pkg", "b.go"): "package pkg\n", "@ws/a.go": "package a\n",
	} {
		expected, err := projectpaths.ResolveRead(ctx, nil, tctx, path)
		testutil.FailErr(t, "ResolveRead "+path, err)
		got, err := session.Resolve(ctx, path)
		testutil.FailErr(t, "session Resolve "+path, err)
		if got != expected {
			t.Fatalf("session resolved %q as %+v, ResolveRead as %+v", path, got, expected)
		}
		text, err := readThroughSession(t, session, got)
		testutil.FailErr(t, "session Open "+path, err)
		if text != want {
			t.Fatalf("session read %q = %q, want %q", path, text, want)
		}
	}
}

// A root the host refuses is refused for every path, as ResolveRead does.
func TestReadSessionRefusesAnUnsafeRoot(t *testing.T) {
	tctx := tools.ToolContext{
		Source: tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "root", Label: "root", Path: string(filepath.Separator), IsPrimary: true}},
			ActiveRootID: "root"},
	}
	session := projectpaths.NewReadSession(nil, tctx)
	defer session.Close()
	_, err := session.Resolve(context.Background(), "tmp")
	requireReject(t, err, "SANDBOX_CAPABILITY_REQUEST_INVALID")
}

// A link leaving the root resolves lexically but never opens.
func TestReadSessionRefusesLinksLeavingTheRoot(t *testing.T) {
	tctx, ws := readSessionContext(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	testutil.FailErr(t, "write outside", os.WriteFile(outside, []byte("secret"), 0o600))
	testutil.FailErr(t, "link outside", os.Symlink(outside, filepath.Join(ws, "escape.txt")))
	session := projectpaths.NewReadSession(nil, tctx)
	defer session.Close()
	resolved, err := session.Resolve(context.Background(), "escape.txt")
	testutil.FailErr(t, "resolve link", err)
	if text, err := readThroughSession(t, session, resolved); err == nil {
		t.Fatalf("link outside the root read %q", text)
	}
}

// The profile's read globs apply to every session read.
func TestReadSessionEnforcesReadGlobs(t *testing.T) {
	tctx, _ := readSessionContext(t)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}, ReadGlobs: []string{"pkg/**"},
	}})
	session := projectpaths.NewReadSession(boundary, tctx)
	defer session.Close()
	ctx := context.Background()
	_, err := session.Resolve(ctx, "pkg/b.go")
	testutil.FailErr(t, "resolve in scope", err)
	_, sessionErr := session.Resolve(ctx, "a.go")
	_, resolveErr := projectpaths.ResolveRead(ctx, boundary, tctx, "a.go")
	if sessionErr == nil || resolveErr == nil || sessionErr.Error() != resolveErr.Error() {
		t.Fatalf("out-of-scope read: session %v, ResolveRead %v", sessionErr, resolveErr)
	}
}

// Host namespaces resolve through the full rules, never as attached-root paths.
func TestReadSessionDefersScratchToFullResolution(t *testing.T) {
	tctx, scratchDir, _ := scratchToolContext(t)
	testutil.FailErr(t, "write scratch", os.WriteFile(filepath.Join(scratchDir, "note.txt"), []byte("note"), 0o600))
	session := projectpaths.NewReadSession(nil, tctx)
	defer session.Close()
	ctx := context.Background()
	for _, path := range []string{"@scratch/note.txt", filepath.Join(scratchDir, "note.txt")} {
		expected, err := projectpaths.ResolveRead(ctx, nil, tctx, path)
		testutil.FailErr(t, "ResolveRead "+path, err)
		got, err := session.Resolve(ctx, path)
		testutil.FailErr(t, "session Resolve "+path, err)
		if got != expected || !got.External {
			t.Fatalf("scratch %q resolved as %+v, want %+v", path, got, expected)
		}
		text, err := readThroughSession(t, session, got)
		testutil.FailErr(t, "session Open "+path, err)
		if text != "note" {
			t.Fatalf("scratch read %q", text)
		}
	}
}
