package contract

// Scanner-gated tests require a reachable task with the scanner resource.

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const scannerSuiteTask = "test:scanners"

const scannerResourceHelper = "testutil.MissingScannerResource"

func TestScannerGatedTestsAreCoveredByTheSuite(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	inventory := packagesCallingHelper(t, root, scannerResourceHelper)
	if len(inventory) == 0 {
		t.Fatalf("no test calls %s — the scanner inventory is empty",
			scannerResourceHelper)
	}
	scopes := scannerSuiteScopes(t)

	packages := make([]string, 0, len(inventory))
	for pkg := range inventory {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)

	var uncovered []string
	for _, pkg := range packages {
		for _, test := range inventory[pkg] {
			covered := false
			for _, scope := range scopes {
				if scope.covers(pkg, test) {
					covered = true
					break
				}
			}
			if !covered {
				uncovered = append(uncovered, pkg+" "+test)
			}
		}
	}
	contractcheck.FailViolations(t, "tests gate on a bundled scanner engine but "+scannerSuiteTask+
		" does not run them, so their skip is the only outcome CI ever sees — widen the "+
		"package list in Taskfile.yml", uncovered)
}

func TestScannerSuiteMakesMissingEnginesFatal(t *testing.T) {
	t.Parallel()
	want := testutil.EnvScannerSuiteRequired + "=1"
	wrapper := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/with-bundled-scanners.sh")
	if !strings.Contains(wrapper, want) {
		t.Fatalf("bundled scanner wrapper must set %s", want)
	}
	for _, scope := range scannerSuiteScopes(t) {
		if !strings.Contains(scope.Command, "--full") {
			t.Fatalf("%s runs a leg without --full, so testing.Short() skips the scanner "+
				"tests it exists to run:\n  %s", scannerSuiteTask, strings.TrimSpace(scope.Command))
		}
		if scope.Run == nil {
			t.Fatalf("%s must select only scanner-gated tests:\n  %s",
				scannerSuiteTask, strings.TrimSpace(scope.Command))
		}
	}
}

func TestScannerSuiteRunsOnlyScannerGatedTests(t *testing.T) {
	t.Parallel()
	inventory := packagesCallingHelper(t, contractcheck.RepoRoot(t), scannerResourceHelper)
	allowed := make(map[string]bool)
	for _, tests := range inventory {
		for _, name := range tests {
			allowed[name] = true
		}
	}

	var extra []string
	for _, scope := range scannerSuiteScopes(t) {
		for _, name := range goTestScopeNameToken.FindAllString(scope.RunSource, -1) {
			if !allowed[name] {
				extra = append(extra, name)
			}
		}
	}
	contractcheck.FailViolations(t, "scanner suite selects tests without scanner resources", contractcheck.DedupeStrings(extra))
}

func TestHostedProfilesRequireTheFullScannerSuite(t *testing.T) {
	t.Parallel()
	plan := verificationCatalog(t)
	// The profiles that judge a commit's code; the release profile judges only the world it ships into.
	for _, profile := range []string{"check", "qualification"} {
		found := false
		for _, lane := range plan.CI {
			if slices.Contains(lane.Profiles, profile) && slices.Contains(lane.Targets, "test:full") {
				found = true
			}
		}
		if !found {
			t.Errorf("hosted profile %s does not invoke the full Go and required scanner suite", profile)
		}
	}
}

func TestScannerSuiteStagesItsEngine(t *testing.T) {
	t.Parallel()
	body := taskBody(taskfile(t), scannerSuiteTask)
	if body == "" {
		t.Fatalf("Taskfile.yml has no %s task", scannerSuiteTask)
	}
	if !strings.Contains(body, "with-bundled-scanners.sh") ||
		!strings.Contains(contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "scripts/with-bundled-scanners.sh"), "resolve-opengrep.sh") {
		t.Fatalf("%s must resolve its bundled engine before running", scannerSuiteTask)
	}
}

func scannerSuiteScopes(t *testing.T) []goTestScope {
	t.Helper()
	body := taskBody(taskfile(t), scannerSuiteTask)
	if body == "" {
		t.Fatalf("Taskfile.yml has no %s task", scannerSuiteTask)
	}
	scopes := parseGoTestScopes("Taskfile.yml "+scannerSuiteTask, body)
	if len(scopes) == 0 {
		t.Fatalf("parsed no go test invocation from %s", scannerSuiteTask)
	}
	return scopes
}

func TestFullSuiteRequiresBundledScannersWithoutRepeatingTheSubset(t *testing.T) {
	plan := verificationCatalog(t)
	if !plan.Go["test:full"].Bundled {
		t.Fatal("the full Go suite must require the authenticated bundled scanners")
	}
	if slices.Contains(plan.Groups["check:tests"], "test:scanners") {
		t.Fatal("check:tests must not repeat the scanner subset after test:full")
	}
}
