package gitcmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// gitAs runs fixture Git with its own author and dates, so history has
// distinct contributors and a stable committer order.
func (h *harness) gitAs(name, date string, args ...string) {
	h.t.Helper()
	cmd := exec.Command(h.bin, args...)
	cmd.Dir = h.root
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.root, "GIT_TEMPLATE_DIR=" + h.template}, fixtureEnv...)
	cmd.Env = append(cmd.Env, "GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+strings.ToLower(name)+"@example.test",
		"GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	if out, err := cmd.CombinedOutput(); err != nil {
		h.t.Fatalf("git %v: %v %s", args, err, out)
	}
}

var historyTemplate = repoTemplate{name: "history"}

// newHistoryHarness builds five commits by two authors with annotated and
// lightweight tags, three branches and a hard reset in the reflog.
func newHistoryHarness(t *testing.T) *harness {
	return historyTemplate.materialize(t, func(h *harness) {
		seedEmptyRepo(h)
		steps := []struct{ author, date, file, content, message string }{
			{"Ann", "2026-01-01T00:00:00Z", "src/main.go", "package main\n\nfunc legacyEntry() {}\n", "add entry"},
			{"Ann", "2026-01-02T00:00:00Z", "docs/guide.md", "legacyEntry is documented here\n", "document entry"},
			{"Bob", "2026-01-03T00:00:00Z", "src/main.go", "package main\n\nfunc entry() {}\n", "rename entry"},
			{"Bob", "2026-01-04T00:00:00Z", "src/util.go", "package main\n\nfunc helper() {}\n", "add helper"},
			{"Ann", "2026-01-05T00:00:00Z", "bin.dat", "bin\x00entry\n", "add data"},
		}
		for i, step := range steps {
			h.write(step.file, step.content)
			h.gitAs(step.author, step.date, "add", "--", step.file)
			h.gitAs(step.author, step.date, "commit", "-q", "-m", step.message)
			switch i {
			case 0:
				h.gitAs("Ann", step.date, "tag", "-a", "-m", "first release", "v1.0")
			case 2:
				h.gitAs("Bob", step.date, "branch", "older")
			case 3:
				h.gitAs("Bob", step.date, "tag", "v1.1-light")
				h.gitAs("Bob", step.date, "branch", "newer")
			}
		}
		h.write("scratch.txt", "discarded\n")
		h.gitAs("Ann", "2026-01-06T00:00:00Z", "add", "--", "scratch.txt")
		h.gitAs("Ann", "2026-01-06T00:00:00Z", "commit", "-q", "-m", "temporary")
		h.gitAs("Ann", "2026-01-06T00:00:00Z", "reset", "-q", "--hard", "HEAD~1")
	})
}

func TestHistoryQueriesMatchBundledGit(t *testing.T) {
	t.Parallel()
	base := newHistoryHarness(t)
	for _, line := range []string{
		"git describe --tags --always",
		"git describe --always",
		"git describe --tags --long --abbrev=10 HEAD~1",
		"git describe --match v1* --exclude *-light HEAD",
		"git describe --tags --dirty",
		"git reflog -n 5",
		"git reflog show --oneline -3 main",
		"git shortlog -sn v1.0..HEAD",
		"git shortlog -s -e HEAD -- src",
		"git ls-tree -r --name-only HEAD",
		"git ls-tree -r -l HEAD src",
		"git ls-tree -d HEAD",
		"git ls-tree -r -z HEAD~1",
		"git cat-file -p HEAD:src/main.go",
		"git cat-file -t HEAD:src",
		"git cat-file -s HEAD:bin.dat",
		"git cat-file -e HEAD:absent",
		"git show-ref --heads",
		"git show-ref --tags -d",
		"git show-ref --verify -s refs/heads/main",
		"git for-each-ref --sort=-committerdate --count=3 --format=%(refname:short) refs/heads",
		"git for-each-ref --format=%(objecttype) %(refname) refs/tags",
	} {
		t.Run(line, func(t *testing.T) {
			t.Parallel()
			h := base.withT(t)
			r := h.exec(strings.Fields(line)...)
			testutil.FailErr(t, line, r.err)
			stdout, stderr, status := h.direct(h.root, strings.Fields(line)[1:]...)
			if r.stdout != stdout || r.stderr != stderr || r.result.Status != status {
				t.Fatalf("stdout %q\nwant   %q\nstderr %q want %q\nstatus %d want %d", r.stdout, stdout, r.stderr, stderr, r.result.Status, status)
			}
			if r.result.EffectCertainty != toolcontract.NoEffect || !r.result.Complete {
				t.Fatalf("result %+v", r.result)
			}
		})
	}
}

func TestHistoryQueryRequiredFacts(t *testing.T) {
	t.Parallel()
	h := newHistoryHarness(t)
	if got := h.exec("git", "describe", "--tags", "--dirty").stdout; strings.Contains(got, "dirty") || !strings.HasPrefix(got, "v1.1-light-1-g") {
		t.Fatalf("clean describe %q", got)
	}
	h.write("src/util.go", "package main\n")
	if got := h.exec("git", "describe", "--tags", "--dirty").stdout; !strings.HasSuffix(got, "-dirty\n") {
		t.Fatalf("dirty describe %q", got)
	}
	temporary := strings.TrimSpace(h.git("rev-parse", "--short", "HEAD@{1}"))
	if got := h.exec("git", "reflog", "-n", "5").stdout; !strings.Contains(got, temporary+" HEAD@{1}: commit: temporary") || !strings.Contains(got, "HEAD@{0}: reset: moving to HEAD~1") {
		t.Fatalf("reflog %q lacks the entry before the reset", got)
	}
	if got := h.exec("git", "shortlog", "-sn", "v1.0..HEAD").stdout; got != "     2\tAnn\n     2\tBob\n" {
		t.Fatalf("shortlog %q", got)
	}
	if got, want := h.exec("git", "shortlog", "-s").stdout, h.git("shortlog", "-s", "HEAD"); got != want {
		t.Fatalf("shortlog without a revision %q, want the HEAD summary %q", got, want)
	}
	if got := h.exec("git", "ls-tree", "-r", "--name-only", "HEAD").stdout; got != "bin.dat\ndocs/guide.md\nsrc/main.go\nsrc/util.go\n" {
		t.Fatalf("ls-tree %q", got)
	}
	if got := h.exec("git", "cat-file", "-p", "HEAD:bin.dat").stdout; got != "bin\x00entry\n" {
		t.Fatalf("cat-file bytes %q", got)
	}
	if got := h.exec("git", "cat-file", "-t", "HEAD:src").stdout; got != "tree\n" {
		t.Fatalf("cat-file type %q", got)
	}
	if got := h.exec("git", "for-each-ref", "--sort=-committerdate", "--count=3", "--format=%(refname:short)", "refs/heads").stdout; got != "main\nnewer\nolder\n" {
		t.Fatalf("for-each-ref %q", got)
	}
}

func TestHistoryQueriesRefuseWritesAndUncataloguedForms(t *testing.T) {
	t.Parallel()
	h := newHistoryHarness(t)
	reflog := h.git("reflog")
	for _, c := range []struct {
		argv []string
		code string
	}{
		{[]string{"git", "cat-file", "--batch"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "cat-file", "--batch-check"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "cat-file", "-t", "-p", "HEAD"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "cat-file", "blob", "HEAD:bin.dat"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "reflog", "expire", "--all"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "reflog", "delete", "HEAD@{1}"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "ls-tree"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "for-each-ref", "--format=%(signature)"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "grep", "-E", "-F", "x"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "grep"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "describe", "--contains"}, "COMMAND_OPTION_UNSUPPORTED"},
	} {
		h.exec(c.argv...).refused(t, strings.Join(c.argv, " "), c.code)
	}
	if got := h.git("reflog"); got != reflog {
		t.Fatal("refused reflog form changed the reflog")
	}
}

func TestGrepMatchesBundledGit(t *testing.T) {
	t.Parallel()
	base := newHistoryHarness(t)
	base.write("src/main.go", "package main\n\nfunc entry() {}\n// Entry point\n")
	base.write("docs/guide.md", "entry staged\n")
	base.git("add", "--", "docs/guide.md")
	base.write("docs/guide.md", "entry working\n")
	for _, c := range []struct{ dir, line string }{
		{"", "git grep entry"},
		{"", "git grep -n entry HEAD"},
		{"", "git grep -n legacyEntry HEAD~3 -- src/"},
		{"", "git grep --cached -n entry"},
		{"", "git grep -i -w ENTRY HEAD"},
		{"", "git grep -c entry HEAD HEAD~3"},
		{"", "git grep -l entry"},
		{"", "git grep -h -n entry HEAD"},
		{"", "git grep -v -n package HEAD -- src"},
		{"", "git grep -n -e helper -e legacy HEAD~2 HEAD"},
		{"", "git grep -F -n entry() HEAD"},
		{"", "git grep -E -n (helper|entry)\\( HEAD"},
		{"", "git grep -z -n entry HEAD"},
		{"", "git grep -n entry HEAD:src"},
		{"", "git grep -q entry HEAD"},
		{"", "git grep absent-pattern HEAD"},
		{"", "git grep entry no-such-revision"},
		{"", "git grep -- entry"},
		{"src", "git grep -n entry HEAD"},
		{"src", "git grep -n entry HEAD -- ../docs"},
		{"src", "git grep -n entry"},
	} {
		t.Run(c.dir+" "+c.line, func(t *testing.T) {
			t.Parallel()
			h := base.withT(t)
			words := strings.Fields(c.line)
			if c.dir != "" {
				words = append([]string{"git", "-C", c.dir}, words[1:]...)
			}
			r := h.exec(words...)
			testutil.FailErr(t, c.line, r.err)
			stdout, stderr, status := h.direct(filepath.Join(h.root, c.dir), strings.Fields(c.line)[1:]...)
			if r.stdout != stdout || r.stderr != stderr || r.result.Status != status {
				t.Fatalf("stdout %q\nwant   %q\nstderr %q want %q\nstatus %d want %d", r.stdout, stdout, r.stderr, stderr, r.result.Status, status)
			}
		})
	}
}

func TestGrepFindsLinesOnlyAtTheRevision(t *testing.T) {
	t.Parallel()
	h := newHistoryHarness(t)
	r := h.exec("git", "grep", "-n", "legacyEntry", "HEAD~3", "--", "src/")
	testutil.FailErr(t, "grep", r.err)
	if r.stdout != "HEAD~3:src/main.go:3:func legacyEntry() {}\n" || r.result.Status != 0 {
		t.Fatalf("grep at revision %q %+v", r.stdout, r.result)
	}
	if r := h.exec("git", "grep", "-n", "legacyEntry", "--", "src/"); r.result.Status != 1 || r.stdout != "" {
		t.Fatalf("working tree still matched %q %+v", r.stdout, r.result)
	}
	r = h.exec("git", "grep", "-P", "-n", `entry\(\)\s*\{`, "HEAD")
	testutil.FailErr(t, "perl grep", r.err)
	if r.stdout != "HEAD:src/main.go:3:func entry() {}\n" {
		t.Fatalf("perl grep %q", r.stdout)
	}
	h.owners.limitOverride = map[string]int64{"stream_bytes": 8}
	for _, words := range [][]string{{"git", "grep", "entry", "HEAD"}, {"git", "grep", "entry"}} {
		h.exec(words...).refused(t, strings.Join(words, " "), "COMMAND_LIMIT_EXCEEDED")
	}
}
