package command

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestRejectCommandNotFoundFromConfineExit(t *testing.T) {
	// Built from confine's own writer, so the fixture cannot drift from it.
	res := &hostcmd.Result{
		ExitCode: confine.CommandNotFoundExit,
		Tail:     confine.HelperStderrPrefix + (&confine.CommandNotFoundError{Name: "missing-tool"}).Error(),
		OK:       false,
	}
	err := rejectCommandNotFound(res, map[string]any{"command": "missing-tool --help"})
	tr := tools.AsToolReject(err)
	if tr == nil {
		t.Fatalf("want ToolReject, got %v", err)
	}
	if tr.Code != "COMMAND_NOT_FOUND" {
		t.Fatalf("code = %q", tr.Code)
	}
	if got, _ := tr.Data["command"].(string); got != "missing-tool" {
		t.Fatalf("command = %q", got)
	}
}

func TestRejectCommandNotFoundIgnoresOtherExit127(t *testing.T) {
	res := &hostcmd.Result{ExitCode: 127, Tail: "some other failure", OK: false}
	if err := rejectCommandNotFound(res, map[string]any{"command": "tool"}); err != nil {
		t.Fatalf("non-confine 127 must not reject: %v", err)
	}
}

func TestRejectStartCommandNotFoundLookPath(t *testing.T) {
	err := rejectStartCommandNotFound(&confine.CommandNotFoundError{Name: "nope"})
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_NOT_FOUND" {
		t.Fatalf("got %v", err)
	}
	if got, _ := tr.Data["command"].(string); got != "nope" {
		t.Fatalf("command = %q", got)
	}
}

func TestRejectStartCommandNotFoundExecErrNotFound(t *testing.T) {
	err := rejectStartCommandNotFound(exec.ErrNotFound)
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_NOT_FOUND" {
		t.Fatalf("got %v", err)
	}
}

func TestRejectStartCommandNotFoundPreservesWrappedExecName(t *testing.T) {
	err := rejectStartCommandNotFound(fmt.Errorf("start command: %w", &exec.Error{
		Name: "missing-command", Err: exec.ErrNotFound,
	}))
	tr := tools.AsToolReject(err)
	if tr == nil || tr.Code != "COMMAND_NOT_FOUND" {
		t.Fatalf("got %v", err)
	}
	if got, _ := tr.Data["command"].(string); got != "missing-command" {
		t.Fatalf("command = %q, want missing-command", got)
	}
}

func TestRejectStartCommandNotFoundIgnoresOther(t *testing.T) {
	if err := rejectStartCommandNotFound(errors.New("boom")); err != nil {
		t.Fatalf("unexpected reject: %v", err)
	}
}

func TestCommandArgv0FromCommandLine(t *testing.T) {
	got := commandArgv0(map[string]any{"command": "mybin sub --flag"})
	if got != "mybin" {
		t.Fatalf("argv0 = %q", got)
	}
}

// The host can read the near miss off PATH rather than sending the model to
// probe for it.
func TestCommandNotFoundNamesResolvableNeighbours(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"toolshed3", "toolshed-next", "unrelated"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	t.Setenv("PATH", dir)

	tr := tools.AsToolReject(commandNotFoundReject("toolshed"))
	if tr == nil {
		t.Fatal("want ToolReject")
	}
	got, _ := tr.Data["resolvable"].(string)
	if got != "toolshed-next, toolshed3" {
		t.Fatalf("resolvable = %q", got)
	}
}

func TestCommandNotFoundStaysSilentWithoutNeighbours(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tr := tools.AsToolReject(commandNotFoundReject("toolshed"))
	if tr == nil {
		t.Fatal("want ToolReject")
	}
	if _, present := tr.Data["resolvable"]; present {
		t.Fatalf("nothing on PATH must state nothing: %v", tr.Data)
	}
}

// An argv0 that is already a path was resolved as written; there is no name to
// complete.
func TestResolvableNeighboursIgnoresPathArgv0(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "toolshed3"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", dir)
	if got := resolvableNeighbours(filepath.Join("bin", "toolshed")); got != nil {
		t.Fatalf("neighbours for a path argv0 = %v", got)
	}
}

// A short name can prefix half of PATH.
func TestResolvableNeighboursAreBounded(t *testing.T) {
	dir := t.TempDir()
	for _, suffix := range []string{"1", "2", "3", "4", "5", "6"} {
		if err := os.WriteFile(filepath.Join(dir, "g"+suffix), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	t.Setenv("PATH", dir)
	if got := resolvableNeighbours("g"); len(got) != neighbourLimit {
		t.Fatalf("neighbours = %v want %d", got, neighbourLimit)
	}
}

// Neighbours come from the resolved PATH commands run with, not the engine's
// own process PATH.
func TestResolvableNeighboursReadTheResolvedPath(t *testing.T) {
	resolved := t.TempDir()
	if err := os.WriteFile(filepath.Join(resolved, "toolshed3"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	lycexec.SetResolvedPathSource(func() string { return resolved })
	t.Cleanup(func() { lycexec.SetResolvedPathSource(nil) })
	if got := resolvableNeighbours("toolshed"); len(got) != 1 || got[0] != "toolshed3" {
		t.Fatalf("neighbours = %v, want [toolshed3] from the resolved PATH", got)
	}
}
