package contract

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/localdata"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	_ "modernc.org/sqlite"
)

var manifestRequiredKeys = []string{
	"recipe",
	"app_version",
	"schema_version",
	"project_id",
	"session_id",
	"tool_result_message_id",
	"seeded_secondary",
	"created_at",
	"schema_identity",
	"source_history",
	"worker_history",
	"artifact_id",
	"grant_id",
	"backup_sha256",
	"store_sha256",
	"semantics_sha256",
}

// releaseCandidateCorpus is the fixture this candidate ships.
func releaseCandidateCorpus(t *testing.T, root string) string {
	t.Helper()
	version := strings.TrimSpace(contractcheck.ReadRepoFile(t, root, "VERSION"))
	return filepath.Join(root, "lycaon", "testdata", "upgrade-corpus", version)
}

var releaseBlastForbiddenTouch = []string{
	"lycaon/internal/db/schema.sql",
	"lycaon/internal/db/schema_version_lock.json",
	"lycaon/internal/db/store_schema.go",
}

func TestReleaseBlastRadiusCorpusLayout(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	baseline := releaseCandidateCorpus(t, root)
	raw, err := os.ReadFile(filepath.Join(baseline, "MANIFEST.json"))
	contractcheck.FailErr(t, "read release baseline MANIFEST", err)
	var m map[string]any
	contractcheck.FailErr(t, "parse MANIFEST", json.Unmarshal(raw, &m))
	if m["recipe"] != "retained-history-v1" {
		t.Fatalf("recipe = %v want retained-history-v1", m["recipe"])
	}
	if m["seeded_secondary"] != "tool_result" {
		t.Fatalf("seeded_secondary = %v want tool_result", m["seeded_secondary"])
	}
	for _, name := range []string{"store.db", "backup.zip", "SEMANTICS.json", "app-state-v1", "project-root"} {
		if _, err := os.Stat(filepath.Join(baseline, name)); err != nil {
			t.Fatalf("release baseline missing %s: %v", name, err)
		}
	}
	// Fixtures materialize their placeholder through the shared overlay helper.
	if _, err := os.Stat(filepath.Join(baseline, "project-root", testutil.OverlayFixtureDirName)); err != nil {
		t.Fatalf("release baseline project-root/%s missing: %v", testutil.OverlayFixtureDirName, err)
	}
	if _, err := os.Stat(filepath.Join(baseline, "project-root", settingsoverlay.DirName())); err == nil {
		t.Fatalf("release baseline stores the resolved %s path — store the overlay as %s", settingsoverlay.DirName(), testutil.OverlayFixtureDirName)
	}
}

func TestReleaseBlastRadiusNoCredentialsInCorpus(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	corpus := filepath.Join(root, "lycaon", "testdata", "upgrade-corpus")
	credNames := map[string]struct{}{}
	for _, rel := range localdata.CredentialRelPaths() {
		credNames[filepath.Base(rel)] = struct{}{}
	}
	err := filepath.Walk(corpus, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		base := info.Name()
		if base == "store.db-wal" || base == "store.db-shm" || base == "store.db-journal" {
			t.Errorf("forbidden sqlite sidecar in corpus: %s", path)
		}
		if _, ok := credNames[base]; ok {
			t.Errorf("credential filename in corpus: %s", path)
		}
		if strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") {
			t.Errorf("key-like file in corpus: %s", path)
		}
		if info.Size() > 0 && info.Size() < 1<<20 {
			raw, rerr := os.ReadFile(path)
			if rerr == nil && bytesContainPrivateKeyPEM(raw) {
				t.Errorf("private key PEM in corpus: %s", path)
			}
		}
		return nil
	})
	contractcheck.FailErr(t, "walk corpus", err)
}

func TestReleaseBlastRadiusSeedBootTasks(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	taskfile := contractcheck.ReadRepoFile(t, root, "Taskfile.yml")
	for _, needle := range []string{"upgrade:corpus:seed:", "upgrade:corpus:boot:"} {
		if !strings.Contains(taskfile, needle) {
			t.Fatalf("Taskfile.yml missing %s", needle)
		}
	}
}

func TestReleaseBlastRadiusPriorReleaseAuthorityIsR2(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	script := contractcheck.ReadRepoFile(t, root, "scripts/upgrade-rehearse.sh")
	for _, needle := range []string{
		"scripts/release_control.py",
		`--generation "${SIGNING_GENERATION}"`,
		"release-validate-updater-manifest.sh",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("upgrade-rehearse.sh must use the public feed resolver (%q missing)", needle)
		}
	}
	if strings.Contains(script, "api.github.com") {
		t.Fatal("upgrade-rehearse.sh must not resolve the prior release from the GitHub API")
	}
}

func TestReleaseBlastRadiusHaltAndRecovery(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	taskfile := contractcheck.ReadRepoFile(t, root, "Taskfile.yml")
	for _, task := range []string{"release:halt:", "release:recovery:prepare:"} {
		if !strings.Contains(taskfile, task) {
			t.Fatalf("Taskfile.yml missing %s", task)
		}
	}
	script := contractcheck.ReadRepoFile(t, root, "scripts/release-halt.sh")
	for _, needle := range []string{
		"updates/releases",
		`--channel "${CHANNEL}"`,
		"active updater version is",
		"release-r2-publish-pointer.sh",
		`--from-version "${BAD_VERSION}"`,
		"installed ${BAD_VERSION} clients remain installed",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("release halt is missing safety contract %q", needle)
		}
	}
	workflow := contractcheck.ReadRepoFile(t, root, ".github/workflows/release-halt.yml")
	for _, needle := range []string{
		"environment: release-publication",
		"scripts/release_withdrawal.py prepare",
		"scripts/release_withdrawal.py apply",
		"steps.distribution_plan.outcome",
		"needs.halt.outputs.barrier == 'success'",
	} {
		if !strings.Contains(workflow, needle) {
			t.Fatalf("release halt workflow missing %q", needle)
		}
	}
	pointer := contractcheck.ReadRepoFile(t, root, "scripts/release-r2-publish-pointer.sh")
	for _, needle := range []string{
		"--from-version",
		"updater pointer changed to",
		"updater pointer may only advance",
	} {
		if !strings.Contains(pointer, needle) {
			t.Fatalf("pointer publisher missing race/monotonic guard %q", needle)
		}
	}
	preflight := contractcheck.ReadRepoFile(t, root, "scripts/release-preflight.sh")
	release := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")
	if !strings.Contains(preflight, ".release/recovery.json") ||
		!strings.Contains(release, "release-metadata/${VERSION}/recovery.json") {
		t.Fatal("recovery provenance must be validated and deposited immutably")
	}
}

func TestReleaseBlastRadiusStaticUpdaterEndpoint(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	conf := contractcheck.ReadRepoFile(t, root, "lycaon-den/src-tauri/tauri.conf.json")
	if strings.Contains(strings.ToLower(conf), "workers.dev") ||
		strings.Contains(strings.ToLower(conf), "rollout") {
		t.Fatal("tauri.conf.json must not use a Worker or rollout endpoint")
	}
	endpoint := staticUpdaterEndpoint(t, root)
	if !strings.HasSuffix(endpoint, "/updates/stable/key-1/latest.json") {
		t.Fatalf("updater endpoint is not the static stable manifest: %q", endpoint)
	}
}

func TestReleaseBlastRadiusNonInterference(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	for _, rel := range []string{
		"scripts/upgrade-corpus-seed.sh",
		"scripts/upgrade-corpus-boot.sh",
		"scripts/release-halt.sh",
		"scripts/release-r2-live-test.sh",
		"scripts/release-recovery-prepare.sh",
		".github/workflows/release.yml",
		".github/workflows/release-halt.yml",
		".github/workflows/release-system-live-test.yml",
		".github/workflows/ci.yml",
	} {
		body := contractcheck.ReadRepoFile(t, root, rel)
		for _, surface := range releaseBlastForbiddenTouch {
			for _, needle := range []string{
				"> " + surface,
				">> " + surface,
				surface + " <<",
				"tee " + surface,
			} {
				if strings.Contains(body, needle) {
					t.Fatalf("%s must not write durable surface via %q", rel, needle)
				}
			}
		}
	}
}

func TestReleaseBlastRadiusFixtureHonesty(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	baseline := releaseCandidateCorpus(t, root)
	raw, err := os.ReadFile(filepath.Join(baseline, "MANIFEST.json"))
	contractcheck.FailErr(t, "read MANIFEST", err)
	var m map[string]any
	contractcheck.FailErr(t, "parse MANIFEST", json.Unmarshal(raw, &m))
	for _, k := range manifestRequiredKeys {
		if _, ok := m[k]; !ok {
			t.Fatalf("MANIFEST missing required key %q", k)
		}
	}
	schemaWant, ok := m["schema_version"].(float64)
	if !ok {
		t.Fatalf("schema_version type %T", m["schema_version"])
	}
	toolID, _ := m["tool_result_message_id"].(string)
	// Opening the committed WAL-mode fixture in place creates -wal/-shm sidecars beside it.
	payload, err := os.ReadFile(filepath.Join(baseline, "store.db"))
	contractcheck.FailErr(t, "read store.db", err)
	dbPath := filepath.Join(t.TempDir(), "store.db")
	contractcheck.FailErr(t, "copy store.db", os.WriteFile(dbPath, payload, 0o600))
	db, err := sql.Open("sqlite", dbPath)
	contractcheck.FailErr(t, "open store.db", err)
	defer func() { _ = db.Close() }()
	var userVersion int
	contractcheck.FailErr(t, "pragma user_version", db.QueryRow(`PRAGMA user_version`).Scan(&userVersion))
	if userVersion != int(schemaWant) {
		t.Fatalf("PRAGMA user_version=%d want MANIFEST.schema_version=%d", userVersion, int(schemaWant))
	}
	var n int
	contractcheck.FailErr(t, "lookup tool_result",
		db.QueryRow(`SELECT COUNT(*) FROM messages WHERE id = ? AND role = 'tool'`, toolID).Scan(&n))
	if n != 1 {
		t.Fatalf("tool_result_message_id %s rows=%d want 1", toolID, n)
	}
}

func bytesContainPrivateKeyPEM(raw []byte) bool {
	s := string(raw)
	return strings.Contains(s, "BEGIN RSA PRIVATE KEY") ||
		strings.Contains(s, "BEGIN OPENSSH PRIVATE KEY") ||
		strings.Contains(s, "BEGIN PRIVATE KEY")
}

func extractYAMLJob(workflow, job string) string {
	marker := "  " + job + ":\n"
	start := strings.Index(workflow, "\n"+marker)
	if start >= 0 {
		start++
	} else if strings.HasPrefix(workflow, marker) {
		start = 0
	} else {
		return ""
	}
	rest := workflow[start:]
	re := regexp.MustCompile(`(?m)^  [a-zA-Z0-9_-]+:\n`)
	locs := re.FindAllStringIndex(rest, -1)
	if len(locs) < 2 {
		return rest
	}
	return rest[:locs[1][0]]
}
