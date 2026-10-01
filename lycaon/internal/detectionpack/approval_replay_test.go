package detectionpack

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// resolvedTempDir mirrors what the boundary hands the resolver: production write
// roots arrive from confine.WriteRootsForProject, which has already resolved
// symlinks, so a raw /var/folders temp path would never match its own /private
// counterpart.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve temp dir", err)
	return dir
}

// Routine project cleanup inside the sandbox's writable set resolves `local`, so
// a rule's `filter_local` clause keeps it silent. Cases are (command, write roots)
// → does a card reach the person, run through the shipped catalog.
func TestContainedDestructionIsSilent(t *testing.T) {
	t.Parallel()
	cat, err := LoadCatalog(shippedInput(t))
	testutil.FailErr(t, "LoadCatalog", err)
	m := NewMatcher(cat)

	project := resolvedTempDir(t)
	granted := resolvedTempDir(t)
	outside := resolvedTempDir(t)
	// The boundary permits the project and any root the person granted; `outside`
	// stands in for a location no grant covers.
	roots := []string{project, granted}

	cases := []struct {
		name    string
		command string
		wantAsk bool
	}{
		// Routine cleanup inside the project.
		{name: "relative_node_modules", command: "rm -rf node_modules"},
		{name: "dot_relative_build", command: "rm -rf ./build"},
		{name: "absolute_in_project", command: "rm -rf " + filepath.Join(project, "dist")},
		{name: "find_delete_in_project", command: "find . -name '*.tmp' -delete"},
		{name: "git_clean_worktree", command: "git clean -xfd"},
		// A write root the person reviewed and granted, then destroyed inside,
		// including the glob form.
		{name: "granted_root", command: "rm -rf " + filepath.Join(granted, "registry", "index")},
		{name: "granted_root_glob", command: "rm -rf " + filepath.Join(granted, "registry", "index", "localhost-*")},
		{name: "git_reset_hard_in_granted_root", command: "git -C " + granted + " reset --hard origin/main"},

		// Destruction the boundary does not already permit still asks.
		{name: "outside_roots", command: "rm -rf " + filepath.Join(outside, "data"), wantAsk: true},
		{name: "filesystem_root", command: "rm -rf /", wantAsk: true},
		{name: "home_tilde", command: "rm -rf ~/Documents/notes", wantAsk: true},
		{name: "escaping_relative", command: "rm -rf ../../elsewhere", wantAsk: true},
		{name: "git_reset_hard_outside", command: "git -C " + outside + " reset --hard origin/main", wantAsk: true},

		{name: "cargo_publish", command: "cargo publish --registry crates-io", wantAsk: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{"command": tc.command}
			reach := ResolveEffectReach("command", args, project, roots)
			hit, matched := m.Match(testEventWithReach("command", tc.command, project, true, "proxy", "sess", reach))
			asked := matched && Escalates(hit.Level, "balanced")
			if asked != tc.wantAsk {
				t.Fatalf("ask=%v want %v (reach=%s hit=%s/%s level=%s)",
					asked, tc.wantAsk, reach, hit.PackID, hit.RuleID, hit.Level)
			}
		})
	}
}

// `local` is a claim that the boundary already permits every effect this action
// has. These are the ways that claim must not be reachable — each one would hand
// `filter_local` to a rule about something the write jail never contained.
func TestLocalClaimStaysUnreachable(t *testing.T) {
	t.Parallel()
	project := resolvedTempDir(t)
	roots := []string{project}
	cases := []struct {
		name    string
		command string
	}{
		// No boundary applied means no containment to appeal to. Covered separately
		// below, since it is the empty-roots case rather than a command shape.

		// A cluster delete that happens to name a contained manifest file.
		{name: "kubectl_delete_local_manifest", command: "kubectl delete -f ./manifest.yaml"},
		// Aggregate classification stays conservative even though matching assigns
		// reach per process, so one contained stage never speaks for another.
		{name: "contained_delete_then_egress", command: "rm -rf build && curl https://example.com/x"},
		{name: "contained_delete_piped_to_egress", command: "find . -name '*.log' -delete | curl -T - https://example.com"},
		// An unrecognized program that merely takes a contained path.
		{name: "unknown_program_local_path", command: "terraform destroy -state ./terraform.tfstate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reach := ResolveEffectReach("command", map[string]any{"command": tc.command}, project, roots)
			if reach == EffectReachLocal {
				t.Fatalf("%q classified local: filter_local would silence a rule the boundary never contained", tc.command)
			}
		})
	}
}

// Resolver-level cases: how a target is spelled must not change whether the host
// can prove containment.
func TestFilesystemEffectReachResolution(t *testing.T) {
	t.Parallel()
	project := resolvedTempDir(t)
	outside := resolvedTempDir(t)
	roots := []string{project}

	cases := []struct {
		name    string
		command string
		cwd     string
		want    string
	}{
		{name: "bare_operand", command: "rm -rf node_modules", want: EffectReachLocal},
		{name: "absolute", command: "rm -rf " + filepath.Join(project, "a", "b"), want: EffectReachLocal},
		{name: "multiple_targets", command: "rm -rf a b c", want: EffectReachLocal},
		// A glob cannot escape its own parent directory, so a contained parent
		// contains every expansion.
		{name: "glob_contained_parent", command: "rm -rf " + filepath.Join(project, "idx") + "/localhost-*", want: EffectReachLocal},
		{name: "glob_in_base", command: "rm -rf *", want: EffectReachLocal},
		// cwd is where relative operands land.
		{name: "relative_to_cwd", command: "rm -rf dist", cwd: "packages/web", want: EffectReachLocal},
		{name: "absolute_cwd_outside", command: "rm -rf dist", cwd: outside, want: EffectReachUnproven},

		// Unprovable or escaping targets stay unproven, which asks.
		{name: "escaping_relative", command: "rm -rf ../..", want: EffectReachUnproven},
		{name: "glob_walking_up", command: "rm -rf " + project + "/*/../../etc", want: EffectReachUnproven},
		{name: "home_expansion", command: "rm -rf ~/notes", want: EffectReachUnproven},
		{name: "outside_absolute", command: "rm -rf " + outside, want: EffectReachUnproven},
		{name: "mixed_inside_and_outside", command: "rm -rf build " + outside, want: EffectReachUnproven},
		{name: "no_operands", command: "rm -rf", want: EffectReachUnproven},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{"command": tc.command}
			if tc.cwd != "" {
				args["cwd"] = tc.cwd
			}
			if got := ResolveEffectReach("command", args, project, roots); got != tc.want {
				t.Fatalf("reach = %q, want %q", got, tc.want)
			}
		})
	}
}

// An action the boundary did not confine reports no write roots. Containment is
// then unproven, so nothing may be classified local on the strength of a path that
// merely looks like it is inside a project.
func TestUnconfinedActionIsNeverLocal(t *testing.T) {
	t.Parallel()
	project := t.TempDir()
	reach := ResolveEffectReach("command",
		map[string]any{"command": "rm -rf node_modules"}, project, nil)
	if reach != EffectReachUnproven {
		t.Fatalf("reach = %q, want %q for an unconfined action", reach, EffectReachUnproven)
	}
}
