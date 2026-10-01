package contract

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func taskfile(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "Taskfile.yml"))
	contractcheck.FailErr(t, "read Taskfile.yml", err)
	return string(data)
}

func taskBody(text, name string) string {
	needle := "\n  " + name + ":\n"
	i := strings.Index(text, needle)
	if i < 0 {
		return ""
	}
	rest := text[i+1:]
	lines := strings.Split(rest, "\n")
	var b strings.Builder
	b.WriteString(lines[0])
	b.WriteByte('\n')
	for _, ln := range lines[1:] {
		if strings.HasPrefix(ln, "  ") && !strings.HasPrefix(ln, "    ") && strings.Contains(ln, ":") {
			break
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	return b.String()
}

type verificationGoRecipe struct {
	Options []string `json:"options"`
	Exclude string   `json:"exclude"`
	Bundled bool     `json:"bundled"`
}

type verificationCatalogData struct {
	Groups map[string][]string             `json:"groups"`
	Go     map[string]verificationGoRecipe `json:"go"`
	CI     map[string]struct {
		Targets  []string `json:"targets"`
		Profiles []string `json:"profiles"`
	} `json:"ci"`
}

func verificationCatalog(t *testing.T) verificationCatalogData {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(contractcheck.RepoRoot(t), "scripts", "verification-plan.json"))
	contractcheck.FailErr(t, "read verification plan", err)
	var plan verificationCatalogData
	contractcheck.FailErr(t, "decode verification plan", json.Unmarshal(data, &plan))
	return plan
}

func TestShortRunExcludesNonUnitTiers(t *testing.T) {
	t.Parallel()
	plan := verificationCatalog(t)
	if plan.Go["test:short"].Exclude != "/test/(contract|integration|security|smoke|wiring)(/|$)" {
		t.Fatal("test:short must exclude contract, external integration, security, smoke, and wiring packages")
	}
	for _, name := range []string{"test:short", "test:contract", "test:wiring"} {
		if _, ok := plan.Go[name]; !ok {
			t.Fatalf("verification plan is missing %s", name)
		}
		if !strings.Contains(taskBody(taskfile(t), name), "test-execution.py plan -- "+name) {
			t.Fatalf("%s must delegate its recipe to the verification plan", name)
		}
	}
}

func TestCheckDigestUsesTheSameShortBoundary(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "scripts", "check-summary.sh"))
	contractcheck.FailErr(t, "read scripts/check-summary.sh", err)
	for _, want := range []string{`GO_TEST_TIER="test:short"`, `GO_TEST_TIER="test:full"`, `"${ROOT}/task" "${GO_TEST_TIER}"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("check:digest must delegate to the catalog tiers; missing %s", want)
		}
	}
}

func TestIntegrationTierIsOptInOutsideReleaseRuns(t *testing.T) {
	t.Parallel()
	plan := verificationCatalog(t)
	for _, name := range []string{"test:integration", "test:full", "test:race"} {
		options := plan.Go[name].Options
		if !slices.Contains(options, "--full") {
			t.Fatalf("%s must run without -short so integration scenarios execute", name)
		}
		index := slices.Index(options, "--tags")
		if index < 0 || index+1 >= len(options) || options[index+1] != "integration" {
			t.Fatalf("%s must opt into the integration build tag", name)
		}
	}
	if slices.Contains(plan.Go["test:short"].Options, "--tags") {
		t.Fatal("test:short must not opt into integration tests")
	}
}

func TestInternalIntegrationFilesRequireTheIntegrationTag(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	internal := filepath.Join(root, "lycaon", "internal")
	var missing []string
	err := filepath.WalkDir(internal, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_integration_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(data), "//go:build integration\n") {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			missing = append(missing, rel)
		}
		return nil
	})
	contractcheck.FailErr(t, "walk internal integration tests", err)
	contractcheck.FailViolations(t, "internal integration tests must declare //go:build integration", missing)
}

func TestCheckFastGateMembership(t *testing.T) {
	t.Parallel()
	text := taskfile(t)

	fast := taskBody(text, "check-fast")
	if !strings.Contains(fast, "test-source-snapshot.sh run -- ./task check-fast:workspace") {
		t.Fatal("check-fast must run one stable source snapshot")
	}

	fastWorkspace := taskBody(text, "check-fast:workspace")
	if !strings.Contains(fastWorkspace, `test -n "${PW_SOURCE_SNAPSHOT_COMMIT:-}"`) {
		t.Fatal("check-fast:workspace must reject an unpinned source tree")
	}
	plan := verificationCatalog(t)
	want := []string{"build", "lint:fast", "den:typecheck", "den:lint", "den:test:fast", "test:short"}
	if !slices.Equal(plan.Groups["check-fast"], want) {
		t.Fatalf("check-fast stages = %v, want %v", plan.Groups["check-fast"], want)
	}
	if !strings.Contains(fastWorkspace, "test-execution.py plan -- check-fast") {
		t.Fatal("check-fast:workspace must use the declared verification plan")
	}

	fastTask := taskBody(text, "den:test:fast")
	if !strings.Contains(fastTask, "LYCAON_VITEST_FAST") {
		t.Fatal("den:test:fast must set LYCAON_VITEST_FAST")
	}

	check := taskBody(text, "check")
	if !strings.Contains(check, "test-source-snapshot.sh run -- ./task check:workspace") {
		t.Fatal("check must run one stable source snapshot")
	}

	checkWorkspace := taskBody(text, "check:workspace")
	if !strings.Contains(checkWorkspace, `test -n "${PW_SOURCE_SNAPSHOT_COMMIT:-}"`) {
		t.Fatal("check:workspace must reject an unpinned source tree")
	}
	if !slices.Contains(plan.Groups["check"], "build:cross") || !slices.Contains(plan.Groups["check"], "check:tests") {
		t.Fatal("check:workspace must run cross-build and complete tests")
	}

	if !strings.Contains(checkWorkspace, "test-execution.py plan -- check") {
		t.Fatal("check:workspace must use the declared verification plan")
	}
	for _, required := range []string{"test:runner", "test:full", "den:test", "den:test:rust"} {
		if !slices.Contains(plan.Groups["check:tests"], required) {
			t.Fatalf("check:tests must include %s", required)
		}
	}
	for _, forbidden := range []string{"den:test:fast", "test:short", "test:scanners"} {
		if slices.Contains(plan.Groups["check:tests"], forbidden) {
			t.Fatalf("check:tests must not include %s", forbidden)
		}
	}
	if !slices.Contains(plan.Groups["check:drift"], "openapi:bundle:check") {
		t.Fatal("check:drift must verify OpenAPI bundle generation")
	}
	if slices.Contains(plan.Groups["check:drift"], "openapi:diff") {
		t.Fatal("check:drift must not gate pre-v1 changes on the OpenAPI review report")
	}

	if !strings.Contains(taskBody(text, "openapi:diff"), "diff-openapi.sh") {
		t.Fatal("openapi:diff must remain available for release review")
	}
}

func TestVitestFastHasAnExplicitBoundary(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon-den", "vitest.config.ts"))
	contractcheck.FailErr(t, "read vitest.config.ts", err)
	text := string(data)
	if !strings.Contains(text, "LYCAON_VITEST_FAST") {
		t.Fatal("vitest.config.ts must branch on LYCAON_VITEST_FAST")
	}

	got, ok := parseQuotedBlock(text, "const VITEST_FAST_INCLUDE")
	if !ok {
		t.Fatal("vitest.config.ts missing VITEST_FAST_INCLUDE")
	}
	if len(got) == 0 {
		t.Fatal("fast Vitest canaries must not be empty")
	}

	seenTS, seenTSX := false, false
	for _, rel := range got {
		if !strings.HasPrefix(rel, "src/") || strings.Contains(rel, "..") || strings.ContainsAny(rel, "*?{[") || strings.Contains(rel, ".perf.test.") {
			t.Fatalf("fast canary must name an ordinary test file: %s", rel)
		}
		if _, err := os.Stat(filepath.Join(root, "lycaon-den", rel)); err != nil {
			t.Fatalf("fast canary %s: %v", rel, err)
		}
		seenTS = seenTS || strings.HasSuffix(rel, ".test.ts")
		seenTSX = seenTSX || strings.HasSuffix(rel, ".test.tsx")
	}
	if !seenTS || !seenTSX {
		t.Fatal("fast Vitest canaries must cover both model and DOM project boundaries")
	}
}

func parseQuotedBlock(src, marker string) ([]string, bool) {
	i := strings.Index(src, marker)
	if i < 0 {
		return nil, false
	}
	rest := src[i:]
	open := strings.Index(rest, "[")
	close := strings.Index(rest, "]")
	if open < 0 || close <= open {
		return nil, false
	}
	re := regexp.MustCompile(`"([^"]+)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(rest[open:close], -1) {
		out = append(out, m[1])
	}
	return out, true
}

var (
	lockScriptName      = "repo-snapshot-lock.sh"
	lockSourcesPathOnly = regexp.MustCompile(`^-\s+['"][^'"]*` + regexp.QuoteMeta(lockScriptName) + `['"]$`)
	lockHolding         = regexp.MustCompile(regexp.QuoteMeta(lockScriptName) + `"?\s+holding\b`)
)

func TestSnapshotLockCallersDeclareMode(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var violations []string
	scan := func(rel string) {
		path := filepath.Join(root, rel)
		f, err := os.Open(path)
		contractcheck.FailErr(t, "open "+rel, err)
		defer f.Close()
		sc := bufio.NewScanner(f)
		line := 0
		for sc.Scan() {
			line++
			raw := sc.Text()
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if !strings.Contains(raw, lockScriptName) {
				continue
			}
			if lockSourcesPathOnly.MatchString(trimmed) {
				continue
			}
			if strings.Contains(raw, "generate --") || strings.Contains(raw, "read --") || lockHolding.MatchString(raw) {
				continue
			}
			violations = append(violations, rel+":"+strconv.Itoa(line)+": "+trimmed)
		}
		contractcheck.FailErr(t, "scan "+rel, sc.Err())
	}

	scan("Taskfile.yml")
	entries, err := os.ReadDir(filepath.Join(root, "scripts"))
	contractcheck.FailErr(t, "read scripts/", err)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sh") {
			continue
		}
		if e.Name() == lockScriptName {
			continue
		}
		scan(filepath.Join("scripts", e.Name()))
	}
	contractcheck.FailViolations(t, "repo-snapshot-lock.sh callers must pass generate|read|holding", violations)
}

func TestSnapshotReadersUseReadWrapper(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, name := range []string{"go-test-digest.sh", "vitest-digest.sh", "lint-go.sh", "lint-vuln.sh"} {
		data, err := os.ReadFile(filepath.Join(root, "scripts", name))
		contractcheck.FailErr(t, "read "+name, err)
		text := string(data)
		if !lockHolding.MatchString(text) {
			t.Fatalf("%s must skip the lock when already holding", name)
		}
		if !strings.Contains(text, "read --") {
			t.Fatalf("%s must run under repo-snapshot-lock.sh read", name)
		}
	}
}

func TestGocacheTrimCoversCheckoutCache(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "scripts", "gocache-trim.sh"))
	contractcheck.FailErr(t, "read gocache-trim.sh", err)
	text := string(data)
	if !strings.Contains(text, `go env GOCACHE`) {
		t.Fatal("gocache-trim.sh must still trim go env GOCACHE")
	}
	if !strings.Contains(text, "checkout-cache-dir.sh") || !strings.Contains(text, "/golangci-cache-") {
		t.Fatal("gocache-trim.sh must trim diagnostic caches outside the checkout")
	}
	if !strings.Contains(text, "env -u GOCACHE") {
		t.Fatal("gocache-trim.sh must trim the default GOCACHE when the env is overridden")
	}
	// Each checkout has a separate external lint cache.
	lint, err := os.ReadFile(filepath.Join(root, "scripts", "lint-go.sh"))
	contractcheck.FailErr(t, "read lint-go.sh", err)
	if !strings.Contains(string(lint), "checkout-cache-dir.sh") {
		t.Fatal("lint-go.sh must resolve its caches through scripts/checkout-cache-dir.sh")
	}
	for _, script := range []string{text, string(lint)} {
		if strings.Contains(script, ".bin/go-build-cache") || strings.Contains(script, ".bin/golangci-cache") {
			t.Fatal("build caches must not be placed inside the checkout under .bin/")
		}
	}
}

func TestGolangciAllowsParallelRunners(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", ".golangci.yml"))
	contractcheck.FailErr(t, "read .golangci.yml", err)
	text := string(data)
	if !strings.Contains(text, "allow-parallel-runners: true") {
		t.Fatal(".golangci.yml must set allow-parallel-runners: true")
	}
	if strings.Contains(text, "allow-parallel-runners: false") {
		t.Fatal(".golangci.yml must not set allow-parallel-runners: false")
	}
}

func TestCheckDigestUsesVitestFast(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "scripts", "check-summary.sh"))
	contractcheck.FailErr(t, "read check-summary.sh", err)
	text := string(data)
	if !strings.Contains(text, "LYCAON_VITEST_FAST=1") || !strings.Contains(text, "den:test:fast") {
		t.Fatal("check:digest must run den:test:fast under LYCAON_VITEST_FAST=1")
	}
}

func TestStoreWiringDoesNotImportStockFrame(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon-den", "src", "contributions", "store-wiring.test.ts"))
	contractcheck.FailErr(t, "read store-wiring.test.ts", err)
	if strings.Contains(string(data), "stock-frame.generated") {
		t.Fatal("store-wiring.test.ts must not import stock-frame.generated.ts")
	}
}
