package visual

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
)

// AssertArtifactRecord checks required record fields, a well-formed content
// hash, and that the row carries no absolute path / bytes payload.
func AssertArtifactRecord(t testing.TB, row ArtifactRecord) {
	t.Helper()
	requireNonEmpty(t, "id", row.ID)
	requireNonEmpty(t, "mime", row.Mime)
	requireNonEmpty(t, "source", row.Source)
	requireNonEmpty(t, "session_id", row.SessionID)
	requireNonEmpty(t, "root_session_id", row.RootSessionID)
	requireNonEmpty(t, "project_id", row.ProjectID)
	requireNonEmpty(t, "created_at", row.CreatedAt)
	if _, err := time.Parse(time.RFC3339, row.CreatedAt); err != nil {
		t.Fatalf("created_at %q: want RFC3339: %v", row.CreatedAt, err)
	}
	assertContentHash(t, row.ContentHash)
	assertNoPathLeak(t, row.ContentHash, row.ID, row.Caption, row.EvidenceHandle)
	switch row.Source {
	case "render", "capture", "fetch", "user", "workspace":
	default:
		t.Fatalf("source %q: want render|capture|fetch|user|workspace", row.Source)
	}
}

// LoadDurableArtifactRecords loads testdata/durable/artifact_records.json.
func LoadDurableArtifactRecords(t testing.TB) []ArtifactRecord {
	t.Helper()
	var rows []ArtifactRecord
	loadDurableJSON(t, "artifact_records.json", &rows)
	return rows
}

func loadDurableJSON(t testing.TB, name string, dest any) {
	t.Helper()
	path := filepath.Join("testdata", "durable", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
}

func requireNonEmpty(t testing.TB, field, value string) {
	t.Helper()
	if strings.TrimSpace(value) == "" {
		t.Fatalf("%s: want non-empty", field)
	}
}

func assertContentHash(t testing.TB, hash string) {
	t.Helper()
	requireNonEmpty(t, "hash", hash)
	if len(hash) != 64 {
		t.Fatalf("hash length = %d want 64 (sha256 hex)", len(hash))
	}
	if _, err := hex.DecodeString(hash); err != nil {
		t.Fatalf("hash %q: want lowercase hex: %v", hash, err)
	}
	if hash != strings.ToLower(hash) {
		t.Fatalf("hash %q: want lowercase hex", hash)
	}
}

func assertNoPathLeak(t testing.TB, values ...string) {
	t.Helper()
	for _, v := range values {
		if strings.Contains(v, "/"+settingsoverlay.DirName()+"/projects/") ||
			strings.HasPrefix(v, "/Users/") ||
			strings.HasPrefix(v, "/home/") ||
			strings.HasPrefix(v, "~/") {
			t.Fatalf("homedir/absolute path leak in %q", v)
		}
	}
}
