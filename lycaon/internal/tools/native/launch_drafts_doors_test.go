package native

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/native/terminal"
)

type launchDoorWorld struct {
	t         *testing.T
	dir       string
	documents *nativefixture.DiskDrafts
	registry  *tools.DefaultRegistry
	capture   *command.CommandTool
	tc        tools.ToolContext
	terminal  string
}

func newLaunchDoorWorld(t *testing.T) *launchDoorWorld {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix openpty")
	}
	repochange.DisableWatchersForTest()
	t.Cleanup(repochange.ResetWatchersForTest)
	f := newNativeWalkFixture(t)
	bg := newTestBackgroundRegistry(t)
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register terminal tools", RegisterTerminalSessionTools(registry, bg))
	w := &launchDoorWorld{t: t, dir: f.dir, registry: registry, tc: f.tc}
	w.documents = &nativefixture.DiskDrafts{Blocked: map[string]string{}, Publish: func(path, text string) error {
		return os.WriteFile(filepath.Join(f.dir, filepath.FromSlash(path)), []byte(text), 0o644)
	}}
	w.tc.Source.EditorDocuments = w.documents
	w.tc.Effects.Out = &tools.ToolInvocationOut{}
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true}, []sandbox.ToolProfile{{ID: w.tc.Identity.Agent, Tools: map[string]bool{"command": true}}})
	w.capture = &command.CommandTool{Runner: hostcmd.NewRunner(), Boundary: boundary, Background: bg}
	testutil.FailErr(t, "make subdirectory", os.Mkdir(filepath.Join(f.dir, "sub"), 0o755))
	w.write("a.txt", "saved\n")
	w.write("sub/b.txt", "saved b\n")
	w.script("copy.sh", "cp a.txt witness.txt\n")
	w.script("copy-on-enter.sh", "IFS= read -r _\ncp a.txt witness.txt\nIFS= read -r _\n")
	return w
}

func (w *launchDoorWorld) write(path, content string) {
	w.t.Helper()
	testutil.FailErr(w.t, "write "+path, os.WriteFile(filepath.Join(w.dir, filepath.FromSlash(path)), []byte(content), 0o644))
}

func (w *launchDoorWorld) script(name, body string) {
	w.t.Helper()
	testutil.FailErr(w.t, "write "+name, os.WriteFile(filepath.Join(w.dir, name), []byte("#!/bin/sh\n"+body), 0o755))
}

func (w *launchDoorWorld) read(path string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(w.dir, filepath.FromSlash(path)))
	return string(raw), err == nil
}

func (w *launchDoorWorld) witness() string {
	w.t.Helper()
	testutil.WaitFor(w.t, "the launched process copies the file", func() bool {
		_, ok := w.read("witness.txt")
		return ok
	})
	got, _ := w.read("witness.txt")
	return got
}

func (w *launchDoorWorld) run(tool string, args map[string]any) (string, error) {
	tc := w.tc
	tc.Invocation.ToolName = tool
	return w.registry.Run(w.t.Context(), tool, args, tc)
}

func (w *launchDoorWorld) openTerminal(script string) (terminal.OpenResult, error) {
	w.t.Helper()
	raw, err := w.run(terminal.OpenToolName, map[string]any{"command": filepath.Join(w.dir, script)})
	if err != nil {
		return terminal.OpenResult{}, err
	}
	var opened terminal.OpenResult
	testutil.FailErr(w.t, "decode terminal_open", json.Unmarshal([]byte(raw), &opened))
	w.t.Cleanup(func() {
		_, _ = w.registry.Run(context.Background(), terminal.CloseToolName, map[string]any{"id": opened.ID}, w.tc)
	})
	return opened, nil
}

type launchDoor struct {
	name string
	tool string
	// prepare opens any terminal needed before the draft is typed.
	prepare func(w *launchDoorWorld)
	launch  func(w *launchDoorWorld) (toolcontract.LaunchDrafts, error)
}

var launchDoors = []launchDoor{
	{name: "command terminal capture", tool: "command", launch: func(w *launchDoorWorld) (toolcontract.LaunchDrafts, error) {
		tc := w.tc
		tc.Invocation.ToolName = "command"
		raw, err := w.capture.Run(w.t.Context(), map[string]any{"command": "./copy.sh", "terminal_capture": map[string]any{}}, tc)
		if err != nil {
			return toolcontract.LaunchDrafts{}, err
		}
		var result toolcontract.LaunchDrafts
		testutil.FailErr(w.t, "decode capture result", json.Unmarshal([]byte(raw), &result))
		return result, nil
	}},
	{name: "terminal open", tool: terminal.OpenToolName, launch: func(w *launchDoorWorld) (toolcontract.LaunchDrafts, error) {
		opened, err := w.openTerminal("copy.sh")
		return opened.LaunchDrafts, err
	}},
	{name: "terminal send", tool: terminal.SendToolName,
		prepare: func(w *launchDoorWorld) {
			opened, err := w.openTerminal("copy-on-enter.sh")
			testutil.FailErr(w.t, "open the terminal the send feeds", err)
			w.terminal = opened.ID
		},
		launch: func(w *launchDoorWorld) (toolcontract.LaunchDrafts, error) {
			raw, err := w.run(terminal.SendToolName, map[string]any{"id": w.terminal, "input": "{Enter}"})
			if err != nil {
				return toolcontract.LaunchDrafts{}, err
			}
			var sent terminal.SendResult
			testutil.FailErr(w.t, "decode terminal_send", json.Unmarshal([]byte(raw), &sent))
			return sent.LaunchDrafts, nil
		},
	},
}

func enterLaunchDoor(t *testing.T, door launchDoor) *launchDoorWorld {
	t.Helper()
	w := newLaunchDoorWorld(t)
	if door.prepare != nil {
		door.prepare(w)
	}
	w.documents.ForgetCalls()
	return w
}

func TestEveryLaunchDoorSavesDraftsBeforeItsProcessReads(t *testing.T) {
	for _, door := range launchDoors {
		t.Run(door.name, func(t *testing.T) {
			w := enterLaunchDoor(t, door)
			w.documents.Type("a.txt", "typed by the person\n")
			w.documents.Type("sub/b.txt", "typed in a subdirectory\n")
			left, err := door.launch(w)
			testutil.FailErr(t, "launch", err)
			if got := w.witness(); got != "typed by the person\n" {
				t.Fatalf("the process read %q, not the draft", got)
			}
			if got, _ := w.read("sub/b.txt"); got != "typed in a subdirectory\n" {
				t.Fatalf("a draft outside the process's file stayed unsaved: %q", got)
			}
			if !reflect.DeepEqual(left, toolcontract.LaunchDrafts{}) {
				t.Fatalf("result states unsaved drafts: %+v", left)
			}
			if calls := w.documents.Calls(); len(calls) != 1 || calls[0].ToolName != door.tool || calls[0].SessionID != w.tc.Identity.SessionID {
				t.Fatalf("saves = %+v, want one attributed to %s", calls, door.tool)
			}
		})
	}
}

func TestEveryLaunchDoorNamesDraftsItCouldNotSave(t *testing.T) {
	for _, door := range launchDoors {
		for _, cause := range []string{toolcontract.UnsavedChangedOutside, toolcontract.UnsavedNotWritten, toolcontract.UnsavedAgentEditHeld} {
			t.Run(door.name+"/"+cause, func(t *testing.T) {
				w := enterLaunchDoor(t, door)
				w.documents.Type("a.txt", "typed but unsavable\n")
				w.documents.Type("sub/b.txt", "typed in a subdirectory\n")
				w.documents.Blocked["a.txt"] = cause
				left, err := door.launch(w)
				testutil.FailErr(t, "launch", err)
				if got := w.witness(); got != "saved\n" {
					t.Fatalf("the process read %q, want the earlier bytes", got)
				}
				if got, _ := w.read("sub/b.txt"); got != "typed in a subdirectory\n" {
					t.Fatalf("the saveable draft stayed unsaved: %q", got)
				}
				want := toolcontract.LaunchDrafts{UnsavedDocuments: []toolcontract.UnsavedDocument{{Path: "a.txt", Cause: cause}}}
				if !reflect.DeepEqual(left, want) {
					t.Fatalf("unsaved = %+v, want %+v", left, want)
				}
			})
		}
	}
}

func TestEveryLaunchDoorBoundsTheDraftsItNames(t *testing.T) {
	for _, door := range launchDoors {
		t.Run(door.name, func(t *testing.T) {
			w := enterLaunchDoor(t, door)
			var want []toolcontract.UnsavedDocument
			for i := range toolcontract.MaxUnsavedDocuments + 3 {
				path := "f" + string(rune('a'+i)) + ".txt"
				w.documents.Type(path, "typed\n")
				w.documents.Blocked[path] = toolcontract.UnsavedChangedOutside
				want = append(want, toolcontract.UnsavedDocument{Path: path, Cause: toolcontract.UnsavedChangedOutside})
			}
			sort.Slice(want, func(i, j int) bool { return want[i].Path < want[j].Path })
			left, err := door.launch(w)
			testutil.FailErr(t, "launch", err)
			if !reflect.DeepEqual(left.UnsavedDocuments, want[:toolcontract.MaxUnsavedDocuments]) || left.OmittedUnsavedDocuments != 3 {
				t.Fatalf("named %+v, omitted %d", left.UnsavedDocuments, left.OmittedUnsavedDocuments)
			}
		})
	}
}

func TestEveryLaunchDoorStartsNothingWhenTheSaveCannotRun(t *testing.T) {
	failure := errors.New("document store unavailable")
	for _, door := range launchDoors {
		t.Run(door.name, func(t *testing.T) {
			w := enterLaunchDoor(t, door)
			w.documents.Type("a.txt", "typed by the person\n")
			w.documents.Err = failure
			if _, err := door.launch(w); !errors.Is(err, failure) {
				t.Fatalf("err = %v, want the save's failure", err)
			}
			if got, started := w.read("witness.txt"); started {
				t.Fatalf("a process ran without the save and read %q", got)
			}
			w.documents.Err = nil
			_, err := door.launch(w)
			testutil.FailErr(t, "launch once the save runs", err)
			if got := w.witness(); got != "typed by the person\n" {
				t.Fatalf("the failed call reached its process: it read %q", got)
			}
		})
	}
}

func TestCallsThatStartNoProcessSaveNoDraft(t *testing.T) {
	w := newLaunchDoorWorld(t)
	opened, err := w.openTerminal("copy-on-enter.sh")
	testutil.FailErr(t, "open terminal", err)
	w.documents.ForgetCalls()
	w.documents.Type("a.txt", "typed by the person\n")
	for _, c := range []struct {
		name string
		call func() (string, error)
	}{
		{"terminal read", func() (string, error) {
			return w.run(terminal.ReadToolName, map[string]any{"id": opened.ID})
		}},
		{"terminal snapshot", func() (string, error) {
			return w.run(terminal.SnapshotToolName, map[string]any{"id": opened.ID})
		}},
		{"terminal send to no terminal", func() (string, error) {
			return w.run(terminal.SendToolName, map[string]any{"id": "no-such-terminal", "input": "{Enter}"})
		}},
		{"terminal send of unknown control", func() (string, error) {
			return w.run(terminal.SendToolName, map[string]any{"id": opened.ID, "input": "{NoSuchKey}"})
		}},
		{"terminal open without a command", func() (string, error) {
			return w.run(terminal.OpenToolName, map[string]any{})
		}},
		{"terminal open without an executable", func() (string, error) {
			return w.run(terminal.OpenToolName, map[string]any{"command": "./missing-command"})
		}},
		{"terminal capture without an executable", func() (string, error) {
			return w.capture.Run(t.Context(), map[string]any{"command": "./missing-command", "terminal_capture": map[string]any{}}, w.tc)
		}},
		{"snapshot capture without an executable", func() (string, error) {
			return w.capture.Run(t.Context(), map[string]any{"command": "./missing-command", "snapshot_capture": map[string]any{}}, w.tc)
		}},
		{"desktop capture", func() (string, error) {
			return w.capture.Run(t.Context(), map[string]any{"command": "screencapture out.png", "terminal_capture": map[string]any{}}, w.tc)
		}},
		{"terminal close", func() (string, error) {
			return w.run(terminal.CloseToolName, map[string]any{"id": opened.ID})
		}},
	} {
		_, _ = c.call()
		if calls := w.documents.Calls(); len(calls) != 0 {
			t.Fatalf("%s saved drafts: %+v", c.name, calls)
		}
		if got, _ := w.read("a.txt"); got != "saved\n" {
			t.Fatalf("%s wrote the draft to disk: %q", c.name, got)
		}
	}
}

func TestCapturedLaunchRunsTheSavedExecutable(t *testing.T) {
	for _, capture := range []string{"terminal_capture", "snapshot_capture", "terminal_open"} {
		t.Run(capture, func(t *testing.T) {
			w := newLaunchDoorWorld(t)
			script := "#!/bin/sh\nprintf 'updated executable' > witness.txt\n"
			if capture == "snapshot_capture" {
				script += "exit 42\n"
			}
			w.documents.Type("copy.sh", script)
			w.documents.Publish = func(path, text string) error {
				replacement := filepath.Join(w.dir, "replacement.sh")
				if err := os.WriteFile(replacement, []byte(text), 0o755); err != nil {
					return err
				}
				return os.Rename(replacement, filepath.Join(w.dir, path))
			}
			var err error
			if capture == "terminal_open" {
				_, err = w.openTerminal("copy.sh")
			} else {
				_, err = w.capture.Run(t.Context(), map[string]any{"command": "./copy.sh", capture: map[string]any{}}, w.tc)
			}
			testutil.FailErr(t, "launch the saved executable", err)
			if got := w.witness(); got != "updated executable" {
				t.Fatalf("launch ran the wrong executable: witness=%q", got)
			}
		})
	}
}
