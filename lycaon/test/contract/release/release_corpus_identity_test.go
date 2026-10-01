package contract

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/db/migrations"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestReleaseCandidateCorpusIdentity(t *testing.T) {
	root := contractcheck.RepoRoot(t)
	version := strings.TrimSpace(contractcheck.ReadRepoFile(t, root, "VERSION"))
	fixture := filepath.Join(root, "lycaon", "testdata", "upgrade-corpus", version)
	raw, err := os.ReadFile(filepath.Join(fixture, "MANIFEST.json"))
	contractcheck.FailErr(t, "read candidate corpus manifest", err)
	var manifest struct {
		AppVersion     string              `json:"app_version"`
		SchemaIdentity migrations.Baseline `json:"schema_identity"`
		StoreSHA       string              `json:"store_sha256"`
		BackupSHA      string              `json:"backup_sha256"`
		SemanticsSHA   string              `json:"semantics_sha256"`
	}
	contractcheck.FailErr(t, "decode candidate corpus manifest", json.Unmarshal(raw, &manifest))
	current, err := db.CurrentBaseline(t.Context())
	contractcheck.FailErr(t, "read current schema baseline", err)
	if manifest.AppVersion != version || manifest.SchemaIdentity != current {
		t.Fatalf("candidate corpus is stale: version %s, schema %+v; want %s, %+v; refresh the unreleased fixture", manifest.AppVersion, manifest.SchemaIdentity, version, current)
	}
	for name, want := range map[string]string{
		"store.db": manifest.StoreSHA, "backup.zip": manifest.BackupSHA, "SEMANTICS.json": manifest.SemanticsSHA,
	} {
		payload, readErr := os.ReadFile(filepath.Join(fixture, name))
		contractcheck.FailErr(t, "read candidate corpus payload", readErr)
		sum := sha256.Sum256(payload)
		if hex.EncodeToString(sum[:]) != want {
			t.Fatalf("candidate corpus checksum differs: %s", name)
		}
	}
	standalone, err := os.ReadFile(filepath.Join(fixture, "store.db"))
	contractcheck.FailErr(t, "read standalone candidate database", err)
	verifyCandidateDatabase(t, standalone)
	archive, err := zip.OpenReader(filepath.Join(fixture, "backup.zip"))
	contractcheck.FailErr(t, "open candidate backup", err)
	defer func() { _ = archive.Close() }()
	archived, err := archive.Open("store.db")
	contractcheck.FailErr(t, "open archived candidate database", err)
	payload, err := io.ReadAll(archived)
	contractcheck.FailErr(t, "read archived candidate database", err)
	contractcheck.FailErr(t, "close archived candidate database", archived.Close())
	verifyCandidateDatabase(t, payload)
}

func verifyCandidateDatabase(t *testing.T, payload []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	contractcheck.FailErr(t, "write isolated candidate database", os.WriteFile(path, payload, 0o600))
	database, err := db.OpenReadOnly(t.Context(), path)
	contractcheck.FailErr(t, "open isolated candidate database", err)
	defer func() { _ = database.Close() }()
	contractcheck.FailErr(t, "verify candidate database baseline", db.CheckBaseline(t.Context(), database))
}
