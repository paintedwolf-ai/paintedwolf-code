package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// TestE2EScriptsTearDownVolumes requires complete end-to-end cleanup.
func TestE2EScriptsTearDownVolumes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lib := readFile(t, filepath.Join(root, "scripts", "e2e", "docker-lib.sh"))
	if !strings.Contains(lib, "down -v") {
		t.Fatal("scripts/e2e/docker-lib.sh must run docker compose down -v to remove named volumes")
	}
	if !strings.Contains(lib, "--remove-orphans") {
		t.Fatal("scripts/e2e/docker-lib.sh must pass --remove-orphans")
	}
	scripts := []string{
		filepath.Join(root, "scripts", "e2e", "docker-stack-down.sh"),
	}
	for _, path := range scripts {
		body := readFile(t, path)
		if !strings.Contains(body, "e2e_docker_teardown_project") {
			t.Fatalf("%s must call e2e_docker_teardown_project", path)
		}
	}
}

func TestE2EDenScriptsCleanupTempState(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	scripts := []string{
		filepath.Join(root, "scripts", "e2e-den.sh"),
		filepath.Join(root, "scripts", "e2e-den-desktop.sh"),
	}
	trapRE := regexp.MustCompile(`trap\s+cleanup\s+EXIT\s+INT\s+TERM`)
	for _, path := range scripts {
		body := readFile(t, path)
		if !trapRE.MatchString(body) {
			t.Fatalf("%s must trap cleanup on EXIT INT TERM", path)
		}
		if !strings.Contains(body, `rm -rf "${LYCAON_E2E_STATE_DIR}"`) {
			t.Fatalf("%s must remove LYCAON_E2E_STATE_DIR on cleanup", path)
		}
		if !strings.Contains(body, "test-results") || !strings.Contains(body, "playwright-report") {
			t.Fatalf("%s must remove Playwright output dirs on cleanup", path)
		}
	}
}

func TestE2EDockerStackPersistsProjectForTeardown(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	up := readFile(t, filepath.Join(root, "scripts", "e2e", "docker-stack-up.sh"))
	if !strings.Contains(up, "e2e_docker_persist_project") {
		t.Fatal("docker-stack-up.sh must persist compose project name for teardown")
	}
	down := readFile(t, filepath.Join(root, "scripts", "e2e", "docker-stack-down.sh"))
	if !strings.Contains(down, "e2e_docker_read_persisted_project") {
		t.Fatal("docker-stack-down.sh must read persisted compose project when env is unset")
	}
}

func TestE2EComposeVolumesAreProjectScoped(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	compose := readFile(t, filepath.Join(root, "lycaon", "test", "fixtures", "e2e", "docker-compose.e2e.yml"))
	if strings.Contains(compose, "external: true") {
		t.Fatal("docker-compose.e2e.yml must not declare external volumes — they survive compose down")
	}
}

func TestE2EComposeStagesSchemasAlongsideModule(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	compose := readFile(t, filepath.Join(root, "lycaon", "test", "fixtures", "e2e", "docker-compose.e2e.yml"))
	for _, marker := range []string{
		"source: ${LYCAON_REPO_ROOT}/schemas",
		"target: /workspace/schemas",
		"read_only: true",
	} {
		if !strings.Contains(compose, marker) {
			t.Fatalf("docker-compose.e2e.yml must stage the schema tree beside the Go module: missing %q", marker)
		}
	}
}

func TestE2ECleanupScriptExists(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "scripts", "e2e-cleanup.sh")
	body := readFile(t, path)
	if !strings.Contains(body, "e2e_docker_purge_orphan_projects") {
		t.Fatal("e2e-cleanup.sh must purge orphaned compose projects")
	}
}

func TestE2EWorkflowsAlwaysRunCleanup(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	workflows := []string{
		filepath.Join(root, ".github", "workflows", "e2e-verification.yml"),
	}
	qualification := readFile(t, filepath.Join(root, ".github", "workflows", "qualification.yml"))
	if !strings.Contains(qualification, "uses: ./.github/workflows/e2e-verification.yml") {
		t.Fatal("qualification must invoke end-to-end verification")
	}
	for _, path := range workflows {
		body := readFile(t, path)
		if !strings.Contains(body, "if: always()") {
			t.Fatalf("%s must include an always() cleanup step for E2E artifacts", path)
		}
		if !strings.Contains(body, "e2e:cleanup") {
			t.Fatalf("%s must run ./task e2e:cleanup on always()", path)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
