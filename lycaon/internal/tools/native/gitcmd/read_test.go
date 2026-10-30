package gitcmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/commandsurface"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitengine"
	gittestsetup "github.com/lycaon/lycaon/internal/testsetup/git"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestMain(m *testing.M) {
	gittestsetup.Enable()
	os.Exit(m.Run())
}

// fixtureEnv pins identities and dates so every repository is reproducible.
var fixtureEnv = []string{
	"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_PAGER=cat", "GIT_OPTIONAL_LOCKS=0",
	"GIT_AUTHOR_NAME=Ann Author", "GIT_AUTHOR_EMAIL=ann@example.test", "GIT_AUTHOR_DATE=2026-01-02T03:04:05Z",
	"GIT_COMMITTER_NAME=Ann Author", "GIT_COMMITTER_EMAIL=ann@example.test", "GIT_COMMITTER_DATE=2026-01-02T03:04:05Z",
}

type harness struct {
	t        *testing.T
	root     string
	bin      string
	template string
	owners   *Owners
	paths    *projectpaths.ProgramPaths
	tc       tools.ToolContext
}

func newHarness(t *testing.T) *harness {
	h := newEmptyHarness(t)
	h.write("a.txt", "one\ntwo\n")
	h.write("sub/b.txt", "bee\n")
	h.git("add", ".")
	h.git("commit", "-q", "-m", "first commit")
	h.git("tag", "v1")
	h.write("a.txt", "one\ntwo\nthree\n")
	h.git("commit", "-q", "-am", "second commit")
	h.git("branch", "topic")
	h.write("a.txt", "one\nTWO\nthree\n")
	h.write("staged.txt", "s\n")
	h.git("add", "staged.txt")
	h.write("new.txt", "untracked\n")
	return h
}

func newEmptyHarness(t *testing.T) *harness {
	h := newUninitializedHarness(t)
	h.git("init", "-q", "-b", "main")
	h.git("config", "user.name", "Ann Author")
	h.git("config", "user.email", "ann@example.test")
	return h
}

func newUninitializedHarness(t *testing.T) *harness {
	t.Helper()
	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "bundled git", err)
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	owners, err := NewOwners(git.NewManager())
	testutil.FailErr(t, "owners", err)
	boundary := nativefixture.Boundary(t)
	owners.Boundary = boundary
	tc := nativefixture.Context(root)
	paths, err := projectpaths.NewProgramPaths(t.Context(), boundary, tc, "")
	testutil.FailErr(t, "program paths", err)
	h := &harness{t: t, root: root, bin: bin, template: t.TempDir(), owners: owners, paths: paths, tc: tc}
	return h
}

func (h *harness) write(name, content string) {
	h.t.Helper()
	full := filepath.Join(h.root, name)
	testutil.FailErr(h.t, "mkdir", os.MkdirAll(filepath.Dir(full), 0o755))
	testutil.FailErr(h.t, "write "+name, os.WriteFile(full, []byte(content), 0o644))
}

// git runs the bundled binary directly, as fixture setup and as the
// reference the frontend must reproduce.
func (h *harness) git(args ...string) string {
	h.t.Helper()
	out, _, status := h.direct(h.root, args...)
	if status != 0 {
		h.t.Fatalf("git %v: status %d: %s", args, status, out)
	}
	return out
}

func (h *harness) direct(dir string, args ...string) (string, string, int) {
	cmd := exec.Command(h.bin, append([]string{"-c", "color.ui=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + h.root, "GIT_TEMPLATE_DIR=" + h.template}, fixtureEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	status := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		status = exit.ExitCode()
	} else if err != nil {
		h.t.Fatal(err)
	}
	return stdout.String(), stderr.String(), status
}

type run struct {
	stdout, stderr string
	result         toolcontract.ProducerResult
	err            error
}

// exec binds and invokes one git argv the way the umbrella dispatch does.
func (h *harness) exec(words ...string) run {
	h.t.Helper()
	inv := toolcontract.OperationInvocation{Kind: toolcontract.ProgramChildInvocation, Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "n1", ExecutionID: uuid.NewString()},
		ActorID: "actor", SessionID: "s1", Scope: toolcontract.OperationScope{Kind: toolcontract.ProjectOperationScope, ProjectID: "p1", RootID: "r1"},
		SourceUnitID: "program", Span: toolcontract.SourceSpan{Line: 1, Column: 1}, CatalogDigest: toolcontract.LanguageCatalogDigest()}
	argv := make(commandprogram.Fields, len(words))
	for i, word := range words {
		argv[i] = commandprogram.Field{Value: word, Span: inv.Span}
	}
	statement := commandsurface.Statement{SyntaxLocation: commandsurface.SyntaxLocation{ID: commandsurface.NodeID(inv.Identity.NodeID), Span: inv.Span}}
	request := commandprogram.OperationRequest{Invocation: inv, OperationID: "git", Statement: statement, Argv: argv}
	prepared, err := h.owners.PrepareGit(h.paths, request)
	if err != nil {
		return run{err: err, result: toolcontract.ProducerResult{Status: 2, Terminal: toolcontract.ProducerRefused, EffectCertainty: toolcontract.NoEffect}}
	}
	var stdout, stderr bytes.Buffer
	result, err := prepared.InvokeOwner(h.t.Context(), h.tc, commandprogram.Streams{Input: io.NopCloser(strings.NewReader("")), Output: &stdout, Error: &stderr})
	if invalid := result.Validate(); invalid != nil {
		h.t.Fatalf("%v produced an invalid result %+v: %v", words, result, invalid)
	}
	return run{stdout: stdout.String(), stderr: stderr.String(), result: result, err: err}
}

func (r run) refused(t *testing.T, label, code string) {
	t.Helper()
	reject := toolrejection.AsToolReject(r.err)
	if reject == nil || reject.Code != code || r.stdout != "" {
		t.Fatalf("%s: want refusal %s, got %+v err %v stdout %q", label, code, r.result, r.err, r.stdout)
	}
}

// readCases are the catalog read forms and the everyday spellings of them.
// Each runs through the frontend and through the bundled git directly in the
// same repository, and must match it byte for byte.
var readCases = []string{
	"git status", "git status -s", "git status -sb", "git status --short --branch", "git status --porcelain",
	"git status --porcelain=v1", "git status --porcelain=v2", "git status -uno", "git status --untracked-files=all",
	"git status -z", "git status -- sub",
	"git diff", "git diff --stat", "git diff --name-only", "git diff --name-status", "git diff --numstat",
	"git diff --shortstat", "git diff --cached", "git diff --staged --stat", "git diff -U1", "git diff --unified=0",
	"git diff HEAD~1", "git diff HEAD~1 -- a.txt", "git diff --exit-code", "git diff -w", "git diff topic main",
	"git log", "git log --oneline", "git log --oneline -n 1", "git log -1", "git log --stat", "git log --name-only",
	"git log --name-status", "git log -p -n 1", "git log --format=%h%x20%s", "git log --pretty=%an%x20%ae", "git log --graph --oneline --all",
	"git log --reverse --oneline", "git log --oneline -- sub", "git log --oneline topic", "git log --author=Ann --oneline",
	"git log --grep=second --oneline", "git log --oneline --decorate", "git log --first-parent --no-merges --oneline",
	"git show", "git show --stat", "git show --name-only", "git show -s --format=%s", "git show --oneline -s",
	"git show HEAD~1:a.txt", "git show v1 --stat",
	"git blame sub/b.txt", "git blame -L 1,1 sub/b.txt", "git blame -s sub/b.txt", "git blame -e sub/b.txt", "git blame HEAD~1 -- a.txt",
	"git blame --porcelain HEAD -- a.txt",
	"git branch", "git branch -a", "git branch --list", "git branch --list t*", "git branch -v", "git branch --show-current",
	"git branch --contains HEAD~1", "git branch --merged main", "git branch -r",
	"git tag --list", "git tag -l v*",
	"git rev-parse HEAD", "git rev-parse --verify HEAD", "git rev-parse --short HEAD", "git rev-parse --short=7 HEAD",
	"git rev-parse --abbrev-ref HEAD", "git rev-parse --show-toplevel", "git rev-parse --git-dir",
	"git rev-parse --is-inside-work-tree", "git rev-parse --symbolic-full-name HEAD",
	"git rev-list --count HEAD", "git rev-list -n 1 HEAD", "git rev-list --reverse HEAD",
	"git merge-base main topic", "git merge-base --is-ancestor topic main", "git merge-base --is-ancestor main v1",
	"git remote", "git remote -v",
	"git config --get user.name", "git config --list", "git config -l", "git config --get-regexp ^user",
	"git config --get no.such",
	"git stash list", "git worktree list", "git worktree list --porcelain",
	"git ls-files", "git ls-files -m", "git ls-files -o --exclude-standard", "git ls-files -s", "git ls-files --cached -- sub",
	"git ls-files -z",
	"git --no-pager log --oneline", "git -c core.quotepath=false status -s", "git -C sub log --oneline",
	"git -C sub rev-parse --show-prefix", "git -C sub ls-files",
	"git log --oneline nope", "git show nope",
}

// TestReadFormsMatchBundledGit: every read form is Git's own output and status.
func TestReadFormsMatchBundledGit(t *testing.T) {
	h := newHarness(t)
	for _, line := range readCases {
		t.Run(line, func(t *testing.T) {
			words := strings.Fields(line)
			r := h.exec(words...)
			if r.err != nil {
				t.Fatalf("%s: %v", line, r.err)
			}
			dir, rest := h.root, words[1:]
			var args []string
			for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
				switch rest[0] {
				case "-C":
					dir, rest = filepath.Join(dir, rest[1]), rest[2:]
				case "-c":
					args, rest = append(args, rest[0], rest[1]), rest[2:]
				default:
					rest = rest[1:]
				}
			}
			if rest[0] == "config" {
				rest = append([]string{"config", "--local"}, rest[1:]...)
			}
			args = append(args, rest...)
			stdout, stderr, status := h.direct(dir, args...)
			if r.stdout != stdout || r.result.Status != status || r.stderr != stderr {
				t.Fatalf("%s:\nstdout %q\nwant   %q\nstderr %q want %q\nstatus %d want %d", line, r.stdout, stdout, r.stderr, stderr, r.result.Status, status)
			}
			if r.result.Terminal != toolcontract.ProducerCompleted || !r.result.Complete || r.result.EffectCertainty != toolcontract.NoEffect {
				t.Fatalf("%s: result %+v", line, r.result)
			}
		})
	}
}

// TestEverydayFormsGolden pins the output of the forms agents use most.
func TestEverydayFormsGolden(t *testing.T) {
	h := newHarness(t)
	head := strings.TrimSpace(h.git("rev-parse", "--short", "HEAD"))
	first := strings.TrimSpace(h.git("rev-parse", "--short", "HEAD~1"))
	cases := []struct{ argv, stdout string }{
		{"git status -sb", "## main\n M a.txt\nA  staged.txt\n?? new.txt\n"},
		{"git log --oneline -n 2", head + " second commit\n" + first + " first commit\n"},
		{"git diff --stat", " a.txt | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)\n"},
		{"git diff --name-only", "a.txt\n"},
		{"git diff --cached --name-only", "staged.txt\n"},
		{"git show --stat --format=%s", "second commit\n\n a.txt | 1 +\n 1 file changed, 1 insertion(+)\n"},
		{"git branch -a", "* main\n  topic\n"},
		{"git rev-parse --show-toplevel", h.root + "\n"},
		{"git rev-parse --abbrev-ref HEAD", "main\n"},
		{"git ls-files", "a.txt\nstaged.txt\nsub/b.txt\n"},
		{"git -C sub ls-files", "b.txt\n"},
		{"git tag -l", "v1\n"},
	}
	for _, c := range cases {
		r := h.exec(strings.Fields(c.argv)...)
		if r.err != nil || r.stdout != c.stdout || r.result.Status != 0 || !r.result.PublicationEligible {
			t.Fatalf("%s:\nstdout %q\nwant   %q\nresult %+v err %v", c.argv, r.stdout, c.stdout, r.result, r.err)
		}
	}
	diff := h.exec("git", "diff", "--exit-code", "--quiet")
	if diff.result.Status != 1 || !diff.result.PublicationEligible || diff.result.PublicationBasis != toolcontract.NativeComparisonPublication {
		t.Fatalf("diff --exit-code: %+v", diff.result)
	}
}

// TestReadFormsRefuseWritesAndUncataloguedForms: a read spelling that would
// write, reach the network or run a program refuses before Git runs.
func TestReadFormsRefuseWritesAndUncataloguedForms(t *testing.T) {
	h := newHarness(t)
	before := h.git("for-each-ref")
	config := h.git("config", "--local", "--list")
	for _, c := range []struct {
		argv []string
		code string
	}{
		{[]string{"git", "branch", "created"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "tag", "v2"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "config", "user.name", "Mallory"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "config", "--get", "user.name", "--list"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "config", "--get", "user.name", "--get", "user.email"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "config", "--unset", "user.name"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "config", "--add", "user.name", "Changed"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "config", "--global", "--list"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "config", "--file", "other.config", "--list"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "config", "--includes", "--list"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "remote", "add", "origin", "https://example.test/r.git"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "remote", "show", "origin"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "stash", "list", "extra"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "log", "--format=%G?"}, "COMMAND_OPERAND_INVALID"},
		{[]string{"git", "-c", "core.pager=less", "log"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "-c", "diff.external=evil", "diff"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "diff", "--no-index", "a.txt", "new.txt"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "log", "--output=leak.txt"}, "COMMAND_OPTION_UNSUPPORTED"},
		{[]string{"git", "bisect"}, "COMMAND_NATIVE_FORM_UNSUPPORTED"},
		{[]string{"git", "gc"}, "COMMAND_NATIVE_FORM_UNSUPPORTED"},
		{[]string{"git", "-C", "/etc", "status"}, ""},
	} {
		r := h.exec(c.argv...)
		if c.code == "" {
			if toolrejection.AsToolReject(r.err) == nil || r.stdout != "" {
				t.Fatalf("%v escaped confinement: %+v err %v", c.argv, r.result, r.err)
			}
			continue
		}
		r.refused(t, strings.Join(c.argv, " "), c.code)
	}
	if got := h.git("for-each-ref"); got != before {
		t.Fatalf("refs changed:\n%s\nwas\n%s", got, before)
	}
	if got := h.git("config", "--local", "--list"); got != config {
		t.Fatalf("config changed")
	}
	if _, err := os.Stat(filepath.Join(h.root, "leak.txt")); err == nil {
		t.Fatal("log --output wrote a file")
	}
}

// TestGitDirectoryFailuresAreGitsOwn: a missing -C directory or a directory
// outside any repository reports Git's fatal message and status 128.
func TestGitDirectoryFailuresAreGitsOwn(t *testing.T) {
	h := newHarness(t)
	missing := h.exec("git", "-C", "nope", "status")
	if missing.err != nil || missing.result.Status != 128 || missing.stderr != "fatal: cannot change to 'nope': No such file or directory\n" {
		t.Fatalf("missing -C: %+v %q %v", missing.result, missing.stderr, missing.err)
	}
	testutil.FailErr(t, "plain dir", os.MkdirAll(filepath.Join(h.root, "plain"), 0o755))
	testutil.FailErr(t, "unlink repo", os.Rename(filepath.Join(h.root, ".git"), filepath.Join(h.root, "dot-git")))
	outside := h.exec("git", "-C", "plain", "status")
	if outside.err != nil || outside.result.Status != 128 || outside.stderr != "fatal: not a git repository (or any of the parent directories): .git\n" {
		t.Fatalf("not a repo: %+v %q %v", outside.result, outside.stderr, outside.err)
	}
}

// TestResolveSkipsGlobalOptions: the umbrella resolves the longest
// catalogued subcommand after global options.
func TestResolveSkipsGlobalOptions(t *testing.T) {
	h := newHarness(t)
	for argv, want := range map[string]string{
		"git status": "git status", "git -C sub -c color.ui=false stash list": "git stash list", "git --no-pager worktree list": "git worktree list",
		"git -Csub log": "git log", "git stash": "git stash", "git frobnicate": "",
	} {
		words := strings.Fields(argv)
		fields := make(commandprogram.Fields, len(words))
		for i, word := range words {
			fields[i] = commandprogram.Field{Value: word}
		}
		if got, _ := h.owners.Resolve(fields); got != want {
			t.Fatalf("%s resolved to %q, want %q", argv, got, want)
		}
	}
}

// TestStreamBoundRefuses: output past stream_bytes is a typed refusal.
func TestStreamBoundRefuses(t *testing.T) {
	h := newHarness(t)
	h.owners.limitOverride = map[string]int64{"stream_bytes": 8}
	h.exec("git", "log").refused(t, "stream bound", "COMMAND_LIMIT_EXCEEDED")
}
