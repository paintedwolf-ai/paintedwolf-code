package gitcmd

import (
	"context"
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
// remote) is marked unlaunched.
var formInvocations = map[string]struct {
	argv       []string
	unlaunched bool
}{
	"git status": {argv: []string{"status", "--short"}}, "git diff": {argv: []string{"diff"}},
	"git blame": {argv: []string{"blame", "a.txt"}}, "git ls-files": {argv: []string{"ls-files"}},
	"git grep": {argv: []string{"grep", "one"}}, "git describe": {argv: []string{"describe", "--tags"}},
	"git log": {argv: []string{"log", "--oneline"}}, "git show": {argv: []string{"show", "--stat"}},
	"git branch": {argv: []string{"branch", "--list"}}, "git tag": {argv: []string{"tag", "-l"}},
	"git rev-parse": {argv: []string{"rev-parse", "HEAD"}}, "git rev-list": {argv: []string{"rev-list", "HEAD"}},
	"git merge-base": {argv: []string{"merge-base", "HEAD", "topic"}}, "git remote": {argv: []string{"remote"}},
	"git config": {argv: []string{"config", "--get", "user.name"}}, "git stash list": {argv: []string{"stash", "list"}},
	"git worktree list": {argv: []string{"worktree", "list"}}, "git reflog": {argv: []string{"reflog"}},
	"git shortlog": {argv: []string{"shortlog", "-s", "HEAD"}}, "git ls-tree": {argv: []string{"ls-tree", "HEAD"}},
	"git cat-file": {argv: []string{"cat-file", "-t", "HEAD"}}, "git show-ref": {argv: []string{"show-ref"}},
	"git for-each-ref": {argv: []string{"for-each-ref"}},

	"git add": {argv: []string{"add", "new.txt"}}, "git commit": {argv: []string{"commit", "-m", "staged work"}},
	"git restore": {argv: []string{"restore", "a.txt"}}, "git checkout": {argv: []string{"checkout", "topic"}},
	"git switch": {argv: []string{"switch", "topic"}}, "git merge": {argv: []string{"merge", "topic"}},
	"git cherry-pick": {argv: []string{"cherry-pick", "v1"}}, "git revert": {argv: []string{"revert", "--no-edit", "HEAD"}},
	"git rebase": {argv: []string{"rebase", "topic"}}, "git stash": {argv: []string{"stash"}},
	"git reset": {argv: []string{"reset", "--hard"}}, "git clean": {argv: []string{"clean", "-n"}},
	"git worktree": {argv: []string{"worktree", "prune"}}, "git apply": {argv: []string{"apply", "--check", "new.txt"}},
	"git rm": {argv: []string{"rm", "sub/b.txt"}}, "git mv": {argv: []string{"mv", "sub/b.txt", "sub/c.txt"}},
	"git bisect": {argv: []string{"bisect", "good"}}, "git bisect start": {argv: []string{"bisect", "start"}},
	"git bisect skip": {argv: []string{"bisect", "skip"}}, "git bisect run": {argv: []string{"bisect", "run", "true"}},
	"git bisect reset": {argv: []string{"bisect", "reset"}}, "git bisect log": {argv: []string{"bisect", "log"}},
	"git bisect terms": {argv: []string{"bisect", "terms"}}, "git bisect visualize": {argv: []string{"bisect", "visualize"}},
	"git bisect view": {argv: []string{"bisect", "view"}}, "git init": {argv: []string{"init", "fresh"}},

	"git pull": {argv: []string{"pull"}, unlaunched: true}, "git fetch": {argv: []string{"fetch"}, unlaunched: true},
	"git push": {argv: []string{"push"}, unlaunched: true}, "git ls-remote": {argv: []string{"ls-remote"}, unlaunched: true},
	"git clone": {argv: []string{"clone", "https://example.test/repo.git", "cloned"}, unlaunched: true},
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
	drafts := &nativefixture.DiskDrafts{Dir: h.root, Blocked: map[string]string{}, OnSave: func() { events.note("save") }}
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
			if treeForms[name] {
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

// Git reads what a native read serves: each form that reads the working tree
// reports, stages or carries the person's unsaved text.
func TestGitWorkingTreeFormsSeeThePersonsDraft(t *testing.T) {
	for _, c := range []struct {
		name  string
		argv  []string
		sees  func(h *harness, r run) bool
		draft string
	}{
		{name: "diff", argv: []string{"diff"}, sees: func(_ *harness, r run) bool { return strings.Contains(r.stdout, "+typed by the person") }},
		{name: "status", argv: []string{"status", "--short"}, draft: "sub/b.txt", sees: func(_ *harness, r run) bool { return strings.Contains(r.stdout, "sub/b.txt") }},
		{name: "grep", argv: []string{"grep", "typed by"}, sees: func(_ *harness, r run) bool { return strings.Contains(r.stdout, "a.txt") }},
		{name: "blame", argv: []string{"blame", "a.txt"}, sees: func(_ *harness, r run) bool { return strings.Contains(r.stdout, "typed by the person") }},
		{name: "add", argv: []string{"add", "a.txt"}, sees: func(h *harness, _ run) bool {
			return strings.Contains(h.git("diff", "--cached"), "+typed by the person")
		}},
		{name: "stash", argv: []string{"stash"}, sees: func(h *harness, _ run) bool {
			return strings.Contains(h.git("stash", "show", "-p"), "+typed by the person")
		}},
		{name: "mv", argv: []string{"mv", "a.txt", "moved.txt"}, sees: func(h *harness, _ run) bool {
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
	r := h.exec("diff")
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
