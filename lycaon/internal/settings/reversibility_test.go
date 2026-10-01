package settings_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestClassifyTierUsesStructuredFacts(t *testing.T) {
	cases := []struct {
		name   string
		action hitl.ProposedAction
		want   settings.ReversibilityTier
	}{
		{
			name: "reversible edit in project",
			action: hitl.ProposedAction{
				Tool: "edit", Files: []string{"/proj/src/a.go"}, ProjectDir: "/proj",
			},
			want: settings.TierReversible,
		},
		{
			name:   "recoverable commit",
			action: hitl.ProposedAction{Tool: "git_commit", ProjectDir: "/proj"},
			want:   settings.TierRecoverable,
		},
		{
			name: "structured path escape",
			action: hitl.ProposedAction{
				Tool: "edit", Files: []string{"/etc/hosts"}, ProjectDir: "/proj",
			},
			want: settings.TierIrreversible,
		},
		{
			name:   "unknown tool",
			action: hitl.ProposedAction{Tool: "mystery_tool"},
			want:   settings.TierIrreversible,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := settings.ClassifyTier(tc.action); got != tc.want {
				t.Fatalf("tier=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestClassifyTierCommandUsesContainmentFact(t *testing.T) {
	dir := t.TempDir()
	contained := hitl.Contained{
		FSJailed: true, Egress: hitl.ContainedEgressProxy, Roots: []string{dir},
	}
	for _, command := range []string{
		"go test ./...",
		"cat /etc/hosts",
		`sh -c "cat /etc/hosts"`,
		"git push origin main",
	} {
		for _, tc := range []struct {
			name      string
			contained hitl.Contained
			want      settings.ReversibilityTier
		}{
			{name: "contained", contained: contained, want: settings.TierRecoverable},
			{name: "uncontained", want: settings.TierIrreversible},
			{
				name: "filesystem only",
				contained: hitl.Contained{
					FSJailed: true, Egress: hitl.ContainedEgressDirectIP, Roots: []string{dir},
				},
				want: settings.TierIrreversible,
			},
		} {
			t.Run(tc.name+"/"+command, func(t *testing.T) {
				got := settings.ClassifyTier(hitl.ProposedAction{
					Tool: "command", Args: map[string]any{"command": command}, ProjectDir: dir,
					Contained: tc.contained,
				})
				if got != tc.want {
					t.Fatalf("tier=%v, want %v", got, tc.want)
				}
			})
		}
	}
}

func TestCoordinatorInternalToolsNeverGate(t *testing.T) {
	internal := []string{
		"update_progress", "record_finding", "request_decision", "answer_decision",
		"extend_worker_budget", "worker_cancel",
		"preview_overlay", "promote_overlay", "reject_overlay",
		"delegate_dispatch", "delegate_init", "delegate_status", "delegate_decompose",
		"state_start", "state_close", "state_query", "state_update",
		"workflow_catalog_summaries", "pack_board", "wait",
		"scan_pack", "scan_list", "scan_summary", "scan_query", "scan_compare",
	}
	for _, tool := range internal {
		if tier := settings.ClassifyTier(hitl.ProposedAction{Tool: tool, ProjectDir: "/proj"}); tier == settings.TierIrreversible {
			t.Errorf("%s classifies irreversible", tool)
		}
	}
}

// The posture ladder itself is pinned in internal/gate/posture_test.go, beside the
// table it reads. What settings defines is the mapping to the egress default.
func TestEgressPostureForFollowsTheAskLine(t *testing.T) {
	if got := settings.EgressPostureFor(gate.PostureStrict); got != confine.PostureAsk {
		t.Fatalf("strict egress = %v want ask", got)
	}
	for _, p := range []gate.Posture{gate.PostureBalanced, gate.PostureLight} {
		if got := settings.EgressPostureFor(p); got != confine.PostureObserve {
			t.Fatalf("%s egress = %v want observe", p, got)
		}
	}
}

func TestPathEscapesWorkspaceCatchesRelativeWalkOut(t *testing.T) {
	// Synthetic roots live under a real temp directory: canonicalizing a path
	// under /home stalls on the macOS automounter.
	project := filepath.Join(t.TempDir(), "proj")
	escape := func(path string) bool {
		return settings.PathEscapesWorkspace(hitl.ProposedAction{ProjectDir: project, Files: []string{path}})
	}
	for _, path := range []string{"../outside.txt", "../../.ssh/authorized_keys", "a/../../b", "..", "./../x"} {
		if !escape(path) {
			t.Errorf("PathEscapesWorkspace(%q)=false, want escape", path)
		}
	}
	for _, path := range []string{"src/main.go", "./src/main.go", "a/../b", project + "/src/main.go"} {
		if escape(path) {
			t.Errorf("PathEscapesWorkspace(%q)=true, want inside", path)
		}
	}
}

// A write worker's whole workspace is its disposable branch, which sits under no
// attached project root — the confinement roots are part of the boundary.
func TestPathEscapesWorkspaceHonorsConfinementRoots(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, ".config", "paintedwolf", "drafts", "f6a404b9")
	branch := filepath.Join(home, ".config", "paintedwolf", "worker-branches", "253f5529", "4d513aaf")
	outside := filepath.Join(t.TempDir(), "other-repo", "main.go")
	worker := func(path string) hitl.ProposedAction {
		return hitl.ProposedAction{
			Tool:       "list_dir",
			ProjectDir: project,
			Files:      []string{path},
			Contained:  hitl.Contained{FSJailed: true, Roots: []string{branch}},
		}
	}
	for _, path := range []string{branch, branch + "/ntp-health", project + "/notes.md"} {
		if settings.PathEscapesWorkspace(worker(path)) {
			t.Errorf("PathEscapesWorkspace(%q)=true, want inside", path)
		}
		if tier := settings.ClassifyTier(worker(path)); tier != settings.TierReversible {
			t.Errorf("ClassifyTier(list_dir %q)=%v, want reversible", path, tier)
		}
	}
	for _, path := range []string{"/etc/hosts", outside} {
		if !settings.PathEscapesWorkspace(worker(path)) {
			t.Errorf("PathEscapesWorkspace(%q)=false, want escape", path)
		}
	}
	// Confinement that did not apply contributes no roots, leaving the project_dir
	// boundary exactly where it was.
	unconfined := hitl.ProposedAction{Tool: "list_dir", ProjectDir: project, Files: []string{branch}}
	if !settings.PathEscapesWorkspace(unconfined) {
		t.Error("PathEscapesWorkspace with no confinement roots must still measure project_dir")
	}
}

func TestAliasOfARootIsNotACrossing(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "root-link")
	testutil.FailErr(t, "symlink root", os.Symlink(real, alias))

	// The person attached the alias; the tool named the resolved location.
	action := hitl.ProposedAction{
		Tool: "read", ProjectDir: alias, Files: []string{filepath.Join(real, "main.go")},
	}
	if settings.PathEscapesWorkspace(action) {
		t.Fatal("a file under the root's real location must not read as a crossing")
	}
	action.ProjectDir = real
	action.Files = []string{filepath.Join(alias, "main.go")}
	if settings.PathEscapesWorkspace(action) {
		t.Fatal("a file named through an alias of the root must not read as a crossing")
	}
	action.Files = []string{filepath.Join(filepath.Dir(alias), "elsewhere.go")}
	if !settings.PathEscapesWorkspace(action) {
		t.Fatal("a sibling of the alias is still outside the root")
	}
}
