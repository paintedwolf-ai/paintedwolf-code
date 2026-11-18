package contract

// The applied-Seatbelt recipes run in full and name only live tests.

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

const seatbeltWholePackageScope = "./internal/confine"

func TestSeatbeltWholePackageScopeRunsUnfiltered(t *testing.T) {
	t.Parallel()
	for _, scope := range seatbeltDeclaredScopes(t, contractcheck.RepoRoot(t)) {
		if scope.coversPackage(seatbeltWholePackageScope) && scope.Run == nil {
			return
		}
	}
	t.Fatalf("no declared seatbelt scope runs %s unfiltered — the confinement package "+
		"is the core of the applied-Seatbelt suite and must not sit behind a -run filter",
		seatbeltWholePackageScope)
}

func TestSeatbeltTaskRunsFull(t *testing.T) {
	t.Parallel()
	body := taskBody(taskfile(t), "test:seatbelt")
	if body == "" {
		t.Fatal("Taskfile.yml has no test:seatbelt task")
	}
	if !strings.Contains(body, "test-execution.py plan -- test:seatbelt") {
		t.Fatal("test:seatbelt must execute its catalog plan")
	}
	for _, scope := range seatbeltCatalogScopes(t, contractcheck.RepoRoot(t)) {
		if !strings.Contains(scope.Command, "--full") {
			t.Errorf("%s must run with --full", scope.Source)
		}
	}
}

func TestSeatbeltScopeNamesLiveTests(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	live := allGoTestNames(t, root)

	var dead []string
	for _, scope := range seatbeltDeclaredScopes(t, root) {
		for _, name := range deadRunNames(scope, live) {
			dead = append(dead, scope.Source+": -run names "+name+
				", which is not a test function (a prefix of one is not one — an anchored "+
				"alternative that matches nothing runs nothing and exits 0)")
		}
	}
	contractcheck.FailViolations(t, "seatbelt run filters name tests that do not exist", contractcheck.DedupeStrings(dead))
}

func seatbeltDeclaredScopes(t *testing.T, root string) []goTestScope {
	t.Helper()
	var scopes []goTestScope

	body := taskBody(taskfile(t), "test:seatbelt")
	if body == "" {
		t.Fatal("Taskfile.yml has no test:seatbelt task")
	}
	scopes = append(scopes, seatbeltCatalogScopes(t, root)...)

	job := extractYAMLJob(contractcheck.ReadRepoFile(t, root, ".github/workflows/platform-verification.yml"), "confinement")
	if job == "" {
		t.Fatal(".github/workflows/platform-verification.yml has no confinement job")
	}
	scopes = append(scopes, parseGoTestScopes("platform-verification.yml confinement", job)...)
	scopes = append(scopes, linuxConfineScopes(t, root)...)

	if len(scopes) == 0 {
		t.Fatal("parsed no run scopes from the seatbelt task or the confinement job")
	}
	return scopes
}

func seatbeltCatalogScopes(t *testing.T, root string) []goTestScope {
	t.Helper()
	var catalog struct {
		Groups map[string][]string `json:"groups"`
		Go     map[string]struct {
			Options  []string `json:"options"`
			Packages []string `json:"packages"`
			Flags    []string `json:"flags"`
		} `json:"go"`
	}
	data := contractcheck.ReadRepoFile(t, root, "scripts/verification-plan.json")
	contractcheck.FailErr(t, "decode Seatbelt recipes", json.Unmarshal([]byte(data), &catalog))
	legs := catalog.Groups["test:seatbelt"]
	if len(legs) == 0 {
		t.Fatal("Seatbelt verification has no catalog stages")
	}
	var scopes []goTestScope
	for _, name := range legs {
		recipe, ok := catalog.Go[name]
		if !ok || len(recipe.Packages) == 0 {
			t.Fatalf("Seatbelt stage %s has no Go recipe", name)
		}
		scope := goTestScope{Source: "verification-plan.json " + name, Packages: recipe.Packages,
			Command: strings.Join(append(recipe.Options, recipe.Flags...), " ")}
		for _, flag := range recipe.Flags {
			if run, ok := strings.CutPrefix(flag, "-run="); ok {
				scope.RunSource = run
				scope.Run = regexp.MustCompile(run)
			}
		}
		scopes = append(scopes, scope)
	}
	return scopes
}
