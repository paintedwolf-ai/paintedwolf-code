package contract

// OS-confinement tests require a matching CI scope.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Engagement APIs require kernel confinement.
var confineEngagementAPI = []string{"Available", "EnableAutoConfine", "TestingSetAutoConfine"}

const seatbeltWholePackageScope = "./internal/confine"

func TestSeatbeltGatedTestsAreCoveredByAGate(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	inventory := seatbeltGatedTests(t, root)
	scopes := seatbeltDeclaredScopes(t, root)

	packages := make([]string, 0, len(inventory))
	for pkg := range inventory {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)

	var uncovered []string
	for _, pkg := range packages {
		if pkg == seatbeltWholePackageScope {
			continue
		}
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
	contractcheck.FailViolations(t, "tests engage OS confinement but no CI scope runs them — off a "+
		"sandboxed host they skip, and a skip is indistinguishable from a pass. Add each to "+
		"the test:seatbelt -run filter in Taskfile.yml (or to the browser-confinement job in "+
		".github/workflows/ci.yml for browser tests, which is the only leg that stages a browser)",
		uncovered)
}

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
	for _, scope := range parseGoTestScopes("Taskfile.yml test:seatbelt", body) {
		if !strings.Contains(scope.Command, "--full") {
			t.Fatalf("test:seatbelt leg runs without --full, so testing.Short() skips the "+
				"confinement tests it exists to run:\n  %s", strings.TrimSpace(scope.Command))
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

func TestConfineEngagementAPIIsLive(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "confine")
	entries, err := os.ReadDir(dir)
	contractcheck.FailErr(t, "read internal/confine", err)

	exported := map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// Parse platform-tagged variants without applying build constraints.
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil && fn.Name.IsExported() {
				exported[fn.Name.Name] = true
			}
		}
	}

	var missing []string
	for _, name := range confineEngagementAPI {
		if !exported[name] {
			missing = append(missing, "confine."+name)
		}
	}
	contractcheck.FailViolations(t, "confineEngagementAPI names functions internal/confine no longer exports — "+
		"the seatbelt inventory derives from these, so a stale name empties it silently", missing)
}

func seatbeltGatedTests(t *testing.T, root string) map[string][]string {
	t.Helper()
	byPackage := testFuncBodiesByPackage(t, root)

	out := map[string][]string{}
	for pkg, funcs := range byPackage {
		for name := range seatbeltEngagedFuncs(funcs) {
			if strings.HasPrefix(name, "Test") {
				out[pkg] = append(out[pkg], name)
			}
		}
		sort.Strings(out[pkg])
	}
	for pkg, tests := range out {
		if len(tests) == 0 {
			delete(out, pkg)
		}
	}
	if len(out) == 0 {
		t.Fatal("derived no seatbelt-gated tests at all — the engagement scan is broken, " +
			"and every coverage assertion above would pass vacuously")
	}
	return out
}

func seatbeltEngagedFuncs(funcs map[string]string) map[string]bool {
	engaged := map[string]bool{}
	for name, body := range funcs {
		for _, api := range confineEngagementAPI {
			// Package-local tests call the API without a qualifier.
			if callsIdentifier(body, "confine."+api) || callsIdentifier(body, api) {
				engaged[name] = true
				break
			}
		}
	}
	for grew := true; grew; {
		grew = false
		for name, body := range funcs {
			if engaged[name] {
				continue
			}
			for helper := range engaged {
				if callsIdentifier(body, helper) {
					engaged[name] = true
					grew = true
					break
				}
			}
		}
	}
	return engaged
}

func seatbeltDeclaredScopes(t *testing.T, root string) []goTestScope {
	t.Helper()
	var scopes []goTestScope

	body := taskBody(taskfile(t), "test:seatbelt")
	if body == "" {
		t.Fatal("Taskfile.yml has no test:seatbelt task")
	}
	scopes = append(scopes, parseGoTestScopes("Taskfile.yml test:seatbelt", body)...)

	job := extractYAMLJob(contractcheck.ReadRepoFile(t, root, ".github/workflows/ci.yml"), "browser-confinement")
	if job == "" {
		t.Fatal(".github/workflows/ci.yml has no browser-confinement job")
	}
	scopes = append(scopes, parseGoTestScopes("ci.yml browser-confinement", job)...)

	if len(scopes) == 0 {
		t.Fatal("parsed no run scopes from the seatbelt task or the browser-confinement job")
	}
	return scopes
}
