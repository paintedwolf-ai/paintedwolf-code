package contract

// Applied Linux confinement runs one declared scope on every architecture.

import (
	"encoding/json"
	"go/ast"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const linuxConfineScript = "scripts/test-confine-linux.sh"

func linuxConfineScopes(t *testing.T, root string) []goTestScope {
	t.Helper()
	var scopes []goTestScope
	for _, line := range strings.Split(contractcheck.ReadRepoFile(t, root, linuxConfineScript), "\n") {
		if strings.Contains(line, "go-test-digest.sh\"") {
			scopes = append(scopes, parseGoTestScopes(linuxConfineScript, line)...)
		}
	}
	if len(scopes) == 0 {
		t.Fatalf("parsed no Go test scopes from %s", linuxConfineScript)
	}
	return scopes
}

func TestLinuxConfineScopeRunsFullAndNamesLiveTests(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	live := allGoTestNames(t, root)
	whole := false
	var violations []string
	for _, scope := range linuxConfineScopes(t, root) {
		if !strings.Contains(scope.Command, "--full") {
			violations = append(violations, "runs without --full, so testing.Short() skips confinement tests: "+strings.TrimSpace(scope.Command))
		}
		whole = whole || scope.coversPackage("./internal/confine/linuxsandbox") && scope.Run == nil
		for _, name := range deadRunNames(scope, live) {
			violations = append(violations, "-run names "+name+", which is not a test function")
		}
	}
	if !whole {
		violations = append(violations, "no scope runs ./internal/confine/... unfiltered")
	}
	contractcheck.FailViolations(t, linuxConfineScript+" scope is incomplete", violations)
}

// Every command supervisor test runs on each architecture; a new one cannot fall outside the filter.
func TestLinuxConfineScopeCoversEverySupervisorTest(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scopes := linuxConfineScopes(t, root)
	var inventory, uncovered []string
	walkTestFiles(t, root, func(pkg string, fset *token.FileSet, file *ast.File, _ []byte) {
		if pkg != "./internal/exec" || !strings.HasPrefix(filepath.Base(fset.Position(file.Pos()).Filename), "supervisor") {
			return
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				inventory = append(inventory, fn.Name.Name)
			}
		}
	})
	if len(inventory) == 0 {
		t.Fatal("found no command supervisor tests in internal/exec; the inventory scan is broken")
	}
	for _, test := range inventory {
		if !slices.ContainsFunc(scopes, func(scope goTestScope) bool { return scope.covers("./internal/exec", test) }) {
			uncovered = append(uncovered, test)
		}
	}
	slices.Sort(uncovered)
	contractcheck.FailViolations(t, "command supervisor tests missing from the "+linuxConfineScript+" supervisor leg", uncovered)
}

func TestLinuxConfineScopeRequiresAppliedHelperAndBrowserAdmissionFixtures(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scopes := linuxConfineScopes(t, root)
	var missing []string
	for _, fixture := range []struct{ pkg, test string }{
		{"./internal/mcp", "TestLinuxStdioMCPAppliedBoundary"},
		{"./internal/scan/integration", "TestLinuxBundledScannerAppliedBoundary"},
		{"./internal/documentcore", "TestLinuxDocumentCoreAppliedBoundary"},
		{"./internal/browser", "TestBrowserUnavailableLinuxBoundaryDoesNotLaunch"},
		{"./internal/browser", "TestBrowserBoundaryRefusesWhenConfinementCannotBeBuilt"},
		{"./internal/browser", "TestLaunchHeadlessRefusesUnsafeProjectRootBeforeProvisioning"},
	} {
		if !slices.ContainsFunc(scopes, func(scope goTestScope) bool { return scope.covers(fixture.pkg, fixture.test) }) {
			missing = append(missing, fixture.pkg+" "+fixture.test)
		}
	}
	contractcheck.FailViolations(t, "required applied Linux helper/browser fixtures missing from native qualification", missing)
}

// amd64 runs the target in platform qualification, and an arm64 lane runs the same target.
func TestLinuxConfineRunsOnBothArchitectures(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	job := extractYAMLJob(contractcheck.ReadRepoFile(t, root, ".github/workflows/platform-verification.yml"), "upgrade-corpus")
	if !strings.Contains(job, "run: ./task test:confine:linux\n") || !strings.Contains(job, "runs-on: ubuntu-latest") {
		t.Error("the amd64 upgrade-corpus job must run ./task test:confine:linux")
	}
	var plan struct {
		CI map[string]struct {
			Targets  []string `json:"targets"`
			Profiles []string `json:"profiles"`
			Runner   string   `json:"runner"`
		} `json:"ci"`
	}
	contractcheck.FailErr(t, "decode verification plan",
		json.Unmarshal([]byte(contractcheck.ReadRepoFile(t, root, "scripts/verification-plan.json")), &plan))
	for _, lane := range plan.CI {
		if lane.Runner == "ubuntu-24.04-arm" && slices.Contains(lane.Targets, "test:confine:linux") &&
			slices.Contains(lane.Profiles, "release") && slices.Contains(lane.Profiles, "nightly") {
			return
		}
	}
	t.Error("no release and nightly CI lane runs test:confine:linux on ubuntu-24.04-arm")
}
