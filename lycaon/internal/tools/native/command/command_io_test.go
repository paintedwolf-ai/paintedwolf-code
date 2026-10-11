package command

import (
	"github.com/lycaon/lycaon/internal/toolcontract"

	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func testToolContext(root string) tools.ToolContext {
	return tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "implement"},
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}}},
	}
}

func TestCommandIOStdinMutuallyExclusive(t *testing.T) {
	_, err := commandIOFor(context.Background(), nil, testToolContext(t.TempDir()), map[string]any{
		"stdin":      "x",
		"stdin_from": "file.txt",
	}, "command")
	if err == nil {
		t.Fatal("expected mutual exclusion error")
	}
}

func TestCommandIOStdinFromReadScope(t *testing.T) {
	root := t.TempDir()
	inPath := filepath.Join(root, "in.txt")
	testutil.FailErr(t, "WriteFile", os.WriteFile(inPath, []byte("data"), 0o644))

	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"read": true, "write": true, "command": true}},
	})
	io, err := commandIOFor(context.Background(), b, testToolContext(root), map[string]any{
		"stdin_from": "in.txt",
	}, "command")
	testutil.FailErr(t, "CommandIO in scope", err)
	if io.Stdin == nil || io.StdinFrom != "in.txt" {
		t.Fatalf("io = %#v", io)
	}
}

func TestCommandIOStdinFromRejectsOutOfScope(t *testing.T) {
	root := t.TempDir()
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"read": true, "command": true}},
	})
	_, err := commandIOFor(context.Background(), b, testToolContext(root), map[string]any{
		"stdin_from": "/etc/passwd",
	}, "command")
	if err == nil {
		t.Fatal("expected read-scope rejection for path outside project")
	}
}

func TestCommandIORedirectWriteScope(t *testing.T) {
	root := t.TempDir()
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"write": true, "command": true}},
	})
	io, err := commandIOFor(context.Background(), b, testToolContext(root), map[string]any{
		"stdout_to": "out.log",
		"append":    true,
	}, "command")
	testutil.FailErr(t, "CommandIO stdout_to", err)
	if io.Redirect == nil || io.StdoutTo != "out.log" || io.Redirect.Stdout == nil {
		t.Fatalf("io = %#v", io)
	}
}

func TestCommandIORedirectRejectsOutOfScope(t *testing.T) {
	root := t.TempDir()
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "plan_writer", Tools: map[string]bool{"write": true, "command": true}, WriteGlobs: []string{settingsoverlay.Rel("blueprints/**")}},
	})
	tctx := testToolContext(root)
	tctx.Identity.Agent = "plan_writer"
	_, err := commandIOFor(context.Background(), b, tctx, map[string]any{
		"stdout_to": "src/out.log",
	}, "command")
	if err == nil {
		t.Fatal("expected write-scope rejection")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WRITE_SCOPE_DENIED" {
		t.Fatalf("err = %v want WRITE_SCOPE_DENIED ToolReject", err)
	}
	if reject.Data["kind"] != "redirect" {
		t.Fatalf("kind = %v want redirect", reject.Data["kind"])
	}
	if reject.Data["tool"] != "command" {
		t.Fatalf("tool = %v want command", reject.Data["tool"])
	}
}

func TestCommandIORedirectRejectsOutOfScopeVerify(t *testing.T) {
	root := t.TempDir()
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "plan_writer", Tools: map[string]bool{"write": true, "verify": true}, WriteGlobs: []string{settingsoverlay.Rel("blueprints/**")}},
	})
	tctx := testToolContext(root)
	tctx.Identity.Agent = "plan_writer"
	_, err := commandIOFor(context.Background(), b, tctx, map[string]any{
		"stdout_to": "src/out.log",
	}, "verify")
	if err == nil {
		t.Fatal("expected write-scope rejection")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WRITE_SCOPE_DENIED" {
		t.Fatalf("err = %v want WRITE_SCOPE_DENIED ToolReject", err)
	}
	if reject.Data["kind"] != "redirect" || reject.Data["tool"] != "verify" {
		t.Fatalf("data = %#v", reject.Data)
	}
}

func TestCommandIORedirectRejectsCoordinator(t *testing.T) {
	root := t.TempDir()
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "coordinator", Tools: map[string]bool{"command": true}, WriteGlobs: []string{settingsoverlay.Rel("blueprints/**")}},
	})
	tctx := testToolContext(root)
	tctx.Identity.Agent = "coordinator"
	_, err := commandIOFor(context.Background(), b, tctx, map[string]any{
		"stdout_to": "src/out.log",
	}, "command")
	if err == nil {
		t.Fatal("expected write-scope rejection")
	}
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WRITE_SCOPE_DENIED" {
		t.Fatalf("err = %v want WRITE_SCOPE_DENIED ToolReject (coordinator redirect)", err)
	}
	if reject.Data["kind"] != "redirect" {
		t.Fatalf("kind = %v want redirect", reject.Data["kind"])
	}
}

func TestCommandIOEnvParse(t *testing.T) {
	io, err := commandIOFor(context.Background(), nil, testToolContext(t.TempDir()), map[string]any{
		"env": map[string]any{"MY_FLAG": "1"},
	}, "command")
	testutil.FailErr(t, "CommandIO env", err)
	if len(io.EnvKeys) != 1 || io.InlineEnv["MY_FLAG"] != "1" {
		t.Fatalf("io = %#v", io)
	}
}

// On the investigate surface a redirect crosses the same write scope as write
// and edit: ordinary project and overlay files can receive output.
func TestCommandIORedirectSharesInvestigateWriteScope(t *testing.T) {
	root := t.TempDir()
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "coordinator", Tools: map[string]bool{"command": true}, WriteGlobs: []string{settingsoverlay.Rel("blueprints/**")}},
	})
	scopes, err := sandbox.LoadPathScopes()
	if err != nil {
		t.Fatalf("LoadPathScopes: %v", err)
	}
	b.SetPathScopes(scopes)
	tctx := testToolContext(root)
	tctx.Identity.Agent = "coordinator"
	tctx.Turn.TurnSurfaceID = toolcontract.SurfaceImplementInvestigate

	io, err := commandIOFor(context.Background(), b, tctx, map[string]any{
		"stdout_to": "scratch/git-status.txt",
	}, "command")
	if err != nil {
		t.Fatalf("product-tree redirect on investigate: %v", err)
	}
	if io.StdoutTo == "" || io.Redirect == nil || io.Redirect.Stdout == nil {
		t.Fatalf("redirect not wired: %+v", io)
	}

	_, err = commandIOFor(context.Background(), b, tctx, map[string]any{
		"stdout_to": settingsoverlay.Rel("notes.txt"),
	}, "command")
	testutil.FailErr(t, "resolve ordinary overlay output", err)
}

func TestCommandIOBindsInlineRedirectionPerStage(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(filepath.Join(root, "sub"), 0o755))
	testutil.FailErr(t, "write input", os.WriteFile(filepath.Join(root, "sub", "in.txt"), []byte("x"), 0o644))
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"read": true, "write": true, "command": true}},
	})
	args := map[string]any{"command": "sort < in.txt > out.log 2>> err.log", "cwd": "sub"}
	io, err := commandIOFor(context.Background(), b, testToolContext(root), args, "command")
	testutil.FailErr(t, "CommandIO inline redirections", err)
	if io.Redirect == nil || io.Redirect.Stdout != nil || io.StdoutTo != "" {
		t.Fatalf("inline redirection must stay on its stage, not the call: %#v", io)
	}
	for _, written := range []string{"in.txt", "out.log", "err.log"} {
		loc, ok := io.Redirect.Files[written]
		if !ok || filepath.ToSlash(loc.Rel) != "sub/"+written {
			t.Fatalf("%s bound to %+v (ok=%v), want sub/%s under the cwd", written, loc, ok, written)
		}
	}
	if _, isArg := args["stdout_to"]; isArg {
		t.Fatal("binding mutated the caller's arguments")
	}
}

// commandIOFor resolves a call's stream files, supplying a program when the fixture names none.
func commandIOFor(ctx context.Context, b *sandbox.Boundary, tctx tools.ToolContext, args map[string]any, tool string) (hostcmd.IOParams, error) {
	if _, ok := args["command"]; !ok {
		args = maps.Clone(args)
		args["command"] = "true"
	}
	plan, err := commandsurface.ParsePlan(args)
	if err != nil {
		return hostcmd.IOParams{}, err
	}
	return CommandIO(ctx, b, tctx, plan, args, tool)
}

func TestCommandIOInlineReadResolveFailure(t *testing.T) {
	root := t.TempDir()
	b := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{
		{ID: "implement", Tools: map[string]bool{"read": true, "write": true, "command": true}},
	})
	args := map[string]any{"command": "sort < /outside/missing.txt"}
	_, err := commandIOFor(t.Context(), b, testToolContext(root), args, "command")
	if err == nil {
		t.Fatal("expected error resolving outside inline read")
	}
}

func TestCommandIOParseEnvValidation(t *testing.T) {
	root := t.TempDir()
	tc := testToolContext(root)

	// Line 145: env is not an object
	_, err := commandIOFor(t.Context(), nil, tc, map[string]any{"command": "true", "env": "not-an-object"}, "command")
	if err == nil || !strings.Contains(err.Error(), "env must be an object") {
		t.Fatalf("err = %v, want env must be an object", err)
	}

	// Line 154: env value is not a string
	_, err = commandIOFor(t.Context(), nil, tc, map[string]any{"command": "true", "env": map[string]any{"FOO": 123}}, "command")
	if err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("err = %v, want must be a string", err)
	}

	// Line 158: env key is empty
	_, err = commandIOFor(t.Context(), nil, tc, map[string]any{"command": "true", "env": map[string]any{"   ": "val"}}, "command")
	if err == nil || !strings.Contains(err.Error(), "env key is empty") {
		t.Fatalf("err = %v, want env key is empty", err)
	}
}

func TestCommitCommandOutputWithoutCommitter(t *testing.T) {
	err := commitCommandOutput(t.Context(), tools.ToolContext{}, fseffect.Location{}, strings.NewReader("out"), false)
	if err == nil || !strings.Contains(err.Error(), "output committer not configured") {
		t.Fatalf("err = %v, want output committer not configured", err)
	}
}
