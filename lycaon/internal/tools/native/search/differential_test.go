package search

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

// reference is one installed implementation the native forms are compared
// with. Differential cases are bounded (fixed corpus, C locale, ten-second
// limit) and compare exact stdout and status; recursive listings compare
// sorted lines because the references visit directories in readdir order.
type reference struct {
	name string
	path string
	kind string // "bsd", "gnu" or "rg"
}

func lookReferencePath(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	if !strings.Contains(name, "/") {
		for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin"} {
			if path, err := exec.LookPath(dir + "/" + name); err == nil {
				return path, nil
			}
		}
	}
	return "", exec.ErrNotFound
}

func references(t *testing.T) []reference {
	t.Helper()
	var out []reference
	for _, candidate := range []reference{
		{"bsd grep", "/usr/bin/grep", "bsd"},
		{"gnu grep", "ggrep", "gnu"},
		{"gnu grep", "/usr/bin/grep", "gnu"},
		{"ripgrep", "rg", "rg"},
		{"bsd find", "/usr/bin/find", "bsd-find"},
		{"gnu find", "/usr/bin/find", "gnu-find"},
		{"gnu find", "gfind", "gnu-find"},
	} {
		path, err := lookReferencePath(candidate.path)
		if err != nil {
			continue
		}
		version, _ := exec.Command(path, "--version").CombinedOutput()
		gnu := bytes.Contains(version, []byte("GNU findutils"))
		switch candidate.kind {
		case "bsd":
			if !bytes.Contains(version, []byte("BSD grep")) {
				continue
			}
		case "gnu":
			if !bytes.Contains(version, []byte("GNU grep")) {
				continue
			}
		case "bsd-find":
			if gnu {
				continue
			}
		case "gnu-find":
			if !bytes.Contains(version, []byte("GNU findutils")) {
				continue
			}
		}
		candidate.path = path
		if !slices.ContainsFunc(out, func(r reference) bool { return r.path == path && r.kind == candidate.kind }) {
			out = append(out, candidate)
		}
	}
	return out
}

func runReference(t *testing.T, ref reference, dir string, argv []string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ref.path, argv...)
	cmd.Dir = dir
	cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return stdout.String(), 0
	case errors.As(err, &exit):
		return stdout.String(), exit.ExitCode()
	}
	t.Fatalf("%s %v: %v", ref.name, argv, err)
	return "", 0
}

type differentialCase struct {
	argv   []string
	kinds  []string // references that define this behavior identically
	sorted bool
	// json compares JSON Lines with the measured elapsed fields hidden.
	json bool
}

func sortedLines(s string) string {
	lines := strings.SplitAfter(s, "\n")
	slices.Sort(lines)
	return strings.Join(lines, "")
}

// differentialCorpus is ASCII so locale differences cannot enter.
var differentialCorpus = map[string]string{
	"input":         "alpha\nbeta\n",
	"a.txt":         "one\ntwo alpha\nthree\nfour\nfive\nsix alpha\n",
	"b.go":          "package b\n// alpha beta\n",
	"nope.txt":      "zzz\n",
	"dots":          "a.b\naxb\n",
	"pats":          "one\nsix\n",
	"words":         "foo bar\nfoobar foo\n",
	"repeat":        "abab cdcd abcd\n",
	"brace":         "a{1\n",
	"sub/c.txt":     "Alpha\nalphabet\n",
	"sub/.hidden":   "alpha hidden\n",
	"sub/deep/d.md": "alpha deep\n",
	"bin.dat":       "alpha\x00binary\n",
	"tree/a.go":     "package a\n",
	"tree/b.txt":    "x",
	"tree/s/c.go":   "package c\n",
	"tree/s/e.txt":  "",
	"tree/empty/":   "",
}

var grepDifferential = []differentialCase{
	{argv: []string{"grep", "-n", "alpha", "a.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-c", "alpha", "a.txt", "input", "nope.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-l", "alpha", "a.txt", "nope.txt", "b.go"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-L", "alpha", "a.txt", "nope.txt"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-v", "alpha", "input"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-i", "ALPHA", "sub/c.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-w", "-o", "foo", "words"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-x", "alpha", "input"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-o", `\(ab\)\1`, "repeat"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-o", `[a-d]\{4\}`, "repeat"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-E", "-o", "ab|abab", "repeat"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-E", "-o", "(cd)+", "repeat"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-o", `\<fo*\>`, "words"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-o", `foo\|bar`, "words"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-E", "-o", "a{1", "brace"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-P", "-o", `(?<=two )\w+`, "a.txt"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-P", "-o", "ab|abab", "repeat"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-n", "-C", "1", "alpha", "a.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-A", "1", "two", "a.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-B", "1", "six", "a.txt", "input"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-o", "-C", "1", "alpha", "a.txt"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-m", "1", "alpha", "a.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-m", "1", "-A", "1", "alpha", "a.txt"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-c", "-m", "1", "alpha", "a.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-e", "one", "-e", "six", "a.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-f", "pats", "a.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-F", "a.b", "dots"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-F", "-x", "a.b", "dots"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-h", "alpha", "a.txt", "input"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-H", "alpha", "input"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-r", "alpha", "sub"}, kinds: []string{"bsd", "gnu"}, sorted: true},
	{argv: []string{"grep", "-r", "--include=*.md", "alpha", "sub"}, kinds: []string{"bsd", "gnu"}, sorted: true},
	{argv: []string{"grep", "-r", "--exclude-dir=deep", "alpha", "sub"}, kinds: []string{"bsd", "gnu"}, sorted: true},
	{argv: []string{"grep", "-r", "alpha", "input"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "--include=*.go", "alpha", "a.txt", "b.go"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-q", "alpha", "a.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "zzz", "a.txt"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-s", "alpha", "missing"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "alpha", "bin.dat"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-c", "alpha", "bin.dat"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-l", "alpha", "bin.dat"}, kinds: []string{"bsd", "gnu"}},
	{argv: []string{"grep", "-b", "alpha", "a.txt"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "-ob", "alpha", "a.txt"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-ohn", "alpha", "a.txt", "input"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "-Z", "alpha", "a.txt", "input"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-lZ", "alpha", "a.txt", "nope.txt", "input"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-cZ", "alpha", "a.txt", "nope.txt"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-T", "-n", "alpha", "a.txt", "input"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-T", "-n", "-b", "alpha", "a.txt"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "--color=never", "alpha", "input"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "--line-buffered", "alpha", "input"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "-a", "alpha", "bin.dat"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "-I", "alpha", "bin.dat", "input"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "-c", "--binary-files=without-match", "alpha", "bin.dat"}, kinds: []string{"gnu"}},
	{argv: []string{"grep", "-G", "-o", `\(ab\)\1`, "repeat"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "--silent", "alpha", "a.txt"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "-1", "six", "a.txt"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "-d", "skip", "alpha", "sub", "input"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"grep", "-r", "--include-dir=sub", "--include-dir=*deep", "alpha", "sub"}, kinds: []string{"bsd"}, sorted: true},
	{argv: []string{"grep", "-u", "-U", "alpha", "input"}, kinds: []string{"bsd"}},
	{argv: []string{"grep", "-H", "--label=piped", "alpha", "-"}, kinds: []string{"gnu", "bsd"}},
	{argv: []string{"find", "tree/s", "-depth"}, kinds: []string{"bsd-find", "gnu-find"}},
	{argv: []string{"find", "tree", "-depth", "2"}, kinds: []string{"bsd-find"}, sorted: true},
	{argv: []string{"find", "-x", "tree/s"}, kinds: []string{"bsd-find"}, sorted: true},
	{argv: []string{"find", "tree", "-name", "a.go", "-or", "-name", "c.go"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-type", "f", "-size", "5w"}, kinds: []string{"gnu-find"}, sorted: true},
	{argv: []string{"rg", "-T", "md", "alpha", "sub"}, kinds: []string{"rg"}, sorted: true},
	{argv: []string{"rg", "--color=never", "alpha", "input"}, kinds: []string{"rg"}},
	{argv: []string{"find", "tree", "-iregex", ".*/A\\.GO"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-type", "f", "-perm", "-600"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"rg", "-n", "alpha", "input"}, kinds: []string{"rg"}},
	{argv: []string{"rg", "--json", "alpha", "a.txt"}, kinds: []string{"rg"}, json: true},
	{argv: []string{"rg", "--json", "-C", "1", "two", "a.txt"}, kinds: []string{"rg"}, json: true},
	{argv: []string{"rg", "--json", "-r", "X", "(al)pha", "a.txt"}, kinds: []string{"rg"}, json: true},
	{argv: []string{"rg", "--json", "-m", "1", "-A", "1", "alpha", "a.txt"}, kinds: []string{"rg"}, json: true},
	{argv: []string{"rg", "--json", "-v", "alpha", "a.txt"}, kinds: []string{"rg"}, json: true},
	{argv: []string{"rg", "--json", "-w", "alpha", "sub/c.txt"}, kinds: []string{"rg"}, json: true},
	{argv: []string{"rg", "--json", "alpha", "bin.dat"}, kinds: []string{"rg"}, json: true},
	{argv: []string{"rg", "--json", "zz", "a.txt"}, kinds: []string{"rg"}, json: true},
	{argv: []string{"rg", "alpha", "sub"}, kinds: []string{"rg"}, sorted: true},
	{argv: []string{"rg", "--hidden", "alpha", "sub"}, kinds: []string{"rg"}, sorted: true},
	{argv: []string{"rg", "-g", "*.md", "alpha", "sub"}, kinds: []string{"rg"}, sorted: true},
	{argv: []string{"rg", "-g", "!*.md", "alpha", "sub"}, kinds: []string{"rg"}, sorted: true},
	{argv: []string{"rg", "-t", "go", "alpha"}, kinds: []string{"rg"}, sorted: true},
	{argv: []string{"rg", "-c", "alpha", "a.txt", "nope.txt", "input"}, kinds: []string{"rg"}, sorted: true},
	{argv: []string{"rg", "-i", "ALPHA", "sub/c.txt"}, kinds: []string{"rg"}},
	{argv: []string{"rg", "-S", "Alpha", "sub/c.txt"}, kinds: []string{"rg"}},
	{argv: []string{"rg", "-w", "alpha", "sub/c.txt"}, kinds: []string{"rg"}},
	{argv: []string{"rg", "-o", "ab|abab", "repeat"}, kinds: []string{"rg"}},
	{argv: []string{"rg", "-r", "[$1]", "(al)pha", "input"}, kinds: []string{"rg"}},
	{argv: []string{"rg", "--files", "sub"}, kinds: []string{"rg"}, sorted: true},
	{argv: []string{"rg", "-F", "a.b", "dots"}, kinds: []string{"rg"}},
	{argv: []string{"rg", "-n", "-A", "1", "two", "a.txt"}, kinds: []string{"rg"}},
	{argv: []string{"rg", "-l", "binary"}, kinds: []string{"rg"}},
	{argv: []string{"find", "tree"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-name", "*.go"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-type", "d"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-maxdepth", "1", "-type", "f"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-mindepth", "2"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-empty"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-size", "+0c", "-type", "f"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "!", "-name", "*.go", "-type", "f"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-name", "a.go", "-o", "-name", "b.txt"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "(", "-name", "a.go", "-o", "-name", "c.go", ")", "-print"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-name", "s", "-prune", "-o", "-type", "f", "-print"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-path", "*s*", "-type", "f"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "tree", "-iname", "*.GO"}, kinds: []string{"bsd-find", "gnu-find"}, sorted: true},
	{argv: []string{"find", "nope"}, kinds: []string{"bsd-find", "gnu-find"}},
}

// TestDifferentialAgainstInstalledReferences compares each catalogued form
// with every installed reference that defines it identically. The native
// fixtures above stand on their own; these cases add reference evidence.
func TestDifferentialAgainstInstalledReferences(t *testing.T) {
	refs := references(t)
	if len(refs) == 0 {
		t.Skip("no reference grep, rg or find is installed on this host")
	}
	h := newHarness(t, differentialCorpus)
	compared := 0
	for _, c := range grepDifferential {
		for _, ref := range refs {
			if !slices.Contains(c.kinds, ref.kind) {
				continue
			}
			if ref.kind == "rg" != (c.argv[0] == "rg") || strings.HasSuffix(ref.kind, "-find") != (c.argv[0] == "find") {
				continue
			}
			t.Run(ref.name+" "+strings.Join(c.argv, " "), func(t *testing.T) {
				argv := c.argv[1:]
				if c.argv[0] == "find" && ref.kind == "bsd-find" {
					argv = append([]string{"-E"}, argv...)
				}
				want, status := runReference(t, ref, h.root, argv)
				got := h.exec("", c.argv...)
				if got.err != nil {
					t.Fatalf("native error %v", got.err)
				}
				gotOut := got.stdout
				if c.sorted {
					want, gotOut = sortedLines(want), sortedLines(gotOut)
				}
				if c.json {
					want, gotOut = normalizeElapsed(want), normalizeElapsed(gotOut)
				}
				if gotOut != want || got.result.Status != status {
					t.Fatalf("native stdout %q status %d\nreference stdout %q status %d", gotOut, got.result.Status, want, status)
				}
			})
			compared++
		}
	}
	if compared == 0 {
		t.Fatalf("installed references %v matched no differential case", refs)
	}
}
