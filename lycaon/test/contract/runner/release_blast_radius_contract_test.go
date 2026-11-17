package contract

import (
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/localdata"
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

func TestReleaseBlastRadiusCiUpgradeCorpusJob(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	platform := contractcheck.ReadRepoFile(t, root, ".github/workflows/platform-verification.yml")
	if !strings.Contains(platform, "upgrade-corpus:") {
		t.Fatal("platform-verification.yml missing upgrade-corpus job")
	}
	if !strings.Contains(platform, "upgrade:corpus:boot") {
		t.Fatal("platform-verification.yml upgrade-corpus job must run upgrade:corpus:boot")
	}
}

func TestReleaseBlastRadiusCiSeatbeltJob(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	platform := contractcheck.ReadRepoFile(t, root, ".github/workflows/platform-verification.yml")
	if !strings.Contains(platform, "confinement:") {
		t.Fatal("platform-verification.yml missing the confinement job")
	}
	if !strings.Contains(platform, "test:seatbelt") {
		t.Fatal("platform-verification.yml confinement job must run test:seatbelt")
	}
	if !strings.Contains(platform, "runs-on: macos-15") {
		t.Fatal("platform-verification.yml must keep macos-15 runners for darwin-gated jobs")
	}
}

func TestReleaseBlastRadiusDryRunPurity(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	rel := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")
	publish := extractYAMLJob(rel, "publish-immutable")
	if !strings.Contains(publish, "if: ${{ needs.classify.outputs.publish == 'true' }}") {
		t.Fatal("immutable publication must be gated by the classified tag event")
	}
	for _, job := range []string{"update-cask", "notify-website", "activate-updater", "publish-github-release"} {
		body := extractYAMLJob(rel, job)
		if !strings.Contains(body, "publish-immutable") {
			t.Fatalf("%s must depend on the publication-gated immutable deposit", job)
		}
	}
}

func TestReleaseBlastRadiusPublishOrder(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	rel := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")
	order := []string{
		"Verify and stage platform artifacts",
		"Assemble complete updater manifest and cask",
		"Publish immutable release objects",
		"Activate release channel last",
	}
	var idxs []int
	for _, name := range order {
		i := strings.Index(rel, "name: "+name)
		if i < 0 {
			t.Fatalf("release.yml missing %q", name)
		}
		idxs = append(idxs, i)
	}
	for i := 1; i < len(idxs); i++ {
		if idxs[i] <= idxs[i-1] {
			t.Fatal("release.yml deposit order must be prior read → committed corpus → updater artifact")
		}
	}
	if !strings.Contains(extractYAMLJob(rel, "publish-artifacts"), "scripts/release_distribution.py advance --file release-metadata/latest.json") {
		t.Fatal("publication must check authenticated channel state before depositing release objects")
	}
	for _, needle := range []string{
		"activate-updater:",
		"needs: [classify, publish-immutable, update-cask, notify-website]",
		"scripts/release-r2-publish-pointer.sh",
	} {
		if !strings.Contains(rel, needle) {
			t.Fatalf("release.yml must activate the mutable updater pointer last (%q missing)", needle)
		}
	}
	if !strings.Contains(rel, "publish-github-release:") {
		t.Fatal("release.yml must keep the GitHub Release mirror job")
	}
	if strings.Contains(extractYAMLJob(rel, "activate-updater"), "publish-github-release") {
		t.Fatal("the GitHub Release mirror must not gate updater activation")
	}
	for _, needle := range []string{
		`updates/releases/${VERSION}.json`,
		`release-metadata/${VERSION}/$(basename "${cask}")`,
		"release-validate-updater-manifest.sh",
	} {
		if !strings.Contains(rel, needle) {
			t.Fatalf("release.yml must deposit a validated immutable updater manifest (%q missing)", needle)
		}
	}
}

// Upgrade rehearsal resolves the prior release through the public feed resolver
// alone: no other host is asked.
func TestUpgradeRehearsalResolvesPriorReleaseFromThePublicFeed(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	tools, build := t.TempDir(), t.TempDir()
	calls := filepath.Join(tools, "calls")
	python, err := exec.LookPath("python3")
	contractcheck.FailErr(t, "find Python", err)
	contractcheck.FailErr(t, "write HEAD sidecar", os.WriteFile(filepath.Join(build, "lycaon-dev"), []byte("#!/bin/sh\nexit 1\n"), 0o700))
	// The resolver reports no published release; every other script runs unchanged.
	shims := map[string]string{
		"python3": "#!/bin/sh\ncase \"$1\" in */release_control.py) printf 'python3 %s\\n' \"$*\" >> \"$REHEARSAL_CALLS\"; exit 0 ;; esac\nexec " + python + " \"$@\"\n",
	}
	for _, name := range []string{"curl", "sqlite3", "tar"} {
		shims[name] = "#!/bin/sh\nprintf '" + name + " %s\\n' \"$*\" >> \"$REHEARSAL_CALLS\"\nexit 1\n"
	}
	for name, body := range shims {
		contractcheck.FailErr(t, "write "+name+" shim", os.WriteFile(filepath.Join(tools, name), []byte(body), 0o700))
	}
	const feed = "https://downloads.paintedwolf.dev"
	cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts", "upgrade-rehearse.sh"))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
		"PW_BUILD_DIR="+build, "DOWNLOAD_BASE_URL="+feed, "SIGNING_GENERATION=3", "REHEARSAL_CALLS="+calls)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("upgrade rehearsal without a prior release failed: %v\n%s", err, out)
	}
	recorded, err := os.ReadFile(calls)
	contractcheck.FailErr(t, "read rehearsal calls", err)
	version := strings.TrimSpace(contractcheck.ReadRepoFile(t, root, "VERSION"))
	want := "python3 " + filepath.Join(root, "scripts", "release_control.py") + " prior --base-url " + feed +
		" --candidate " + version + " --generation 3\n"
	if string(recorded) != want {
		t.Fatalf("prior release resolution made calls %q, want only %q", recorded, want)
	}
}

func TestReleaseBlastRadiusCredentialedLiveTestIsIsolated(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	taskfile := contractcheck.ReadRepoFile(t, root, "Taskfile.yml")
	if strings.Count(taskfile, "release:live-test") != 2 {
		t.Fatal("release:live-test must exist only as its opt-in task and description")
	}
	script := contractcheck.ReadRepoFile(t, root, "scripts/release-r2-live-test.sh")
	for _, needle := range []string{
		"release-system-tests/${RUN_ID}",
		"refusing to overwrite",
		"public-domain missing object returned HTTP",
		"max-age=31536000",
		"must-revalidate",
		"immutable object accepted changed bytes",
		"ordinary updater activation accepted a SemVer regression",
		"halt compare-and-swap accepted the wrong active version",
		"cleanup left ${key}",
	} {
		if !strings.Contains(script, needle) {
			t.Fatalf("credentialed live test missing %q", needle)
		}
	}
	workflow := contractcheck.ReadRepoFile(t, root, ".github/workflows/release-system-live-test.yml")
	for _, needle := range []string{
		"schedule:",
		"environment: release-rehearsal",
		"secrets.RELEASE_TEST_R2_ACCESS_KEY_ID",
		"secrets.RELEASE_TEST_R2_SECRET_ACCESS_KEY",
		"vars.RELEASE_TEST_R2_ENDPOINT_URL",
		"run: ./task release:live-test",
		"vars.RELEASE_TEST_R2_ENDPOINT_URL",
		"secrets.RELEASE_TEST_R2_ACCESS_KEY_ID",
		"run: ./task release:rehearse:native-repositories-r2 -- --receipt",
		"./task release:rehearse:pacman-repositories-r2 -- --receipt",
	} {
		if !strings.Contains(workflow, needle) {
			t.Fatalf("weekly release live-test workflow missing %q", needle)
		}
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

func TestReleaseBlastRadiusReleaseTokenReadonly(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	t.Parallel()
	rel := contractcheck.ReadRepoFile(t, root, ".github/workflows/release.yml")
	if !regexp.MustCompile(`(?m)^permissions:\n  contents: read\n`).MatchString(rel) {
		t.Fatal("release.yml must keep top-level permissions contents: read")
	}
	macos := extractYAMLJob(rel, "build-release")
	for _, banned := range []string{"git push", "gh pr create", "pull-requests: write", "contents: write"} {
		if strings.Contains(macos, banned) {
			t.Fatalf("macos release job must not contain %q", banned)
		}
	}
	if !strings.Contains(rel, `find "lycaon/testdata/upgrade-corpus/${VERSION}" -type f`) {
		t.Fatal("release.yml must publish only the corpus committed with the tag")
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
