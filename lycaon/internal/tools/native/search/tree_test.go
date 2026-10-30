package search

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/testutil"
)

// treeFiles is a small project with an ignored log, a hidden file, an empty
// directory and a link.
var treeFiles = map[string]string{
	"a/b/c/": "", "a/b/g.log": "y\n", "a/f.txt": "x\n", "d/": "", "top.go": "z\n", ".hidden": "h\n", ".gitignore": "*.log\n",
}

func treeHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, treeFiles)
	symlink(t, "top.go", filepath.Join(h.root, "lnk"))
	return h
}

func TestTreeFormsProduceExactBytes(t *testing.T) {
	h := treeHarness(t)
	v := "│   "
	for _, c := range []struct {
		argv   []string
		stdout string
		status int
	}{
		{argv: []string{"tree"}, stdout: ".\n├── a\n" + v + "├── b\n" + v + v + "├── c\n" + v + v + "└── g.log\n" + v + "└── f.txt\n├── d\n├── lnk -> top.go\n└── top.go\n\n5 directories, 4 files\n"},
		{argv: []string{"tree", "-L", "2", "--gitignore"}, stdout: ".\n├── a\n" + v + "├── b\n" + v + "└── f.txt\n├── d\n├── lnk -> top.go\n└── top.go\n\n4 directories, 3 files\n"},
		{argv: []string{"tree", "-a"}, stdout: ".\n├── .gitignore\n├── .hidden\n├── a\n" + v + "├── b\n" + v + v + "├── c\n" + v + v + "└── g.log\n" + v + "└── f.txt\n├── d\n├── lnk -> top.go\n└── top.go\n\n5 directories, 6 files\n"},
		{argv: []string{"tree", "-d"}, stdout: ".\n├── a\n" + v + "└── b\n" + v + "    └── c\n└── d\n\n5 directories\n"},
		{argv: []string{"tree", "-f"}, stdout: ".\n├── ./a\n" + v + "├── ./a/b\n" + v + v + "├── ./a/b/c\n" + v + v + "└── ./a/b/g.log\n" + v + "└── ./a/f.txt\n├── ./d\n├── ./lnk -> top.go\n└── ./top.go\n\n5 directories, 4 files\n"},
		{argv: []string{"tree", "-L", "1"}, stdout: ".\n├── a\n├── d\n├── lnk -> top.go\n└── top.go\n\n3 directories, 2 files\n"},
		{argv: []string{"tree", "-I", "a|d"}, stdout: ".\n├── lnk -> top.go\n└── top.go\n\n1 directory, 2 files\n"},
		{argv: []string{"tree", "-P", "*.go"}, stdout: ".\n├── a\n" + v + "└── b\n" + v + "    └── c\n├── d\n└── top.go\n\n5 directories, 1 file\n"},
		{argv: []string{"tree", "--noreport", "a"}, stdout: "a\n├── b\n" + v + "├── c\n" + v + "└── g.log\n└── f.txt\n"},
		{argv: []string{"tree", "-f", "a"}, stdout: "a\n├── a/b\n" + v + "├── a/b/c\n" + v + "└── a/b/g.log\n└── a/f.txt\n\n3 directories, 2 files\n"},
		{argv: []string{"tree", "a", "d"}, stdout: "a\n├── b\n" + v + "├── c\n" + v + "└── g.log\n└── f.txt\nd\n\n3 directories, 2 files\n"},
		{argv: []string{"tree", "missing"}, stdout: "missing  [error opening dir]\n\n0 directories, 0 files\n", status: 2},
		{argv: []string{"tree", "top.go"}, stdout: "top.go  [error opening dir]\n\n0 directories, 1 file\n"},
		{argv: []string{"tree", "-J", "-L", "2"}, stdout: "[\n    {\"type\":\"directory\",\"name\":\".\",\"contents\":[\n        {\"type\":\"directory\",\"name\":\"a\",\"contents\":[\n            {\"type\":\"directory\",\"name\":\"b\"},\n            {\"type\":\"file\",\"name\":\"f.txt\"}\n        ]},\n        {\"type\":\"directory\",\"name\":\"d\"},\n        {\"type\":\"link\",\"name\":\"lnk\",\"target\":\"top.go\"},\n        {\"type\":\"file\",\"name\":\"top.go\"}\n    ]}\n,    {\"type\":\"report\",\"directories\":4,\"files\":3}\n]\n"},
		{argv: []string{"tree", "-J", "--gitignore"}, stdout: "[\n    {\"type\":\"directory\",\"name\":\".\",\"contents\":[\n        {\"type\":\"directory\",\"name\":\"a\",\"contents\":[\n            {\"type\":\"directory\",\"name\":\"b\",\"contents\":[\n                {\"type\":\"directory\",\"name\":\"c\"}\n            ]},\n            {\"type\":\"file\",\"name\":\"f.txt\"}\n        ]},\n        {\"type\":\"directory\",\"name\":\"d\"},\n        {\"type\":\"link\",\"name\":\"lnk\",\"target\":\"top.go\"},\n        {\"type\":\"file\",\"name\":\"top.go\"}\n    ]}\n,    {\"type\":\"report\",\"directories\":5,\"files\":3}\n]\n"},
		{argv: []string{"tree", "-d", "-J"}, stdout: "[\n    {\"type\":\"directory\",\"name\":\".\",\"contents\":[\n        {\"type\":\"directory\",\"name\":\"a\",\"contents\":[\n            {\"type\":\"directory\",\"name\":\"b\",\"contents\":[\n                {\"type\":\"directory\",\"name\":\"c\"}\n            ]}\n        ]},\n        {\"type\":\"directory\",\"name\":\"d\"}\n    ]}\n,    {\"type\":\"report\",\"directories\":5}\n]\n"},
		{argv: []string{"tree", "-J", "missing"}, stdout: "[\n    {\"type\":\"unknown\",\"name\":\"missing\",\"contents\":[{\"error\": \"error opening dir\"}\n    ]}\n,    {\"type\":\"report\",\"directories\":0,\"files\":0}\n]\n", status: 2},
		{argv: []string{"tree", "--noreport", "-J", "d"}, stdout: "[\n    {\"type\":\"directory\",\"name\":\"d\"}\n\n]\n"},
		{argv: []string{"tree", "-J", "top.go"}, stdout: "[\n    {\"type\":\"file\",\"name\":\"top.go\",\"contents\":[{\"error\": \"error opening dir\"}\n    ]}\n,    {\"type\":\"report\",\"directories\":0,\"files\":1}\n]\n"},
	} {
		label := strings.Join(c.argv, " ")
		t.Run(label, func(t *testing.T) {
			h.exec("", c.argv...).expect(t, label, c.stdout, "", c.status)
		})
	}
}

// TestTreeGitignoreMatchesTheSourceCatalogView: tree --gitignore lists
// exactly the visible entries the project ignore owner admits, nested
// ignore files included.
func TestTreeGitignoreMatchesTheSourceCatalogView(t *testing.T) {
	h := newHarness(t, map[string]string{
		".gitignore": "*.log\nbuild/\n", "src/main.go": "package main\n", "src/debug.log": "x\n", "build/out.bin": "b",
		"docs/.gitignore": "draft.md\n", "docs/draft.md": "d\n", "docs/guide.md": "g\n", "README.md": "r\n",
	})
	r := h.exec("", "tree", "-f", "-L", "2", "--gitignore", "--noreport")
	var listed []string
	for _, line := range strings.Split(strings.TrimSuffix(r.stdout, "\n"), "\n")[1:] {
		listed = append(listed, line[strings.Index(line, "./")+2:])
	}
	scope := sourcescope.New(h.root, sourcescope.Options{Plane: sourcescope.Plane{IgnoreFiles: true}})
	var admitted []string
	testutil.FailErr(t, "walk fixture", filepath.WalkDir(h.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || path == h.root {
			return err
		}
		rel, _ := filepath.Rel(h.root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(entry.Name(), ".") || !scope.AdmitPath(rel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		admitted = append(admitted, rel)
		return nil
	}))
	slices.Sort(listed)
	slices.Sort(admitted)
	if !slices.Equal(listed, admitted) || slices.Contains(listed, "src/debug.log") || slices.Contains(listed, "build") || slices.Contains(listed, "docs/draft.md") {
		t.Fatalf("tree --gitignore listed %q, source catalog admits %q", listed, admitted)
	}
	if all := h.exec("", "tree", "-f", "--noreport"); !strings.Contains(all.stdout, "./src/debug.log") || !strings.Contains(all.stdout, "./build") {
		t.Fatalf("tree without --gitignore hid ignored paths: %q", all.stdout)
	}
}

func TestTreeRefusesInvalidLevelsAndOverBudgetWalks(t *testing.T) {
	h := treeHarness(t)
	for _, level := range []string{"0", "x", "-2"} {
		h.exec("", "tree", "-L", level).expect(t, "tree -L "+level, "", "tree: Invalid level, must be greater than 0.\n", 1)
	}
	h.exec("", "tree", "-I", "[z-a]").expect(t, "tree -I [z-a]", "", "tree: [z-a]: a wildcard pattern\n", 2)
	h.exec("", "tree", "-P", "ok|[[:nope:]]").expect(t, "tree -P class", "", "tree: ok|[[:nope:]]: a wildcard pattern\n", 2)
	h.owners.overrides = map[string]int64{"traversal_visits": 4}
	r := h.exec("", "tree")
	r.refused(t, "tree over budget", "COMMAND_LIMIT_EXCEEDED")
	if r.stdout != "" {
		t.Fatalf("capped tree printed %q", r.stdout)
	}
}

func TestTreeMarksUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind the superuser")
	}
	h := newHarness(t, map[string]string{"closed/inner": "x\n", "open": "y\n"})
	closed := filepath.Join(h.root, "closed")
	testutil.FailErr(t, "close dir", os.Chmod(closed, 0))
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	h.exec("", "tree").expect(t, "tree unreadable", ".\n├── closed  [error opening dir]\n└── open\n\n2 directories, 1 file\n", "", 0)
	h.exec("", "tree", "-J", "--noreport").expect(t, "tree -J unreadable", "[\n    {\"type\":\"directory\",\"name\":\".\",\"contents\":[\n        {\"type\":\"directory\",\"name\":\"closed\",\"contents\":[{\"error\": \"error opening dir\"}\n        ]},\n        {\"type\":\"file\",\"name\":\"open\"}\n    ]}\n\n]\n", "", 0)
}

// TestTreeDifferentialAgainstInstalledTree compares the forms above with an
// tree 2.3.2 reference in a UTF-8 locale, where one is present.
// Older 2.x releases differ in operand counts, byte ordering and JSON layout;
// the fixed behavior fixtures above run independently of an installed reference.
func TestTreeDifferentialAgainstInstalledTree(t *testing.T) {
	binary, err := exec.LookPath("tree")
	if err != nil {
		t.Log("no installed tree to compare")
		return
	}
	if version, _ := exec.Command(binary, "--version").Output(); !bytes.HasPrefix(version, []byte("tree v2.3.2 ")) {
		t.Logf("installed tree is %q, not the tree 2.3.2 reference", version)
		return
	}
	h := treeHarness(t)
	for _, argv := range [][]string{{}, {"-a"}, {"-d"}, {"-f"}, {"-L", "1"}, {"-L", "2", "--gitignore"}, {"-I", "a|d"}, {"-P", "*.go"}, {"-J", "-L", "2"}, {"-d", "-J"}, {"--noreport", "a"}, {"a", "d"}} {
		cmd := exec.Command(binary, argv...)
		cmd.Dir = h.root
		cmd.Env = []string{"LC_ALL=en_US.UTF-8", "PATH=" + os.Getenv("PATH")}
		want, err := cmd.Output()
		testutil.FailErr(t, "reference tree", err)
		got := h.exec("", append([]string{"tree"}, argv...)...)
		if got.stdout != string(want) {
			t.Errorf("tree %v\nnative    %q\nreference %q", argv, got.stdout, want)
		}
	}
}
