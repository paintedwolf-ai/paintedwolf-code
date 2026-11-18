package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// diskReader is an installed program that writes a file's bytes as it finds
// them on disk.
const diskReader = `python3 -c 'import sys; sys.stdout.buffer.write(open(sys.argv[1],"rb").read())'`

// launchShapes are the ways a program reaches an installed process. %[1]s is
// the file as addressed from the program's directory.
var launchShapes = []struct {
	name, source string
	// dir is the directory the process runs in.
	dir string
}{
	{name: "alone", source: diskReader + " %[1]s"},
	{name: "after a native read", source: "cat %[1]s > /dev/null; " + diskReader + " %[1]s"},
	{name: "pipeline stage", source: "cat %[1]s | " + diskReader + " %[1]s"},
	{name: "subshell", source: "(" + diskReader + " %[1]s)"},
	{name: "background job", source: diskReader + " %[1]s & wait"},
	{name: "second launch", source: "python3 -c 'pass'; " + diskReader + " %[1]s"},
	{name: "from a subdirectory", source: "cd nested && " + diskReader + " %[1]s", dir: "nested"},
}

// launchPlacements are where a draft sits relative to the program's directory.
var launchPlacements = []struct {
	path, eol string
}{
	{path: "a.txt", eol: "lf"},
	{path: "nested/b.txt", eol: "lf"},
	{path: "nested/deep/c.txt", eol: "crlf"},
	{path: "sibling/d.txt", eol: "lf"},
}

func typeHumanDraftEOL(t *testing.T, service *editordoc.Service, p *project.Project, path, content, eol string) *editordoc.Document {
	t.Helper()
	doc, err := service.Open(t.Context(), p, path, "r1", "", "window", nil)
	testutil.FailErr(t, "open "+path, err)
	typed, err := service.ReplaceSnapshot(t.Context(), doc.ID, p.ID, editordoc.SnapshotReplacement{DocumentCommand: editordoc.DocumentCommand{ClientID: "window", OperationID: uuid.NewString(), ExpectedRevision: doc.Revision}, Content: content, EOL: eol})
	testutil.FailErr(t, "type "+path, err)
	return typed
}

func writeTreeFile(t *testing.T, f *programtest.Fixture, path, content string) {
	t.Helper()
	abs := filepath.Join(f.Dir, filepath.FromSlash(path))
	testutil.FailErr(t, "make directory for "+path, os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write "+path, os.WriteFile(abs, []byte(content), 0o644))
}

func readTreeFile(t *testing.T, f *programtest.Fixture, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.Dir, filepath.FromSlash(path)))
	testutil.FailErr(t, "read "+path, err)
	return string(raw)
}

// Whatever shape launches an installed process, and wherever a draft sits
// relative to the process's directory, the process reads the bytes a native
// read serves, every draft in the tree is saved, and nothing is left unsaved.
func TestProgramLaunchReadsTheBytesNativeReadsServe(t *testing.T) {
	for _, shape := range launchShapes {
		for _, read := range launchPlacements {
			t.Run(shape.name+"/"+read.path, func(t *testing.T) {
				f, service, p := documentProgram(t)
				documents := map[string]*editordoc.Document{}
				for _, placed := range launchPlacements {
					saved, typed := "saved\n", "typed in "+placed.path+"\nsecond line\n"
					if placed.eol == "crlf" {
						saved = "saved\r\n"
					}
					writeTreeFile(t, f, placed.path, saved)
					documents[placed.path] = typeHumanDraftEOL(t, service, p, placed.path, typed, placed.eol)
				}
				native := callDocumentProgram(t, f, "cat "+read.path)
				if native.Status != 0 || native.Stderr != "" {
					t.Fatalf("native read=%+v", native)
				}
				for path, doc := range documents {
					if live, err := service.CurrentSnapshot(t.Context(), p.ID, doc.ID); err != nil || !live.Dirty {
						t.Fatalf("a native read saved %s: %+v, %v", path, live, err)
					}
				}

				addressed, err := filepath.Rel(shape.dir, read.path)
				testutil.FailErr(t, "address from the program directory", err)
				launched := callDocumentProgram(t, f, fmt.Sprintf(shape.source, filepath.ToSlash(addressed)))
				if launched.Status != 0 || launched.Stderr != "" || launched.StopCode != "" {
					t.Fatalf("launch=%+v", launched)
				}
				if launched.Stdout != native.Stdout {
					t.Fatalf("the process read %q, the native read served %q", launched.Stdout, native.Stdout)
				}
				if !reflect.DeepEqual(launched.LaunchDrafts, toolcontract.LaunchDrafts{}) {
					t.Fatalf("unsaved after a clean launch: %+v", launched.LaunchDrafts)
				}
				for path, doc := range documents {
					live, err := service.CurrentSnapshot(t.Context(), p.ID, doc.ID)
					testutil.FailErr(t, "document after launch", err)
					want := doc.Draft
					if doc.EOL == "crlf" {
						want = strings.ReplaceAll(want, "\n", "\r\n")
					}
					if live.Dirty || live.Draft != doc.Draft || readTreeFile(t, f, path) != want {
						t.Fatalf("%s after launch: dirty=%v disk=%q want=%q", path, live.Dirty, readTreeFile(t, f, path), want)
					}
				}
			})
		}
	}
}

// refusedLaunches never start a process.
var refusedLaunches = []struct {
	name, source string
	arrange      func(t *testing.T, f *programtest.Fixture)
	stopCode     string
	status       int
}{
	{name: "native reads only", source: "cat a.txt; cat < a.txt"},
	{name: "program not installed", source: "painted-wolf-no-such-program a.txt", status: 127},
	{name: "external execution not allowed", source: diskReader + " a.txt", stopCode: "COMMAND_EFFECT_NOT_ALLOWED", arrange: func(t *testing.T, f *programtest.Fixture) {
		effects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandMutation)
		testutil.FailErr(t, "restricted effects", err)
		f.Runtime.Boundary.SetProfileSource(func(context.Context, string) []sandbox.ToolProfile {
			return []sandbox.ToolProfile{{ID: "fixture", CommandEffects: effects}}
		})
	}},
}

// A program that starts no process saves nothing, whatever stopped it.
func TestProgramWithoutALaunchSavesNoDraft(t *testing.T) {
	for _, c := range refusedLaunches {
		t.Run(c.name, func(t *testing.T) {
			f, service, p := documentProgram(t)
			documents := map[string]*editordoc.Document{}
			for _, placed := range launchPlacements {
				writeTreeFile(t, f, placed.path, "saved\n")
				documents[placed.path] = typeHumanDraftEOL(t, service, p, placed.path, "typed\n", "lf")
			}
			if c.arrange != nil {
				c.arrange(t, f)
			}
			result := callDocumentProgram(t, f, c.source)
			if result.StopCode != c.stopCode || result.Status != c.status && c.stopCode == "" {
				t.Fatalf("result=%+v", result)
			}
			if !reflect.DeepEqual(result.LaunchDrafts, toolcontract.LaunchDrafts{}) {
				t.Fatalf("a program that launched nothing reports unsaved drafts: %+v", result.LaunchDrafts)
			}
			for path, doc := range documents {
				live, err := service.CurrentSnapshot(t.Context(), p.ID, doc.ID)
				testutil.FailErr(t, "document after program", err)
				if !live.Dirty || live.Draft != "typed\n" || readTreeFile(t, f, path) != "saved\n" {
					t.Fatalf("%s was saved without a launch: dirty=%v disk=%q", path, live.Dirty, readTreeFile(t, f, path))
				}
			}
		})
	}
}

// unsavableDrafts are the drafts a launch must leave alone. arrange returns
// what the process reads for the file.
var unsavableDrafts = []struct {
	dir, cause string
	arrange    func(t *testing.T, f *programtest.Fixture, service *editordoc.Service, p *project.Project, path string) string
}{
	{dir: "outside", cause: toolcontract.UnsavedChangedOutside, arrange: func(t *testing.T, f *programtest.Fixture, service *editordoc.Service, p *project.Project, path string) string {
		typeHumanDraft(t, service, p, path, "typed before an outside write\n")
		writeTreeFile(t, f, path, "written outside\n")
		return "written outside\n"
	}},
	{dir: "held", cause: toolcontract.UnsavedNotWritten, arrange: func(t *testing.T, f *programtest.Fixture, service *editordoc.Service, p *project.Project, path string) string {
		typeHumanDraft(t, service, p, path, "typed in a held directory\n")
		dir := filepath.Join(f.Dir, filepath.Dir(path))
		testutil.FailErr(t, "hold directory", os.Chmod(dir, 0o555))
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		return "saved\n"
	}},
	{dir: "review", cause: toolcontract.UnsavedAgentEditHeld, arrange: func(t *testing.T, f *programtest.Fixture, service *editordoc.Service, p *project.Project, path string) string {
		doc, err := service.Open(t.Context(), p, path, "r1", "", "window", nil)
		testutil.FailErr(t, "open "+path, err)
		dir := filepath.Join(f.Dir, filepath.Dir(path))
		testutil.FailErr(t, "hold publication", os.Chmod(dir, 0o555))
		// Another chat's edit is not this program's to publish.
		applied, err := service.ApplyAgentEdit(t.Context(), editordoc.AgentEdit{ProjectID: p.ID, DocumentID: doc.ID, ExpectedRevision: doc.Revision, Content: "another chat's edit\n", OperationID: uuid.NewString(), SessionID: "another-chat", ToolCallID: "other-edit", ToolName: "edit"})
		testutil.FailErr(t, "accept held edit", err)
		testutil.FailErr(t, "release directory", os.Chmod(dir, 0o755))
		if applied.Saved || applied.HeldVersionID == "" {
			t.Fatalf("edit was not held: %+v", applied)
		}
		return "saved\n"
	}},
}

// A launch leaves a draft it cannot save untouched, runs anyway, lets the
// process read that file's disk bytes, saves every draft it can, and names
// each unsaved draft on the result with its cause.
func TestProgramLaunchNamesEveryDraftItCannotSave(t *testing.T) {
	f, service, p := documentProgram(t)
	writeTreeFile(t, f, "ok/f.txt", "saved\n")
	saveable := typeHumanDraft(t, service, p, "ok/f.txt", "typed and saveable\n")
	reads := map[string]string{"ok/f.txt": "typed and saveable\n"}
	var want []toolcontract.UnsavedDocument
	source := diskReader + " ok/f.txt"
	for _, c := range unsavableDrafts {
		path := c.dir + "/f.txt"
		writeTreeFile(t, f, path, "saved\n")
		reads[path] = c.arrange(t, f, service, p, path)
		want = append(want, toolcontract.UnsavedDocument{Path: path, Cause: c.cause})
		source += "; printf '|'; " + diskReader + " " + path
	}
	result := callDocumentProgram(t, f, source)
	if result.Status != 0 || result.StopCode != "" {
		t.Fatalf("a draft that cannot be saved stopped the launch: %+v", result)
	}
	parts := strings.Split(result.Stdout, "|")
	if len(parts) != len(unsavableDrafts)+1 || parts[0] != reads["ok/f.txt"] {
		t.Fatalf("stdout=%q", result.Stdout)
	}
	for i, c := range unsavableDrafts {
		if path := c.dir + "/f.txt"; parts[i+1] != reads[path] || readTreeFile(t, f, path) != reads[path] {
			t.Errorf("%s: the process read %q and disk holds %q, want the earlier bytes %q", c.cause, parts[i+1], readTreeFile(t, f, path), reads[path])
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Path < want[j].Path })
	if !reflect.DeepEqual(result.UnsavedDocuments, want) || result.OmittedUnsavedDocuments != 0 {
		t.Fatalf("unsaved = %+v omitted=%d, want %+v", result.UnsavedDocuments, result.OmittedUnsavedDocuments, want)
	}
	if live, err := service.CurrentSnapshot(t.Context(), p.ID, saveable.ID); err != nil || live.Dirty {
		t.Fatalf("the saveable draft: %+v, %v", live, err)
	}
}

// A result names at most the bound of unsaved drafts and counts the rest.
func TestProgramLaunchBoundsTheUnsavedDraftsItNames(t *testing.T) {
	f, service, p := documentProgram(t)
	total := toolcontract.MaxUnsavedDocuments + 5
	for i := range total {
		path := fmt.Sprintf("many/f%02d.txt", i)
		writeTreeFile(t, f, path, "saved\n")
		typeHumanDraft(t, service, p, path, "typed\n")
		writeTreeFile(t, f, path, "written outside\n")
	}
	result := callDocumentProgram(t, f, "python3 -c 'pass'")
	if result.Status != 0 || len(result.UnsavedDocuments) != toolcontract.MaxUnsavedDocuments || result.OmittedUnsavedDocuments != 5 {
		t.Fatalf("named %d, omitted %d of %d: %+v", len(result.UnsavedDocuments), result.OmittedUnsavedDocuments, total, result)
	}
	for i, document := range result.UnsavedDocuments {
		if want := fmt.Sprintf("many/f%02d.txt", i); document.Path != want || document.Cause != toolcontract.UnsavedChangedOutside {
			t.Fatalf("unsaved[%d] = %+v, want %s", i, document, want)
		}
	}
}
