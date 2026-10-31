//go:build unix

package referenceuserland

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// serveCases runs cases through the driver against a scratch root, as an
// unprivileged driver outside a container does.
func serveCases(t *testing.T, root string, cases ...Case) []Result {
	t.Helper()
	body, err := json.Marshal(request{Shell: "/bin/sh", Cases: cases})
	testutil.FailErr(t, "encode request", err)
	var out bytes.Buffer
	testutil.FailErr(t, "serve", serve(root, bytes.NewReader(body), &out))
	decoder := json.NewDecoder(&out)
	results := make([]Result, len(cases))
	for i := range results {
		testutil.FailErr(t, "decode result", decoder.Decode(&results[i]))
	}
	if decoder.More() {
		t.Fatal("the driver answered more results than cases")
	}
	return results
}

func TestDriverRunsEveryProgramInTheFixedEnvironment(t *testing.T) {
	root := t.TempDir()
	result := serveCases(t, root, Case{
		Program: `printf '%s\n' "$HOME" "$LANG" "$LC_ALL" "$LOGNAME" "$PATH" "$PWD" "$SHELL" "$TMPDIR" "$TZ" "$USER" "$(pwd -P)"; umask; cat`,
	})[0]
	workspace, err := filepath.EvalSymlinks(filepath.Join(root, workspaceDir))
	testutil.FailErr(t, "resolve workspace", err)
	want := strings.Join([]string{
		filepath.Join(root, homeDir), "C.UTF-8", "C.UTF-8", "reference", "/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		filepath.Join(root, workspaceDir), "/bin/sh", filepath.Join(root, tmpDir), "UTC", "reference", workspace, "0022",
	}, "\n") + "\n"
	if result.Status != 0 || string(result.Stdout) != want || len(result.Stderr) != 0 {
		t.Fatalf("result = %d %q %q, want 0 %q with empty stderr and stdin", result.Status, result.Stdout, result.Stderr, want)
	}
	if got := Environment("/bin/bash"); !slices.Contains(got, "HOME=/home/reference") || !slices.Contains(got, "SHELL=/bin/bash") ||
		!slices.Contains(got, "PWD=/workspace") || len(got) != 10 {
		t.Fatalf("container environment = %q", got)
	}
}

func TestDriverKeepsStatusAndStreamsExact(t *testing.T) {
	results := serveCases(t, t.TempDir(),
		Case{Program: `printf 'out\000put'; printf 'err\n' >&2; exit 7`},
		Case{Program: `kill -TERM $$`},
	)
	if got := results[0]; got.Status != 7 || string(got.Stdout) != "out\x00put" || string(got.Stderr) != "err\n" {
		t.Fatalf("result = %d %q %q, want 7 \"out\\x00put\" \"err\\n\"", got.Status, got.Stdout, got.Stderr)
	}
	if got := results[1].Status; got != 143 {
		t.Fatalf("status after SIGTERM = %d, want 143", got)
	}
}

func TestDriverSeedsTheWorkspaceExactly(t *testing.T) {
	root := t.TempDir()
	serveCases(t, root, Case{
		Program: "true",
		Workspace: map[string]Entry{
			"notes.txt":         {Data: []byte("alpha\n")},
			"secret.txt":        {Mode: 0o600, Data: []byte("s\n")},
			"sub/deep/code.go":  {Data: []byte("package deep\n")},
			"locked":            {Mode: fs.ModeDir | 0o555},
			"locked/inside.txt": {Data: []byte("in\n")},
			"empty":             {Mode: fs.ModeDir},
			"link":              {Mode: fs.ModeSymlink, Link: "notes.txt"},
		},
	})
	workspace := filepath.Join(root, workspaceDir)
	for name, want := range map[string]fs.FileMode{
		".": fs.ModeDir | 0o755, "notes.txt": 0o644, "secret.txt": 0o600, "sub": fs.ModeDir | 0o755,
		"sub/deep": fs.ModeDir | 0o755, "sub/deep/code.go": 0o644, "locked": fs.ModeDir | 0o555,
		"locked/inside.txt": 0o644, "empty": fs.ModeDir | 0o755, "link": fs.ModeSymlink,
	} {
		info, err := os.Lstat(filepath.Join(workspace, name))
		testutil.FailErr(t, "stat "+name, err)
		got := info.Mode() &^ fs.ModePerm
		if want.Type() != fs.ModeSymlink {
			got = info.Mode()
		}
		if got != want || !info.ModTime().Equal(SeedTime) {
			t.Errorf("%s: mode %v at %v, want %v at %v", name, info.Mode(), info.ModTime(), want, SeedTime)
		}
	}
	data, err := os.ReadFile(filepath.Join(workspace, "sub/deep/code.go"))
	testutil.FailErr(t, "read seeded file", err)
	if string(data) != "package deep\n" {
		t.Fatalf("seeded content = %q", data)
	}
	testutil.FailErr(t, "unlock for cleanup", os.Chmod(filepath.Join(workspace, "locked"), 0o755))
}

func TestDriverReportsTheFinalStateOfTrackedPaths(t *testing.T) {
	result := serveCases(t, t.TempDir(), Case{
		Program: `printf 'new\n' > made.txt && rm gone.txt && chmod 600 kept.txt && mkdir dir && ln -s made.txt link`,
		Workspace: map[string]Entry{
			"gone.txt": {Data: []byte("bye\n")},
			"kept.txt": {Data: []byte("same\n")},
		},
		Tracked: []string{"made.txt", "gone.txt", "kept.txt", "dir", "link", "never.txt"},
	})[0]
	want := map[string]*Entry{
		"made.txt":  {Mode: 0o644, Data: []byte("new\n")},
		"gone.txt":  nil,
		"kept.txt":  {Mode: 0o600, Data: []byte("same\n")},
		"dir":       {Mode: fs.ModeDir | 0o755},
		"link":      {Mode: result.Files["link"].Mode, Link: "made.txt"},
		"never.txt": nil,
	}
	if result.Status != 0 || !reflect.DeepEqual(result.Files, want) || result.Files["link"].Mode.Type() != fs.ModeSymlink {
		t.Fatalf("result = %d %q, files %+v, want %+v", result.Status, result.Stderr, result.Files, want)
	}
}

func TestDriverResetsEverythingBetweenCases(t *testing.T) {
	results := serveCases(t, t.TempDir(),
		Case{Program: `printf x > left.txt; printf x > "$HOME/home.txt"; printf x > "$TMPDIR/tmp.txt"`,
			Workspace: map[string]Entry{"first.txt": {Data: []byte("1")}}},
		Case{Program: `ls -A . "$HOME" "$TMPDIR"`, Workspace: map[string]Entry{"second.txt": {Data: []byte("2")}},
			Tracked: []string{"left.txt", "first.txt"}},
	)
	got := results[1]
	if got.Status != 0 || strings.Contains(string(got.Stdout), "txt\n") && strings.Count(string(got.Stdout), ".txt") != 1 ||
		!strings.Contains(string(got.Stdout), "second.txt") || got.Files["left.txt"] != nil || got.Files["first.txt"] != nil {
		t.Fatalf("second case saw %q with files %+v; it must start from its own seed only", got.Stdout, got.Files)
	}
}

func TestDriverRefusesPathsOutsideTheWorkspace(t *testing.T) {
	for _, c := range []Case{
		{Program: "true", Workspace: map[string]Entry{"../escape": {}}},
		{Program: "true", Workspace: map[string]Entry{"/etc/passwd": {}}},
		{Program: "true", Tracked: []string{"a/../../b"}},
		{Program: "true", Workspace: map[string]Entry{"link": {Mode: fs.ModeSymlink}}},
		{Program: "true", Workspace: map[string]Entry{"file": {Link: "target"}}},
		{Program: "true", Workspace: map[string]Entry{"dir": {Mode: fs.ModeDir, Data: []byte("x")}}},
		{Program: "true", Workspace: map[string]Entry{"pipe": {Mode: fs.ModeNamedPipe}}},
	} {
		body, err := json.Marshal(request{Shell: "/bin/sh", Cases: []Case{c}})
		testutil.FailErr(t, "encode request", err)
		if err := serve(t.TempDir(), bytes.NewReader(body), &bytes.Buffer{}); err == nil {
			t.Errorf("driver accepted %+v", c)
		}
	}
}

func TestInventoryHashesTheBuiltTreeOnly(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{"usr/bin/tool": "#!/bin/sh\n", "usr/lib/data": "d", "etc/hostname": "a"} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755))
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(root, name), []byte(content), 0o644))
	}
	testutil.FailErr(t, "chmod tool", os.Chmod(filepath.Join(root, "usr/bin/tool"), 0o755))
	testutil.FailErr(t, "link bin", os.Symlink("usr/bin", filepath.Join(root, "bin")))
	testutil.FailErr(t, "link alias", os.Symlink("tool", filepath.Join(root, "usr/bin/alias")))
	take := func() Inventory {
		var out bytes.Buffer
		testutil.FailErr(t, "inventory", serve(root, strings.NewReader(`{"inventory":true}`), &out))
		var inv Inventory
		testutil.FailErr(t, "decode inventory", json.Unmarshal(out.Bytes(), &inv))
		return inv
	}
	base := take()
	if base.Entries != 9 || !reflect.DeepEqual(base.Commands, []string{"alias", "tool"}) || !treePattern.MatchString(base.Tree) {
		t.Fatalf("inventory = %+v, want 9 entries and commands alias, tool", base)
	}
	testutil.FailErr(t, "rewrite runtime file", os.WriteFile(filepath.Join(root, "etc/hostname"), []byte("b"), 0o644))
	if got := take().Tree; got != base.Tree {
		t.Fatalf("a runtime-owned file changed the tree: %s != %s", got, base.Tree)
	}
	for _, change := range []func() error{
		func() error { return os.WriteFile(filepath.Join(root, "usr/lib/data"), []byte("e"), 0o644) },
		func() error { return os.Chmod(filepath.Join(root, "usr/lib/data"), 0o600) },
		func() error { return os.Remove(filepath.Join(root, "usr/bin/alias")) },
	} {
		before := take().Tree
		testutil.FailErr(t, "change tree", change())
		if take().Tree == before {
			t.Fatal("a change to the built tree left its digest unchanged")
		}
	}
}
