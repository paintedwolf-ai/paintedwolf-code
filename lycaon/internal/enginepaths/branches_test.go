package enginepaths

import (
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsWorkerBranchPath(t *testing.T) {
	cases := map[string]bool{
		"/Users/me/.config/paintedwolf/worker-branches/ab12cd34/job-1/foo.go":     true,
		"/Users/me/.config/paintedwolf-dev/worker-branches/deadbeef/uuid/main.py": true,
		"/tmp/override/worker-branches/ab12cd34/job-1/foo.go":                     true,
		settingsoverlay.Rel("blueprints/feature.md"):                              false,
		"shellsim/builtins.py":                                  false,
		"/Users/me/git/lycaon/internal/worker/promote_ready.go": false,
		"": false,
	}
	for path, want := range cases {
		if got := IsWorkerBranchPath(path); got != want {
			t.Fatalf("IsWorkerBranchPath(%q) = %v want %v", path, got, want)
		}
	}
}

func TestProjectKeyStableAndScoped(t *testing.T) {
	a := ProjectKey("/Users/me/git/projectA")
	b := ProjectKey("/Users/me/git/projectB")
	if a == b {
		t.Fatal("distinct projects must not collide")
	}
	if a != ProjectKey("/Users/me/git/projectA/") {
		t.Fatal("project key must be path-clean stable")
	}
	if len(a) != 16 {
		t.Fatalf("project key length = %d want 16", len(a))
	}
}

func TestJobBranchDirLayout(t *testing.T) {
	root := WorkerBranchesRootUnder("/state/lycaon")
	if root != filepath.FromSlash("/state/lycaon/worker-branches") {
		t.Fatalf("branch root = %q", root)
	}
	job := JobBranchDir(root, "/Users/me/repo", "job-1")
	want := filepath.Join(root, ProjectKey("/Users/me/repo"), "job-1")
	if job != want {
		t.Fatalf("job dir = %q want %q", job, want)
	}
}

func TestJobMetaDirIsSiblingNotJob(t *testing.T) {
	job := JobBranchDir(WorkerBranchesRootUnder("/state"), "/repo", "81157094-653f-4496-b675-dcf887c2d4b4")
	meta := MetaDirForBranchRoot(job)
	if meta != job+".meta" {
		t.Fatalf("meta dir = %q want %s.meta", meta, job)
	}
	if !IsJobMetaDirName(filepath.Base(meta)) {
		t.Fatal("meta sibling name must be recognized")
	}
	if IsJobMetaDirName(filepath.Base(job)) {
		t.Fatal("job id must not look like meta")
	}
	if IsJobMetaDirName("") || IsJobMetaDirName("readme.meta.txt") {
		t.Fatal("empty and non-suffix names are not meta dirs")
	}
}

func TestWorkerBranchDisplayRel(t *testing.T) {
	cases := map[string]string{
		"/Users/me/.config/paintedwolf-dev/worker-branches/5d960b8f0a1ff696/582d661b-6b3c-4018-a646-0e1468aeb7e6/Cargo.toml": "Cargo.toml",
		"/Users/me/.config/paintedwolf/worker-branches/ab12cd34/job-1/src/main.rs":                                           "src/main.rs",
		"/Users/me/.config/paintedwolf/worker-branches/ab12cd34/job-1":                                                       ".",
		"Users/me/.config/paintedwolf-dev/worker-branches/deadbeef/uuid/src/cli.rs":                                          "src/cli.rs",
		"shellsim/builtins.py": "shellsim/builtins.py",
	}
	for path, want := range cases {
		if got := WorkerBranchDisplayRel(path); got != want {
			t.Fatalf("WorkerBranchDisplayRel(%q) = %q want %q", path, got, want)
		}
	}
}

func TestRewriteWorkerBranchPaths(t *testing.T) {
	const branch = "/Users/me/.config/paintedwolf-dev/worker-branches/5d960b8f0a1ff696/582d661b-6b3c-4018-a646-0e1468aeb7e6"
	in := "stderr: Compiling mdlinter v0.1.0 (" + branch + ")\n" +
		"read " + branch + "/src/main.rs\n"
	got := RewriteWorkerBranchPaths(in)
	if strings.Contains(got, "worker-branches") || strings.Contains(got, "paintedwolf") {
		t.Fatalf("rewritten text still names the branch layout: %q", got)
	}
	if !strings.Contains(got, "Compiling mdlinter v0.1.0 (.)") {
		t.Fatalf("branch root should become '.': %q", got)
	}
	if !strings.Contains(got, "read src/main.rs") {
		t.Fatalf("file under branch should become repo-relative: %q", got)
	}
	if strings.Contains(got, "(src/main.rs") {
		t.Fatalf("a closed parenthetical root must not swallow the next path: %q", got)
	}
}

func TestProjectSeedDirLayout(t *testing.T) {
	root := WorkerSeedsRootUnder("/state/paintedwolf")
	if root != filepath.FromSlash("/state/paintedwolf/worker-seeds") {
		t.Fatalf("seed root = %q", root)
	}
	want := filepath.Join(root, ProjectKey("/Users/me/repo"))
	if got := ProjectSeedDir(root, "/Users/me/repo"); got != want {
		t.Fatalf("seed dir = %q want %q", got, want)
	}
}
