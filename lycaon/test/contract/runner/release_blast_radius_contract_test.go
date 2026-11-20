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
