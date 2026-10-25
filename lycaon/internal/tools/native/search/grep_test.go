package search

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// grepTree is the shared corpus of the grep and rg form fixtures.
var grepTree = map[string]string{
	"input":          "alpha\nbeta",
	"a.txt":          "one\ntwo alpha\nthree\nfour\nfive\nsix alpha\n",
	"b.go":           "package b\n// alpha beta\n",
	"nope.txt":       "zzz\n",
	"dots":           "a.b\naxb\n",
	"pats":           "one\nsix\n",
	"-dash":          "-x\n",
	"sub/c.txt":      "Alpha\nalphabet\n",
	"sub/.hidden":    "alpha hidden\n",
	"sub/deep/d.md":  "alpha deep\n",
	"bin.dat":        "alpha\x00binary\n",
	"bad.txt":        "ok\nalpha \xff\n",
	"words":          "foo bar\nfoobar foo\n",
	"repeat":         "abab cdcd abcd\n",
	"empty":          "",
	"nested/x/y.txt": "alpha nested\n",
	"alpha.pat":      "alpha\n",
}

type formCase struct {
	name   string
	argv   []string
	stdin  string
	stdout string
	stderr string
	status int
}

func runForms(t *testing.T, cases []formCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, grepTree)
			h.exec(c.stdin, c.argv...).expect(t, c.name, c.stdout, c.stderr, c.status)
		})
	}
}

func TestGrepFormsProduceExactBytes(t *testing.T) {
	runForms(t, []formCase{
		{name: "catalog example", argv: []string{"grep", "-n", "--", "alpha", "./input"}, stdout: "1:alpha\n"},
		{name: "default BRE", argv: []string{"grep", "alpha", "a.txt"}, stdout: "two alpha\nsix alpha\n"},
		{name: "BRE group and back reference", argv: []string{"grep", "-o", `\(ab\)\1`, "repeat"}, stdout: "abab\n"},
		{name: "BRE interval", argv: []string{"grep", "-o", `[a-d]\{4\}`, "repeat"}, stdout: "abab\ncdcd\nabcd\n"},
		{name: "ERE leftmost-longest", argv: []string{"grep", "-E", "-o", "ab|abab", "repeat"}, stdout: "abab\nab\n"},
		{name: "egrep alias", argv: []string{"egrep", "-o", "(cd)+", "repeat"}, stdout: "cdcd\ncd\n"},
		{name: "PCRE leftmost-first", argv: []string{"grep", "-P", "-o", "ab|abab", "repeat"}, stdout: "ab\nab\nab\n"},
		{name: "PCRE lookbehind", argv: []string{"grep", "-P", "-o", `(?<=two )\w+`, "a.txt"}, stdout: "alpha\n"},
		{name: "fixed strings", argv: []string{"grep", "-F", "a.b", "dots"}, stdout: "a.b\n"},
		{name: "fgrep alias whole line", argv: []string{"fgrep", "-x", "a.b", "dots"}, stdout: "a.b\n"},
		{name: "line regexp", argv: []string{"grep", "-x", "alpha", "input"}, stdout: "alpha\n"},
		{name: "ignore case", argv: []string{"grep", "-i", "ALPHA", "sub/c.txt"}, stdout: "Alpha\nalphabet\n"},
		{name: "word regexp", argv: []string{"grep", "-w", "-o", "foo", "words"}, stdout: "foo\nfoo\n"},
		{name: "invert", argv: []string{"grep", "-v", "alpha", "input"}, stdout: "beta\n"},
		{name: "count", argv: []string{"grep", "-c", "alpha", "a.txt", "input", "nope.txt"}, stdout: "a.txt:2\ninput:1\nnope.txt:0\n"},
		{name: "count with max", argv: []string{"grep", "-c", "-m", "1", "alpha", "a.txt"}, stdout: "1\n"},
		{name: "files with matches", argv: []string{"grep", "-l", "alpha", "a.txt", "nope.txt", "b.go"}, stdout: "a.txt\nb.go\n"},
		{name: "files without match", argv: []string{"grep", "-L", "alpha", "a.txt", "nope.txt"}, stdout: "nope.txt\n"},
		{name: "only matching with numbers", argv: []string{"grep", "-on", "alpha", "a.txt"}, stdout: "2:alpha\n6:alpha\n"},
		{name: "context groups", argv: []string{"grep", "-n", "-C", "1", "alpha", "a.txt"}, stdout: "1-one\n2:two alpha\n3-three\n--\n5-five\n6:six alpha\n"},
		{name: "after context", argv: []string{"grep", "-A", "1", "two", "a.txt"}, stdout: "two alpha\nthree\n"},
		{name: "before context with names", argv: []string{"grep", "-B", "1", "six", "a.txt", "input"}, stdout: "a.txt-five\na.txt:six alpha\n"},
		{name: "context default yields to explicit after", argv: []string{"grep", "-A", "0", "-C", "1", "six", "a.txt"}, stdout: "five\nsix alpha\n"},
		{name: "only matching keeps group separators", argv: []string{"grep", "-o", "-C", "1", "alpha", "a.txt"}, stdout: "alpha\n--\nalpha\n"},
		{name: "max count keeps trailing context", argv: []string{"grep", "-m", "1", "-A", "1", "alpha", "a.txt"}, stdout: "two alpha\nthree\n"},
		{name: "several patterns", argv: []string{"grep", "-e", "one", "-e", "six", "a.txt"}, stdout: "one\nsix alpha\n"},
		{name: "pattern file", argv: []string{"grep", "-f", "pats", "a.txt"}, stdout: "one\nsix alpha\n"},
		{name: "empty pattern file selects nothing", argv: []string{"grep", "-f", "empty", "a.txt"}, status: 1},
		{name: "leading dash pattern and operand", argv: []string{"grep", "-e", "-x", "--", "-dash"}, stdout: "-x\n"},
		{name: "no filename", argv: []string{"grep", "-h", "alpha", "a.txt", "input"}, stdout: "two alpha\nsix alpha\nalpha\n"},
		{name: "with filename", argv: []string{"grep", "-H", "alpha", "input"}, stdout: "input:alpha\n"},
		{name: "last of -H and -h wins", argv: []string{"grep", "-H", "-h", "alpha", "input"}, stdout: "alpha\n"},
		{name: "standard input", argv: []string{"grep", "b"}, stdin: "a\nb\n", stdout: "b\n"},
		{name: "standard input label", argv: []string{"grep", "-H", "b", "-"}, stdin: "a\nb\n", stdout: "(standard input):b\n"},
		{name: "recursive includes hidden in byte order", argv: []string{"grep", "-r", "alpha", "sub"}, stdout: "sub/.hidden:alpha hidden\nsub/c.txt:alphabet\nsub/deep/d.md:alpha deep\n"},
		{name: "recursive without operand omits dot", argv: []string{"grep", "-r", "nested"}, stdout: "nested/x/y.txt:alpha nested\n"},
		{name: "recursive single file has no name", argv: []string{"grep", "-r", "alpha", "input"}, stdout: "alpha\n"},
		{name: "recursive trailing slash", argv: []string{"grep", "-r", "nested", "nested/"}, stdout: "nested/x/y.txt:alpha nested\n"},
		{name: "include", argv: []string{"grep", "-r", "--include=*.md", "alpha", "sub"}, stdout: "sub/deep/d.md:alpha deep\n"},
		{name: "exclude", argv: []string{"grep", "-r", "--exclude", "*.md", "--exclude", ".*", "alpha", "sub"}, stdout: "sub/c.txt:alphabet\n"},
		{name: "exclude dir", argv: []string{"grep", "-r", "--exclude-dir=deep", "alpha", "sub"}, stdout: "sub/.hidden:alpha hidden\nsub/c.txt:alphabet\n"},
		{name: "include applies to command-line files", argv: []string{"grep", "--include=*.go", "alpha", "a.txt", "b.go"}, stdout: "b.go:// alpha beta\n"},
		{name: "binary file notice", argv: []string{"grep", "alpha", "bin.dat", "input"}, stdout: "input:alpha\n", stderr: "grep: bin.dat: binary file matches\n"},
		{name: "binary file counts", argv: []string{"grep", "-c", "alpha", "bin.dat"}, stdout: "1\n"},
		{name: "binary file listed", argv: []string{"grep", "-l", "alpha", "bin.dat"}, stdout: "bin.dat\n"},
		{name: "invalid UTF-8 line is binary", argv: []string{"grep", "alpha", "bad.txt"}, stderr: "grep: bad.txt: binary file matches\n"},
		{name: "no match is status one", argv: []string{"grep", "zzz", "a.txt"}, status: 1},
		{name: "missing file", argv: []string{"grep", "alpha", "missing", "input"}, stdout: "input:alpha\n", stderr: "grep: missing: No such file or directory\n", status: 2},
		{name: "missing file suppressed", argv: []string{"grep", "-s", "alpha", "missing"}, status: 2},
		{name: "directory without recursion", argv: []string{"grep", "alpha", "sub"}, stderr: "grep: sub: Is a directory\n", status: 2},
		{name: "quiet match outranks error", argv: []string{"grep", "-q", "alpha", "missing", "a.txt"}, stderr: "grep: missing: No such file or directory\n"},
		{name: "quiet stops at first match", argv: []string{"grep", "-q", "alpha", "a.txt", "missing"}},
		{name: "quiet no match with error", argv: []string{"grep", "-q", "zzz", "missing", "a.txt"}, stderr: "grep: missing: No such file or directory\n", status: 2},
		{name: "quiet no match", argv: []string{"grep", "-q", "zzz", "a.txt"}, status: 1},
		{name: "max count zero", argv: []string{"grep", "-m", "0", "alpha", "a.txt"}, status: 1},
		{name: "byte offsets", argv: []string{"grep", "-b", "alpha", "a.txt"}, stdout: "4:two alpha\n30:six alpha\n"},
		{name: "byte offsets of matches", argv: []string{"grep", "-ob", "alpha", "a.txt"}, stdout: "8:alpha\n34:alpha\n"},
		{name: "clustered -ohn", argv: []string{"grep", "-ohn", "alpha", "a.txt", "input"}, stdout: "2:alpha\n6:alpha\n1:alpha\n"},
		{name: "null after names", argv: []string{"grep", "-Z", "alpha", "a.txt", "input"}, stdout: "a.txt\x00two alpha\na.txt\x00six alpha\ninput\x00alpha\n"},
		{name: "null after listed names", argv: []string{"grep", "-lZ", "alpha", "a.txt", "nope.txt", "input"}, stdout: "a.txt\x00input\x00"},
		{name: "null after counted names", argv: []string{"grep", "-cZ", "alpha", "a.txt", "nope.txt"}, stdout: "a.txt\x002\nnope.txt\x000\n"},
		{name: "null-separated records", argv: []string{"grep", "-z", "alpha"}, stdin: "a\x00b alpha\x00c", stdout: "b alpha\x00"},
		{name: "initial tab", argv: []string{"grep", "-T", "-n", "alpha", "a.txt", "input"}, stdout: "a.txt: 2:\ttwo alpha\na.txt: 6:\tsix alpha\ninput: 1:\talpha\n"},
		{name: "initial tab without prefix", argv: []string{"grep", "-T", "alpha", "input"}, stdout: "alpha\n"},
		{name: "color never", argv: []string{"grep", "--color=never", "alpha", "input"}, stdout: "alpha\n"},
		{name: "colour auto", argv: []string{"grep", "--colour=auto", "alpha", "input"}, stdout: "alpha\n"},
		{name: "line buffered", argv: []string{"grep", "--line-buffered", "alpha", "input"}, stdout: "alpha\n"},
		{name: "text mode prints binary lines", argv: []string{"grep", "-a", "alpha", "bin.dat"}, stdout: "alpha\x00binary\n"},
		{name: "binary files text", argv: []string{"grep", "--binary-files=text", "alpha", "bin.dat"}, stdout: "alpha\x00binary\n"},
		{name: "ignore binary files", argv: []string{"grep", "-I", "alpha", "bin.dat", "input"}, stdout: "input:alpha\n"},
		{name: "binary without match counts zero", argv: []string{"grep", "-c", "--binary-files=without-match", "alpha", "bin.dat"}, stdout: "0\n", status: 1},
		{name: "explicit basic regexp", argv: []string{"grep", "-G", "-o", `\(ab\)\1`, "repeat"}, stdout: "abab\n"},
		{name: "obsolete -y", argv: []string{"grep", "-y", "ALPHA", "sub/c.txt"}, stdout: "Alpha\nalphabet\n"},
		{name: "silent", argv: []string{"grep", "--silent", "alpha", "a.txt"}},
		{name: "numeric context shorthand", argv: []string{"grep", "-1", "six", "a.txt"}, stdout: "five\nsix alpha\n"},
		{name: "skip directories", argv: []string{"grep", "-d", "skip", "alpha", "sub", "input"}, stdout: "input:alpha\n"},
		{name: "recurse directories", argv: []string{"grep", "--directories=recurse", "deep", "sub"}, stdout: "sub/deep/d.md:alpha deep\n"},
		{name: "devices", argv: []string{"grep", "-D", "skip", "alpha", "input"}, stdout: "alpha\n"},
		{name: "include dir", argv: []string{"grep", "-r", "--include-dir=deep", "alpha", "sub"}, stdout: "sub/.hidden:alpha hidden\nsub/c.txt:alphabet\nsub/deep/d.md:alpha deep\n"},
		{name: "include dir prunes", argv: []string{"grep", "-r", "--include-dir=none", "deep", "sub"}, status: 1},
		{name: "no-op compatibility flags", argv: []string{"grep", "-u", "-U", "--mmap", "alpha", "input"}, stdout: "alpha\n"},
		{name: "stdin label", argv: []string{"grep", "-H", "--label=piped", "b"}, stdin: "a\nb\n", stdout: "piped:b\n"},
	})
}

func TestRgFormsProduceExactBytes(t *testing.T) {
	runForms(t, []formCase{
		{name: "catalog example", argv: []string{"rg", "-n", "--", "alpha", "./input"}, stdout: "1:alpha\n"},
		{name: "default RE2 leftmost-first", argv: []string{"rg", "-o", "ab|abab", "repeat"}, stdout: "ab\nab\nab\n"},
		{name: "recursive skips hidden", argv: []string{"rg", "alpha", "sub"}, stdout: "sub/c.txt:alphabet\nsub/deep/d.md:alpha deep\n"},
		{name: "hidden", argv: []string{"rg", "--hidden", "alpha", "sub"}, stdout: "sub/.hidden:alpha hidden\nsub/c.txt:alphabet\nsub/deep/d.md:alpha deep\n"},
		{name: "unrestricted twice is hidden", argv: []string{"rg", "-uu", "alpha", "sub"}, stdout: "sub/.hidden:alpha hidden\nsub/c.txt:alphabet\nsub/deep/d.md:alpha deep\n"},
		{name: "glob include", argv: []string{"rg", "-g", "*.md", "alpha", "sub"}, stdout: "sub/deep/d.md:alpha deep\n"},
		{name: "glob exclude", argv: []string{"rg", "-g", "!*.md", "alpha", "sub"}, stdout: "sub/c.txt:alphabet\n"},
		{name: "anchored glob", argv: []string{"rg", "-g", "sub/deep/*", "alpha"}, stdout: "sub/deep/d.md:alpha deep\n"},
		{name: "file type", argv: []string{"rg", "-t", "go", "alpha"}, stdout: "b.go:// alpha beta\n"},
		{name: "count omits files without matches", argv: []string{"rg", "-c", "alpha", "a.txt", "nope.txt", "input"}, stdout: "a.txt:2\ninput:1\n"},
		{name: "single file has no name", argv: []string{"rg", "alpha", "input"}, stdout: "alpha\n"},
		{name: "with filename", argv: []string{"rg", "-H", "alpha", "input"}, stdout: "input:alpha\n"},
		{name: "no filename", argv: []string{"rg", "--no-filename", "deep", "sub"}, stdout: "alpha deep\n"},
		{name: "ignore case", argv: []string{"rg", "-i", "ALPHA", "sub/c.txt"}, stdout: "Alpha\nalphabet\n"},
		{name: "smart case lowercase", argv: []string{"rg", "-S", "alpha", "sub/c.txt"}, stdout: "Alpha\nalphabet\n"},
		{name: "smart case uppercase", argv: []string{"rg", "-S", "Alpha", "sub/c.txt"}, stdout: "Alpha\n"},
		{name: "case sensitive overrides", argv: []string{"rg", "-i", "-s", "ALPHA", "sub/c.txt"}, status: 1},
		{name: "word", argv: []string{"rg", "-w", "alpha", "sub/c.txt"}, status: 1},
		{name: "replace line", argv: []string{"rg", "-r", "[$1]", "(al)pha", "input"}, stdout: "[al]\n"},
		{name: "replace only matching", argv: []string{"rg", "-o", "-r", "${1}!", "(t)wo", "a.txt"}, stdout: "t!\n"},
		{name: "fixed strings", argv: []string{"rg", "-F", "a.b", "dots"}, stdout: "a.b\n"},
		{name: "extended", argv: []string{"rg", "-E", "-o", "ab|abab", "repeat"}, stdout: "abab\nab\n"},
		{name: "perl", argv: []string{"rg", "-P", "-o", `(?<=two )\w+`, "a.txt"}, stdout: "alpha\n"},
		{name: "files", argv: []string{"rg", "--files", "sub"}, stdout: "sub/c.txt\nsub/deep/d.md\n"},
		{name: "files without match", argv: []string{"rg", "--files-without-match", "alpha", "a.txt", "nope.txt"}, stdout: "nope.txt\n"},
		{name: "binary skipped in recursion", argv: []string{"rg", "-l", "binary"}, status: 1},
		{name: "binary searched when named", argv: []string{"rg", "alpha", "bin.dat"}, stdout: "binary file matches (found \"\\0\" byte around offset 5)\n"},
		{name: "unrestricted thrice searches binary", argv: []string{"rg", "-uuu", "-l", "binary"}, stdout: "bin.dat\n"},
		{name: "follow is accepted", argv: []string{"rg", "-L", "deep", "sub"}, stdout: "sub/deep/d.md:alpha deep\n"},
		{name: "no messages", argv: []string{"rg", "--no-messages", "alpha", "missing"}, status: 2},
		{name: "context", argv: []string{"rg", "-n", "-A", "1", "two", "a.txt"}, stdout: "2:two alpha\n3-three\n"},
		{name: "type not", argv: []string{"rg", "-T", "md", "alpha", "sub"}, stdout: "sub/c.txt:alphabet\n"},
		{name: "color never", argv: []string{"rg", "--color=never", "alpha", "input"}, stdout: "alpha\n"},
	})
}

func TestRgHelpListsCatalogForms(t *testing.T) {
	h := newHarness(t, nil)
	r := h.exec("", "rg", "-h")
	if r.err != nil || r.result.Status != 0 || len(r.stdout) == 0 || r.stdout[:10] != "Usage: rg " {
		t.Fatalf("help: %q %+v %v", r.stdout, r.result, r.err)
	}
}

func TestRgAppliesProjectIgnoreRules(t *testing.T) {
	h := newHarness(t, map[string]string{".gitignore": "skip.txt\n", "skip.txt": "alpha\n", "keep.txt": "alpha\n", ".git/HEAD": "ref\n"})
	h.exec("", "rg", "alpha").expect(t, "ignored", "keep.txt:alpha\n", "", 0)
	h.exec("", "rg", "-u", "alpha").expect(t, "unrestricted", "keep.txt:alpha\nskip.txt:alpha\n", "", 0)
	h.exec("", "rg", "--no-ignore", "alpha").expect(t, "no ignore", "keep.txt:alpha\nskip.txt:alpha\n", "", 0)
	h.exec("", "rg", "alpha", "skip.txt").expect(t, "named path", "alpha\n", "", 0)
	h.exec("", "grep", "-r", "alpha", ".").expect(t, "grep ignores no file", "./keep.txt:alpha\n./skip.txt:alpha\n", "", 0)
}

func TestGrepRecursionLinks(t *testing.T) {
	h := newHarness(t, map[string]string{"d/f": "alpha\n", "target/t": "alpha\n"})
	symlink(t, "f", filepath.Join(h.root, "d", "lf"))
	symlink(t, "../target", filepath.Join(h.root, "d", "ld"))
	symlink(t, "..", filepath.Join(h.root, "d", "loop"))
	h.exec("", "grep", "-r", "alpha", "d").expect(t, "-r skips links", "d/f:alpha\n", "", 0)
	r := h.exec("", "grep", "-R", "alpha", "d")
	r.expect(t, "-R follows links", "d/f:alpha\nd/ld/t:alpha\nd/lf:alpha\nd/loop/target/t:alpha\n", "grep: d/loop/d: warning: recursive directory loop\n", 0)
	h.exec("", "grep", "alpha", "d/lf").expect(t, "named link", "alpha\n", "", 0)
	h.exec("", "grep", "-r", "-S", "alpha", "d/ld").expect(t, "BSD -S follows", "d/ld/t:alpha\n", "", 0)
	h.exec("", "grep", "-R", "-p", "alpha", "d").expect(t, "BSD -p follows none", "d/f:alpha\n", "", 0)
}

func TestSearchPinsEveryRead(t *testing.T) {
	h := newHarness(t, grepTree)
	r := h.exec("", "grep", "-r", "-f", "alpha.pat", "sub")
	r.expect(t, "pins", "sub/.hidden:alpha hidden\nsub/c.txt:alphabet\nsub/deep/d.md:alpha deep\n", "", 0)
	if got, want := h.pins.paths(r.execution), []string{"alpha.pat", "sub/.hidden", "sub/c.txt", "sub/deep/d.md"}; !slices.Equal(got, want) {
		t.Fatalf("pins %v, want %v", got, want)
	}
	r = h.exec("", "grep", "alpha", "bin.dat")
	if got := h.pins.paths(r.execution); !slices.Equal(got, []string{"bin.dat"}) {
		t.Fatalf("binary pins %v", got)
	}
}

// TestNoMatchIsCompleteAndPublishes is Q02's producer half: a complete
// search without matches is status 1 and still publication eligible, while
// an unreadable input is status 2 and never eligible.
func TestNoMatchIsCompleteAndPublishes(t *testing.T) {
	h := newHarness(t, map[string]string{"input": "a\n", "locked/f": "z\n", "open": "a\n"})
	r := h.exec("", "grep", "z", "input")
	r.expect(t, "no match", "", "", 1)
	if !r.result.Complete || !r.result.PublicationEligible || r.result.PublicationBasis != toolcontract.NativeComparisonPublication {
		t.Fatalf("no match settles %+v", r.result)
	}
	locked := filepath.Join(h.root, "locked")
	testutil.FailErr(t, "lock", os.Chmod(locked, 0))
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if entries, err := os.ReadDir(locked); err == nil && len(entries) > 0 {
		t.Logf("the host reads mode-000 directories; the permission fixture is covered by the owner refusal path")
		return
	}
	r = h.exec("", "grep", "-r", "z", ".")
	r.expect(t, "unreadable subtree", "", "grep: ./locked: Permission denied\n", 2)
	if r.result.PublicationEligible {
		t.Fatalf("incomplete search must not publish: %+v", r.result)
	}
}

func TestSearchRefusals(t *testing.T) {
	h := newHarness(t, grepTree)
	for _, c := range []struct {
		name string
		argv []string
		code string
	}{
		{"unsupported option", []string{"grep", "--not-a-version-1-option", "x"}, "COMMAND_OPTION_UNSUPPORTED"},
		{"unsupported PCRE", []string{"grep", "-P", "(?R)", "input"}, "GREP_REGEX_UNSUPPORTED"},
		{"variable lookbehind", []string{"grep", "-P", "(?<=a+)b", "input"}, "GREP_REGEX_UNSUPPORTED"},
		{"pattern budget", []string{"grep", string(make([]byte, 600)), "input"}, "GREP_MATCH_BUDGET"},
		{"BSD decompression", []string{"grep", "-J", "a", "input"}, "COMMAND_OPTION_UNSUPPORTED"},
		{"outside the roots", []string{"grep", "a", "/etc/hosts"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.exec("", c.argv...)
			if c.code == "" {
				if r.err == nil || r.result.Terminal != toolcontract.ProducerRefused || r.stdout != "" || r.result.PublicationEligible {
					t.Fatalf("confinement: %+v %v %q", r.result, r.err, r.stdout)
				}
				return
			}
			r.refused(t, c.name, c.code)
		})
	}
}

func TestSearchUtilityErrors(t *testing.T) {
	h := newHarness(t, grepTree)
	for _, c := range []struct {
		name   string
		argv   []string
		stderr string
	}{
		{"invalid ERE", []string{"grep", "-E", "(", "input"}, "grep: Unmatched ( or \\(\n"},
		{"invalid BRE", []string{"grep", `\(`, "input"}, "grep: Unmatched ( or \\(\n"},
		{"RE2 lacks references", []string{"rg", `(a)\1`, "input"}, ""},
		{"conflicting matchers", []string{"grep", "-E", "-F", "x", "input"}, "grep: one matcher; conflicting matchers specified\n"},
		{"egrep with fixed", []string{"egrep", "-F", "x", "input"}, "egrep: one matcher; conflicting matchers specified\n"},
		{"missing pattern", []string{"grep"}, "grep: a PATTERN operand or -e PATTERN\n"},
		{"missing pattern option value", []string{"grep", "-e"}, "grep: -e: pattern value for -e\n"},
		{"missing pattern file", []string{"grep", "-f", "missing", "input"}, "grep: missing: No such file or directory\n"},
		{"bad max count", []string{"grep", "-m", "x", "a", "input"}, "grep: x: a nonnegative decimal count for -m\n"},
		{"bad context", []string{"grep", "-A", "-1", "a", "input"}, "grep: -1: a nonnegative decimal count for -A\n"},
		{"unknown rg type", []string{"rg", "-t", "nope", "a"}, ""},
		{"rg replacement group", []string{"rg", "-r", "$2", "(a)", "input"}, "rg: $2: capture group $2 not defined by the pattern (it defines 1)\n"},
		{"color always", []string{"grep", "--color=always", "a", "input"}, "grep: always: --color=never or --color=auto\n"},
		{"basic with extended", []string{"grep", "-G", "-E", "a", "input"}, "grep: one matcher; conflicting matchers specified\n"},
		{"bad binary mode", []string{"grep", "--binary-files=maybe", "a", "input"}, "grep: maybe: --binary-files=binary, text or without-match\n"},
		{"bad directory action", []string{"grep", "-d", "maybe", "a", "input"}, "grep: maybe: -d read, skip or recurse\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := h.exec("", c.argv...)
			if r.err != nil || r.result.Terminal != toolcontract.ProducerCompleted || r.result.Status != 2 || r.stdout != "" || r.result.PublicationEligible {
				t.Fatalf("%s: want utility error status 2, got result %+v err %v stdout %q stderr %q", c.name, r.result, r.err, r.stdout, r.stderr)
			}
			if c.stderr != "" && r.stderr != c.stderr {
				t.Fatalf("%s: stderr %q, want %q", c.name, r.stderr, c.stderr)
			}
			if r.stderr == "" {
				t.Fatalf("%s: empty stderr", c.name)
			}
		})
	}
}

func TestSearchLimitsRefuseInsteadOfTruncating(t *testing.T) {
	h := newHarness(t, grepTree)
	h.owners.overrides = map[string]int64{"traversal_visits": 2}
	h.exec("", "grep", "-r", "alpha", "nested").refused(t, "visits", "COMMAND_LIMIT_EXCEEDED")
	h.exec("", "find", "nested").refused(t, "find visits", "COMMAND_LIMIT_EXCEEDED")
	h.owners.overrides = map[string]int64{"traversal_visits": 3}
	h.exec("", "find", "nested").expect(t, "at visit ceiling", "nested\nnested/x\nnested/x/y.txt\n", "", 0)
	h.exec("", "grep", "-r", "alpha", "nested").expect(t, "grep at visit ceiling", "nested/x/y.txt:alpha nested\n", "", 0)
	h.owners.overrides = nil
	h.owners.StreamBytes = 10
	h.exec("", "grep", "alpha", "a.txt").refused(t, "stream", "COMMAND_LIMIT_EXCEEDED")
	h.owners.StreamBytes = 0
	h.owners.ReadBytes = 4
	h.exec("", "grep", "alpha", "a.txt").refused(t, "read", "READ_FILE_TOO_LARGE")
}
