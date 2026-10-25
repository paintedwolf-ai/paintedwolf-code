package projectpaths_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// twoRoots is a project with an api primary root, a web root, session
// scratch and the attachment history its context was captured from.
type twoRoots struct {
	tc               tools.ToolContext
	api, web         projectroot.RootRef
	scratch, outside string
	history          *projectroot.History
}

func realDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve fixture directory", err)
	return dir
}

func newTwoRoots(t *testing.T) *twoRoots {
	t.Helper()
	f := &twoRoots{history: &projectroot.History{}, scratch: realDir(t), outside: realDir(t)}
	f.api = projectroot.RootRef{ID: "api-id", Label: "api", Path: realDir(t), IsPrimary: true}
	f.web = projectroot.RootRef{ID: "web-id", Label: "web", Path: realDir(t)}
	for path, content := range map[string]string{
		filepath.Join(f.api.Path, "go.mod"):              "module api\n",
		filepath.Join(f.api.Path, "cmd", "main.go"):      "package main\n",
		filepath.Join(f.web.Path, "index.html"):          "<p>web</p>\n",
		filepath.Join(f.web.Path, "src", "app.js"):       "app\n",
		filepath.Join(f.outside, "secret.txt"):           "outside\n",
		filepath.Join(f.scratch, "notes", "scratch.txt"): "scratch\n",
	} {
		testutil.FailErr(t, "mkdir fixture", os.MkdirAll(filepath.Dir(path), 0o755))
		testutil.FailErr(t, "write fixture", os.WriteFile(path, []byte(content), 0o600))
	}
	f.tc = tools.ToolContext{
		Source:   tools.InvocationSource{Roots: []projectroot.RootRef{f.api, f.web}, ActiveRootID: f.api.ID, RootHistory: f.history.View("project")},
		Host:     tools.InvocationHost{SessionScratchDir: f.scratch},
		Identity: tools.InvocationIdentity{Agent: toolprofiles.DefaultToolProfileID, ProjectID: "project"},
	}
	return f
}

func (f *twoRoots) paths(t *testing.T) *projectpaths.ProgramPaths {
	t.Helper()
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, f.tc, "")
	testutil.FailErr(t, "bind program paths", err)
	return p
}

// run invokes one path utility and returns its stdout, stderr and result.
func run(t *testing.T, p *projectpaths.ProgramPaths, name string, args ...string) (string, string, toolcontract.ProducerResult, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	result, err := p.Invoke(t.Context(), commandprogram.OperationRequest{OperationID: name, Argv: pathArgv(name, args...)}, commandprogram.Streams{Output: &stdout, Error: &stderr})
	return stdout.String(), stderr.String(), result, err
}

func TestProgramDirectoryIsAbsoluteAcrossAttachedRoots(t *testing.T) {
	f := newTwoRoots(t)
	p := f.paths(t)
	steps := []struct {
		name       string
		args       []string
		wantDir    string
		wantStdout string
		wantOld    string
	}{
		{"pwd", nil, f.api.Path, f.api.Path + "\n", ""},
		{"cd", []string{f.web.Path}, f.web.Path, "", f.api.Path},
		{"cd", []string{"src"}, filepath.Join(f.web.Path, "src"), "", f.web.Path},
		{"cd", []string{filepath.Join(f.api.Path, "cmd")}, filepath.Join(f.api.Path, "cmd"), "", filepath.Join(f.web.Path, "src")},
		{"cd", []string{"-"}, filepath.Join(f.web.Path, "src"), filepath.Join(f.web.Path, "src") + "\n", filepath.Join(f.api.Path, "cmd")},
		{"cd", []string{".."}, f.web.Path, "", filepath.Join(f.web.Path, "src")},
		{"cd", []string{filepath.Join(f.scratch, "notes")}, filepath.Join(f.scratch, "notes"), "", f.web.Path},
		{"pwd", []string{"-P"}, filepath.Join(f.scratch, "notes"), filepath.Join(f.scratch, "notes") + "\n", f.web.Path},
		{"cd", nil, f.api.Path, "", filepath.Join(f.scratch, "notes")},
	}
	for i, step := range steps {
		stdout, stderr, result, err := run(t, p, step.name, step.args...)
		testutil.FailErr(t, step.name+" "+strings.Join(step.args, " "), err)
		old, _ := p.PreviousDirectory()
		if result.Status != 0 || stdout != step.wantStdout || stderr != "" || p.Directory() != step.wantDir || old != step.wantOld {
			t.Fatalf("step %d %s %v: status=%d stdout=%q stderr=%q dir=%q old=%q", i, step.name, step.args, result.Status, stdout, stderr, p.Directory(), old)
		}
	}
	// Relative operands resolve from the statement's directory in each root.
	testutil.FailErr(t, "enter web", p.Chdir(t.Context(), f.web.Path, false))
	bound, err := p.Bind(t.Context(), "src/app.js", false)
	testutil.FailErr(t, "bind web-relative operand", err)
	if bound.Resolved.Abs != filepath.Join(f.web.Path, "src", "app.js") || bound.Resolved.Root.ID != f.web.ID {
		t.Fatalf("relative operand = %+v", bound.Resolved)
	}
	// A fork keeps its own directory.
	fork := p.Fork()
	testutil.FailErr(t, "fork moves", fork.Chdir(t.Context(), f.api.Path, false))
	if p.Directory() != f.web.Path || fork.Directory() != f.api.Path {
		t.Fatalf("fork leaked: parent=%q fork=%q", p.Directory(), fork.Directory())
	}
}

func TestCdFailuresAreUtilityErrorsInShellVoice(t *testing.T) {
	f := newTwoRoots(t)
	p := f.paths(t)
	cases := []struct {
		args   []string
		stderr string
	}{
		{[]string{"-"}, "cd: OLDPWD not set\n"},
		{[]string{"missing"}, "cd: missing: No such file or directory\n"},
		{[]string{"go.mod"}, "cd: go.mod: Not a directory\n"},
	}
	for _, c := range cases {
		stdout, stderr, result, err := run(t, p, "cd", c.args...)
		if err != nil || result.Status != 1 || result.Terminal != toolcontract.ProducerCompleted || stdout != "" || stderr != c.stderr || p.Directory() != f.api.Path {
			t.Fatalf("cd %v: result=%+v stdout=%q stderr=%q err=%v dir=%q", c.args, result, stdout, stderr, err, p.Directory())
		}
	}
	// A path outside every folder and OS location is a host decision, not a
	// missing file.
	_, _, result, err := run(t, p, "cd", "/etc")
	if toolrejection.AsToolReject(err) == nil || result.Terminal != toolcontract.ProducerRefused || p.Directory() != f.api.Path {
		t.Fatalf("outside cd = %+v %v", result, err)
	}
}

func TestProgramAddressesNameRealFolders(t *testing.T) {
	f := newTwoRoots(t)
	p := f.paths(t)
	for label, want := range map[string]string{"api": f.api.Path, "WEB": f.web.Path, "scratch": f.scratch} {
		native, ok, err := p.Address(t.Context(), label)
		if err != nil || !ok || native != want {
			t.Fatalf("@%s = %q,%v,%v want %q", label, native, ok, err, want)
		}
	}
	// npm scopes and other @words stay literal for the program to read.
	if native, ok, err := p.Address(t.Context(), "types"); err != nil || ok || native != "" {
		t.Fatalf("unknown label converted: %q,%v,%v", native, ok, err)
	}
	// Expansion leaves unknown names literal; binding uses the current directory.
	bound, err := p.Bind(t.Context(), "@types/node", false)
	testutil.FailErr(t, "bind literal scoped name", err)
	if bound.Resolved.Abs != filepath.Join(f.api.Path, "@types", "node") {
		t.Fatalf("literal binding=%q", bound.Resolved.Abs)
	}
	scratchless := f.tc
	scratchless.Host.SessionScratchDir = ""
	q, err := projectpaths.NewProgramPaths(t.Context(), nil, scratchless, "")
	testutil.FailErr(t, "bind scratchless paths", err)
	if _, _, err := q.Address(t.Context(), "scratch"); err == nil {
		t.Fatal("@scratch converted without session scratch")
	}
}

func TestRelativeEscapeResolvesLikeItsAbsoluteSpelling(t *testing.T) {
	f := newTwoRoots(t)
	p := f.paths(t)
	// The parent of a temporary root lies in the OS temporary location; both
	// spellings must reach the same decision and the same file.
	parent := filepath.Dir(f.api.Path)
	sibling := filepath.Base(f.outside)
	relative, relErr := p.Bind(t.Context(), filepath.Join("..", sibling, "secret.txt"), false)
	absolute, absErr := p.Bind(t.Context(), filepath.Join(parent, sibling, "secret.txt"), false)
	if (relErr == nil) != (absErr == nil) {
		t.Fatalf("relative=%v absolute=%v", relErr, absErr)
	}
	if relErr == nil && filepath.Clean(relative.Resolved.Abs) != filepath.Clean(absolute.Resolved.Abs) {
		t.Fatalf("relative=%q absolute=%q", relative.Resolved.Abs, absolute.Resolved.Abs)
	}
	// A link inside a root that climbs out with .. does not reach the parent's
	// files with root authority.
	testutil.FailErr(t, "seed climbing link", os.Symlink(f.outside, filepath.Join(f.web.Path, "out")))
	bound, err := p.Bind(t.Context(), filepath.Join(f.web.Path, "out", "secret.txt"), false)
	if err == nil {
		if file, openErr := bound.Open(); openErr == nil {
			file.Close()
			t.Fatal("a link out of the web root read outside data with root authority")
		}
	}
	testutil.FailErr(t, "cd through link and back", p.Chdir(t.Context(), f.web.Path+"/out/..", false))
	if p.Directory() != f.web.Path {
		t.Fatalf("logical cd .. through a link left the root: %q", p.Directory())
	}
}

func TestStaleRootSpellingsRefuseWithCurrentFolders(t *testing.T) {
	f := newTwoRoots(t)
	p := f.paths(t)
	bound, err := p.Bind(t.Context(), filepath.Join(f.web.Path, "index.html"), false)
	testutil.FailErr(t, "bind web file before removal", err)
	settled, err := bound.Open()
	testutil.FailErr(t, "read before removal", err)
	settled.Close()
	// The web folder is detached mid-program.
	f.history.Record("project", []projectroot.RootRef{f.api, f.web}, []projectroot.RootRef{f.api})
	if file, err := bound.Open(); err == nil {
		file.Close()
		t.Fatal("an effect under a removed root kept attachment authority")
	} else if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != tools.RootNotAttachedCode || reject.Data["root_status"] != "removed" {
		t.Fatalf("mid-program removal = %v", err)
	}
	if reject := tools.ValidateRootsCurrent(f.tc); reject == nil || reject.Code != tools.RootNotAttachedCode {
		t.Fatalf("launch under removed root = %v", reject)
	}
	// The next invocation is built from the current roots.
	next := f.tc
	next.Source.Roots = []projectroot.RootRef{f.api}
	next.Source.RootHistory = f.history.View("project")
	q, err := projectpaths.NewProgramPaths(t.Context(), nil, next, "")
	testutil.FailErr(t, "bind next invocation", err)
	if q.Directory() != f.api.Path || tools.ValidateRootsCurrent(next) != nil {
		t.Fatalf("next program starts in %q", q.Directory())
	}
	for _, spelling := range []string{filepath.Join(f.web.Path, "index.html"), f.web.Path} {
		_, err := q.Bind(t.Context(), spelling, false)
		reject := toolrejection.AsToolReject(err)
		if reject == nil || reject.Code != tools.RootNotAttachedCode || reject.Data["root_label"] != "web" || reject.Data["former_path"] != f.web.Path {
			t.Fatalf("%s = %v", spelling, err)
		}
		if folders := reject.Data["attached_folders"].([]string); len(folders) != 2 || folders[0] != "api → "+f.api.Path {
			t.Fatalf("%s folders = %v", spelling, folders)
		}
	}
	if _, _, err := q.Address(t.Context(), "web"); toolrejection.AsToolReject(err) == nil {
		t.Fatal("a removed label converted or stayed literal")
	}
	stdout, stderr, result, err := run(t, q, "cd", f.web.Path)
	if toolrejection.AsToolReject(err) == nil || result.StopCode != tools.RootNotAttachedCode || stdout != "" || stderr != "" {
		t.Fatalf("cd into removed folder = %+v %v", result, err)
	}
}

func TestMovedAndRenamedRootsRefuseOldSpellings(t *testing.T) {
	f := newTwoRoots(t)
	movedPath := filepath.Join(filepath.Dir(f.web.Path), filepath.Base(f.web.Path)+"-moved")
	testutil.FailErr(t, "move web on disk", os.Rename(f.web.Path, movedPath))
	moved := f.web
	moved.Path, moved.Label = movedPath, "site"
	f.history.Record("project", []projectroot.RootRef{f.api, f.web}, []projectroot.RootRef{f.api, moved})
	next := f.tc
	next.Source.Roots = []projectroot.RootRef{f.api, moved}
	next.Source.RootHistory = f.history.View("project")
	p, err := projectpaths.NewProgramPaths(t.Context(), nil, next, "")
	testutil.FailErr(t, "bind after move", err)
	_, err = p.Bind(t.Context(), filepath.Join(f.web.Path, "index.html"), false)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != tools.RootNotAttachedCode || reject.Data["root_status"] != "moved" || reject.Data["current_path"] != movedPath {
		t.Fatalf("old path = %v", err)
	}
	_, _, err = p.Address(t.Context(), "web")
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != tools.RootNotAttachedCode || reject.Data["current_label"] != "site" {
		t.Fatalf("old label = %v", err)
	}
	bound, err := p.Bind(t.Context(), filepath.Join(moved.Path, "index.html"), false)
	testutil.FailErr(t, "bind new label", err)
	file, err := bound.Open()
	testutil.FailErr(t, "read through new label", err)
	file.Close()
}
