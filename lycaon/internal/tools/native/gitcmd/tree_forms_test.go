package gitcmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	lycexec "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

// formInvocations is one runnable argv for every Git form, over the apply
// harness's tree. A form the harness cannot bring to a launch (it has no
// remote, no bisect in progress) is marked unlaunched.
var formInvocations = map[string]struct {
	argv       []string
	saves      bool
	unlaunched bool
}{
	"git status": {argv: []string{"git", "status", "--short"}, saves: true}, "git diff": {argv: []string{"git", "diff"}, saves: true},
	"git blame": {argv: []string{"git", "blame", "a.txt"}, saves: true}, "git ls-files": {argv: []string{"git", "ls-files"}},
	"git grep": {argv: []string{"git", "grep", "one"}, saves: true}, "git describe": {argv: []string{"git", "describe", "--tags"}},
	"git log": {argv: []string{"git", "log", "--oneline"}}, "git show": {argv: []string{"git", "show", "--stat"}},
	"git branch": {argv: []string{"git", "branch", "--list"}}, "git tag": {argv: []string{"git", "tag", "-l"}},
	"git rev-parse": {argv: []string{"git", "rev-parse", "HEAD"}}, "git rev-list": {argv: []string{"git", "rev-list", "HEAD"}},
	"git merge-base": {argv: []string{"git", "merge-base", "HEAD", "topic"}}, "git remote": {argv: []string{"git", "remote"}},
	"git config": {argv: []string{"git", "config", "--get", "user.name"}}, "git stash list": {argv: []string{"git", "stash", "list"}},
	"git worktree list": {argv: []string{"git", "worktree", "list"}}, "git reflog": {argv: []string{"git", "reflog"}},
	"git shortlog": {argv: []string{"git", "shortlog", "-s", "HEAD"}}, "git ls-tree": {argv: []string{"git", "ls-tree", "HEAD"}},
	"git cat-file": {argv: []string{"git", "cat-file", "-t", "HEAD"}}, "git show-ref": {argv: []string{"git", "show-ref"}},
	"git for-each-ref": {argv: []string{"git", "for-each-ref"}},

	"git add": {argv: []string{"git", "add", "new.txt"}, saves: true}, "git commit": {argv: []string{"git", "commit", "-m", "staged work"}, saves: true},
	"git restore": {argv: []string{"git", "restore", "a.txt"}, saves: true}, "git checkout": {argv: []string{"git", "checkout", "topic"}, saves: true},
	"git switch": {argv: []string{"git", "switch", "topic"}, saves: true}, "git merge": {argv: []string{"git", "merge", "topic"}, saves: true},
	"git cherry-pick": {argv: []string{"git", "cherry-pick", "v1"}, saves: true}, "git revert": {argv: []string{"git", "revert", "--no-edit", "HEAD"}, saves: true},
	"git rebase": {argv: []string{"git", "rebase", "topic"}, saves: true}, "git stash": {argv: []string{"git", "stash"}, saves: true},
	"git reset": {argv: []string{"git", "reset", "--hard"}, saves: true}, "git clean": {argv: []string{"git", "clean", "-n"}, saves: true},
	"git worktree": {argv: []string{"git", "worktree", "prune"}}, "git apply": {argv: []string{"git", "apply", "--check", "new.txt"}},
	"git rm": {argv: []string{"git", "rm", "sub/b.txt"}, saves: true}, "git mv": {argv: []string{"git", "mv", "sub/b.txt", "sub/c.txt"}, saves: true},
	"git bisect": {argv: []string{"git", "bisect", "good"}}, "git bisect start": {argv: []string{"git", "bisect", "start"}, saves: true},
	"git bisect skip": {argv: []string{"git", "bisect", "skip"}}, "git bisect run": {argv: []string{"git", "bisect", "run", "true"}},
	"git bisect reset": {argv: []string{"git", "bisect", "reset"}}, "git bisect log": {argv: []string{"git", "bisect", "log"}},
	"git bisect terms": {argv: []string{"git", "bisect", "terms"}}, "git bisect visualize": {argv: []string{"git", "bisect", "visualize"}, unlaunched: true},
	"git bisect view": {argv: []string{"git", "bisect", "view"}, unlaunched: true}, "git init": {argv: []string{"git", "init", "fresh"}},

	"git pull": {argv: []string{"git", "pull"}, unlaunched: true}, "git fetch": {argv: []string{"git", "fetch"}, unlaunched: true},
	"git push": {argv: []string{"git", "push"}, unlaunched: true}, "git ls-remote": {argv: []string{"git", "ls-remote"}, unlaunched: true},
	"git clone": {argv: []string{"git", "clone", "https://example.test/repo.git", "cloned"}, unlaunched: true},
}

func sortedForms[V any](forms map[string]V) []string {
	names := make([]string, 0, len(forms))
	for name := range forms {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Every form Git accepts says whether it reads the working tree, and nothing
// is classified that Git does not accept: a new form cannot arrive unclassified.
func TestEveryGitFormSaysWhetherItReadsTheTree(t *testing.T) {
	accepted := map[string]bool{}
	for name := range readForms {
		accepted[name] = true
	}
	for name := range mutateForms {
		accepted[name] = true
	}
	for _, name := range sortedForms(accepted) {
		if _, classified := treeForms[name]; !classified {
			t.Errorf("%s is not classified in treeForms", name)
		}
		if _, runnable := formInvocations[name]; !runnable {
			t.Errorf("%s has no invocation in formInvocations", name)
		}
	}
	for _, name := range sortedForms(treeForms) {
		if !accepted[name] {
			t.Errorf("treeForms classifies %s, which is not a form", name)
		}
	}
	for name := range bisectReads {
		if treeForms[name] {
			t.Errorf("%s changes neither tree nor session and must not save drafts", name)
		}
	}
}

// launchEvents orders a command's admitted launches and its draft saves.
type launchEvents struct {
	mu     sync.Mutex
	events []string
}

func (l *launchEvents) note(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *launchEvents) count(event string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.events {
		if e == event {
			n++
		}
	}
	return n
}

// draftHarness is the apply harness with a person's unsaved documents beside
// its tree and its launches observed.
func draftHarness(t *testing.T) (*harness, *nativefixture.DiskDrafts, *launchEvents) {
	t.Helper()
	h := newApplyHarness(t)
	events := &launchEvents{}
	drafts := &nativefixture.DiskDrafts{Blocked: map[string]string{}, OnSave: func() { events.note("save") }, Publish: func(path, text string) error {
		return os.WriteFile(filepath.Join(h.root, filepath.FromSlash(path)), []byte(text), 0o644)
	}}
	h.tc.Identity.ProjectID = "p1"
	h.tc.Source.EditorDocuments = drafts
	h.owners.Launch = func(ctx context.Context, tc tools.ToolContext, subject, line string, services []confine.HostService) (lycexec.LaunchPlan, func(context.Context, tools.AgentLaunchOutcome), error) {
		events.note("launch")
		return UnconfinedLaunch(ctx, tc, subject, line, services)
	}
	return h, drafts, events
}

// For every form: a form that reads the working tree saves the person's
// drafts once, after its launch is admitted; every other form saves nothing;
// and a command that never reaches a launch saves nothing.
func TestGitSavesDraftsExactlyForFormsThatReadTheTree(t *testing.T) {
	for _, name := range sortedForms(formInvocations) {
		invocation := formInvocations[name]
		t.Run(strings.ReplaceAll(name, " ", "-"), func(t *testing.T) {
			h, drafts, events := draftHarness(t)
			drafts.Type("notes.txt", "typed by the person\n")
			h.exec(invocation.argv...)
			launches, saves := events.count("launch"), events.count("save")
			if launches == 0 {
				if !invocation.unlaunched {
					t.Fatalf("%v never reached its launch, so it proves nothing about %s", invocation.argv, name)
				}
				if saves != 0 {
					t.Fatalf("a command refused before its launch saved drafts %d times", saves)
				}
				return
			}
			want := 0
			if invocation.saves {
				want = 1
			}
			if saves != want {
				t.Fatalf("%s saved drafts %d times, want %d", name, saves, want)
			}
			if want == 1 && events.events[0] != "launch" {
				t.Fatalf("%s saved drafts before its launch was admitted: %v", name, events.events)
			}
			_, onDisk := h.lookup("notes.txt")
			if onDisk != (want == 1) {
				t.Fatalf("%s: the draft on disk = %v, want %v", name, onDisk, want == 1)
			}
		})
	}
}

func TestGitDraftSavesFollowTheSelectedSource(t *testing.T) {
	for _, tc := range []struct {
		name  string
		argv  []string
		saves bool
	}{
		{name: "diff index", argv: []string{"diff", "--staged"}},
		{name: "blame commit", argv: []string{"blame", "HEAD", "--", "a.txt"}},
		{name: "list modified", argv: []string{"ls-files", "--modified"}, saves: true},
		{name: "list untracked", argv: []string{"ls-files", "--others"}, saves: true},
		{name: "list deleted", argv: []string{"ls-files", "--deleted"}, saves: true},
		{name: "describe dirty", argv: []string{"describe", "--tags", "--dirty"}, saves: true},
		{name: "grep index", argv: []string{"grep", "--cached", "one"}},
		{name: "grep commit", argv: []string{"grep", "one", "HEAD"}},
		{name: "restore index", argv: []string{"restore", "--staged", "a.txt"}},
		{name: "reset soft", argv: []string{"reset", "--soft", "HEAD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, drafts, events := draftHarness(t)
			drafts.Type("notes.txt", "typed by the person\n")
			got := h.exec(append([]string{"git"}, tc.argv...)...)
			if got.err != nil || got.result.Status != 0 {
				t.Fatalf("%v: status=%d err=%v stderr=%s", tc.argv, got.result.Status, got.err, got.stderr)
			}
			_, onDisk := h.lookup("notes.txt")
			if onDisk != tc.saves || (events.count("save") == 1) != tc.saves {
				t.Fatalf("%v: draft on disk=%v, events=%v, want save=%v", tc.argv, onDisk, events.events, tc.saves)
			}
		})
	}
}

func TestGitKeepsDraftsWhenWorktreeAdmissionFails(t *testing.T) {
	for _, argv := range [][]string{
		{"git", "-C", "absent", "status"},
		{"git", "add", "--", "../outside"},
		{"git", "commit", "-m", "example", "--", "../outside"},
		{"git", "grep", "-E", "["},
		{"git", "bisect", "start", "missing-ref"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			h, drafts, events := draftHarness(t)
			drafts.Type("notes.txt", "typed by the person\n")
			got := h.exec(argv...)
			if got.result.Status == 0 || events.count("launch") != 1 {
				t.Fatalf("expected a failure after launch binding: status=%d err=%v events=%v", got.result.Status, got.err, events.events)
			}
			if events.count("save") != 0 {
				t.Fatalf("draft save preceded admission: events=%v", events.events)
			}
		})
	}
}

func TestGitRechecksLaunchAuthorityBeforeSavingDrafts(t *testing.T) {
	h, drafts, events := draftHarness(t)
	drafts.Type("notes.txt", "typed by the person\n")
	bind := h.owners.Launch
	denied := errors.New("authority changed after binding")
	h.owners.Launch = func(ctx context.Context, tc tools.ToolContext, subject, line string, services []confine.HostService) (lycexec.LaunchPlan, func(context.Context, tools.AgentLaunchOutcome), error) {
		plan, release, err := bind(ctx, tc, subject, line, services)
		plan.CurrentAuthority = func(context.Context) error { return denied }
		return plan, release, err
	}
	got := h.exec("git", "status")
	if !errors.Is(got.err, denied) || events.count("save") != 0 {
		t.Fatalf("git status: err=%v events=%v", got.err, events.events)
	}
}

// Git reads what a native read serves: each form that reads the working tree
// reports, stages or carries the person's unsaved text.
func TestGitWorkingTreeFormsSeeThePersonsDraft(t *testing.T) {
	for _, c := range []struct {
		name  string
		argv  []string
		sees  func(h *harness, r run) bool
		draft string
	}{
		{name: "diff", argv: []string{"git", "diff"}, sees: func(_ *harness, r run) bool { return strings.Contains(r.stdout, "+typed by the person") }},
		{name: "status", argv: []string{"git", "status", "--short"}, draft: "sub/b.txt", sees: func(_ *harness, r run) bool { return strings.Contains(r.stdout, "sub/b.txt") }},
		{name: "grep", argv: []string{"git", "grep", "typed by"}, sees: func(_ *harness, r run) bool { return strings.Contains(r.stdout, "a.txt") }},
		{name: "blame", argv: []string{"git", "blame", "a.txt"}, sees: func(_ *harness, r run) bool { return strings.Contains(r.stdout, "typed by the person") }},
		{name: "add", argv: []string{"git", "add", "a.txt"}, sees: func(h *harness, _ run) bool {
			return strings.Contains(h.git("diff", "--cached"), "+typed by the person")
		}},
		{name: "stash", argv: []string{"git", "stash"}, sees: func(h *harness, _ run) bool {
			return strings.Contains(h.git("stash", "show", "-p"), "+typed by the person")
		}},
		{name: "mv", argv: []string{"git", "mv", "a.txt", "moved.txt"}, sees: func(h *harness, _ run) bool {
			moved, ok := h.lookup("moved.txt")
			return ok && strings.Contains(moved, "typed by the person")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, drafts, _ := draftHarness(t)
			path := c.draft
			if path == "" {
				path = "a.txt"
			}
			before, _ := h.lookup(path)
			drafts.Type(path, before+"typed by the person\n")
			r := h.exec(c.argv...)
			if r.err != nil || r.result.Status != 0 {
				t.Fatalf("%v: status=%d err=%v stderr=%s", c.argv, r.result.Status, r.err, r.stderr)
			}
			if !c.sees(h, r) {
				t.Fatalf("%v did not see the draft: stdout=%s", c.argv, r.stdout)
			}
		})
	}
}

// A draft Git's launch cannot save does not stop the command: Git reads the
// file's earlier bytes and the other drafts are saved.
func TestGitRunsOverADraftItCannotSave(t *testing.T) {
	h, drafts, _ := draftHarness(t)
	drafts.Type("a.txt", "unsavable\n")
	drafts.Type("sub/b.txt", "bee\ntyped by the person\n")
	drafts.Blocked["a.txt"] = "changed_outside"
	r := h.exec("git", "diff")
	if r.err != nil || r.result.Status != 0 {
		t.Fatalf("status=%d err=%v stderr=%s", r.result.Status, r.err, r.stderr)
	}
	if strings.Contains(r.stdout, "unsavable") || !strings.Contains(r.stdout, "+typed by the person") {
		t.Fatalf("diff = %s", r.stdout)
	}
}

// lookup reads a tree file that may not exist.
func (h *harness) lookup(name string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(h.root, filepath.FromSlash(name)))
	return string(raw), err == nil
}
