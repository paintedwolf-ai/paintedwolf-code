package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type schemaSQLLock struct {
	Statements []string `json:"statements"`
}

var schemaStmtSplitRE = regexp.MustCompile(`(?m);[\s]*(?:\n|$)`)

func TestSchemaSQLLock(t *testing.T) {
	t.Parallel()
	if configdir.IsDevelopmentBuild() && os.Getenv("UPDATE_SCHEMA_LOCK") != "1" && os.Getenv("PW_RELEASE_QUALIFICATION") != "1" && os.Getenv("PW_RELEASE_VERIFY") != "1" {
		t.Skip("schema.sql statement lock is a release qualification gate; skipped during development and non-release PR verification")
	}

	root := contractcheck.RepoRoot(t)
	schemaPath := filepath.Join(root, "lycaon", "internal", "db", "schema.sql")
	lockPath := filepath.Join(root, "lycaon", "internal", "db", "schema.sql.lock.json")

	schemaData, err := os.ReadFile(schemaPath)
	contractcheck.FailErr(t, "read schema.sql", err)

	current := hashSchemaStatements(string(schemaData))
	if os.Getenv("UPDATE_SCHEMA_LOCK") == "1" {
		writeSchemaSQLLock(t, lockPath, current)
		return
	}

	lockData, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("missing %s (run UPDATE_SCHEMA_LOCK=1 ./task test:digest -- ./test/contract/persistence -run TestSchemaSQLLock): %v", lockPath, err)
	}
	var lock schemaSQLLock
	contractcheck.FailErr(t, "parse schema.sql.lock.json", json.Unmarshal(lockData, &lock))

	if len(lock.Statements) == 0 {
		t.Fatal("schema.sql.lock.json has no statement hashes")
	}
	if len(current) < len(lock.Statements) {
		t.Fatalf("schema.sql has fewer statements than lock (%d vs %d); refresh the lock for intentional baseline changes",
			len(current), len(lock.Statements))
	}
	for i, want := range lock.Statements {
		if current[i] != want {
			t.Fatalf("schema statement %d hash changed\nwas: %s\nnow: %s\nre-run UPDATE_SCHEMA_LOCK=1 only when intentionally editing schema.sql",
				i, want, current[i])
		}
	}
	if len(current) > len(lock.Statements) {
		t.Fatalf("schema.sql has %d new statement(s) not in lock — run UPDATE_SCHEMA_LOCK=1 to record hashes: new tail starts at index %d",
			len(current)-len(lock.Statements), len(lock.Statements))
	}
}

func hashSchemaStatements(sql string) []string {
	chunks := schemaStmtSplitRE.Split(strings.TrimSpace(sql), -1)
	var out []string
	for _, chunk := range chunks {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		if isCommentOnlyChunk(chunk) {
			continue
		}
		norm := normalizeSQLChunk(chunk)
		sum := sha256.Sum256([]byte(norm))
		out = append(out, hex.EncodeToString(sum[:]))
	}
	return out
}

func isCommentOnlyChunk(chunk string) bool {
	for _, line := range strings.Split(chunk, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "--") {
			return false
		}
	}
	return true
}

func normalizeSQLChunk(chunk string) string {
	var lines []string
	for _, line := range strings.Split(chunk, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func writeSchemaSQLLock(t *testing.T, path string, hashes []string) {
	t.Helper()
	payload, err := json.MarshalIndent(schemaSQLLock{Statements: hashes}, "", "  ")
	contractcheck.FailErr(t, "marshal schema lock", err)
	payload = append(payload, '\n')
	contractcheck.FailErr(t, "write schema lock", os.WriteFile(path, payload, 0o644))
}
