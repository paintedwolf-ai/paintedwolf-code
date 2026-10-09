package command

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func testToolContext(root string) tools.ToolContext {
	return tools.ToolContext{
		Agent: "implement",
		Roots: []projectroot.RootRef{{ID: "primary", Path: root, IsPrimary: true}},
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
	tctx.Agent = "plan_writer"
	_, err := commandIOFor(context.Background(), b, tctx, map[string]any{
		"stdout_to": "src/out.log",
	}, "command")
	if err == nil {
		t.Fatal("expected write-scope rejection")
	}
	var reject *tools.ToolReject
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
	tctx.Agent = "plan_writer"
	_, err := commandIOFor(context.Background(), b, tctx, map[string]any{
		"stdout_to": "src/out.log",
	}, "verify")
	if err == nil {
		t.Fatal("expected write-scope rejection")
	}
	var reject *tools.ToolReject
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
	tctx.Agent = "coordinator"
	_, err := commandIOFor(context.Background(), b, tctx, map[string]any{
		"stdout_to": "src/out.log",
	}, "command")
	if err == nil {
		t.Fatal("expected write-scope rejection")
	}
	var reject *tools.ToolReject
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
	tctx.Agent = "coordinator"
	tctx.TurnSurfaceID = tools.SurfaceImplementInvestigate

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
