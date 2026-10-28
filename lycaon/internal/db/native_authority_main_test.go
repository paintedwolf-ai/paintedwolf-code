package db

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The fixture is the unchanged schema.sql blob from main ed9b3f1596f1 and
// declared prerequisite 92f76831141c, before native authority columns exist.
func mainRevision6Store(t *testing.T, path string) *sql.DB {
	t.Helper()
	schema, err := os.ReadFile(filepath.Join("testdata", "native_authority_main6.sql"))
	testutil.FailErr(t, "read exact main schema", err)
	if got := fmt.Sprintf("%x", sha256.Sum256(schema)); got != "893484b7f4ac643ffe6e4e612b4954c7c68f4642197a952cfb05c0013c4ee790" {
		t.Fatalf("main schema fixture changed: %s", got)
	}
	source, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open main source fixture", err)
	_, err = source.ExecContext(t.Context(), string(schema))
	testutil.FailErr(t, "install exact main schema", err)
	_, err = source.ExecContext(t.Context(), `PRAGMA user_version=6; INSERT INTO people(id,role,created_at) VALUES('main-person','owner','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed main source identity", err)
	return source
}

func TestNativeAuthorityExtendsExactMainSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "main.db")
	source := mainRevision6Store(t, path)
	before, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect exact main source", err)
	testutil.FailErr(t, "close main source before registered upgrade", source.Close())
	testutil.FailErr(t, "upgrade complete registered main route", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "reopen complete registered main route", UpgradeStaged(t.Context(), path))
	source, err = sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded main target", err)
	defer source.Close()
	fresh, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fresh.db"))
	testutil.FailErr(t, "open independent fresh target", err)
	defer fresh.Close()
	testutil.FailErr(t, "install candidate fresh target", execFreshSchema(t.Context(), fresh))
	want, err := ReadShape(t.Context(), fresh)
	testutil.FailErr(t, "read complete fresh target shape", err)
	got, err := ReadShape(t.Context(), source)
	testutil.FailErr(t, "read complete upgraded main shape", err)
	if diff := want.Diff(got); len(diff) != 0 {
		t.Fatalf("main upgrade differs from complete fresh target: %v", diff)
	}
	after, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect compared candidate", err)
	if before != migrations.EffectPermitIdentity(after).From {
		t.Fatalf("registered source differs from actual main: got %+v", before)
	}
	t.Logf("verified complete main-to-candidate shape: source=%+v target=%+v", before, after)
}

func seedNativeAuthorityMainData(t *testing.T, source *sql.DB) map[string]string {
	t.Helper()
	_, err := source.ExecContext(t.Context(), `
 INSERT INTO source_snapshots(id,roots_key,merkle_sha256,file_count,total_bytes,capture_quality,created_ts) VALUES('retained-snapshot','roots','tree',1,7,'exact','2026-01-01T00:00:00Z');
 INSERT INTO source_manifests(id,entry_count,total_bytes) VALUES('retained-manifest',1,7);
 INSERT INTO source_manifest_chunks(id,content_id,entry_count,total_bytes,hashed_count) VALUES('retained-chunk','retained-content',1,7,0);
 INSERT INTO source_manifest_entries(chunk_id,path,git_oid,identity,size,mode,modified_ns) VALUES('retained-chunk','kept.txt','retained-object','index',7,384,1);
 INSERT INTO source_manifest_layout(manifest_id,first_path,chunk_id) VALUES('retained-manifest','kept.txt','retained-chunk');
 INSERT INTO source_snapshot_roots(snapshot_id,root_path,ordinal,manifest_id) VALUES('retained-snapshot','/source',0,'retained-manifest');`)
	testutil.FailErr(t, "seed current main manifest data", err)
	_, err = source.ExecContext(t.Context(), `INSERT INTO vault_unlocks(id,project_id,chat_session_id,person_id,authenticator,window_label,unlocked_at,ended_at,end_reason) VALUES('aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa','fixture-project','fixture-session','main-person','linux_user_presence','main',?,?,'away')`, presenceAuditTimestamp, presenceAuditTimestamp)
	testutil.FailErr(t, "seed current main away audit", err)
	retained := map[string]string{}
	for _, table := range []string{"source_snapshots", "source_manifests", "source_manifest_chunks", "source_manifest_entries", "source_manifest_layout", "source_snapshot_roots", "vault_unlocks"} {
		retained[table] = namespaceTableContents(t, source, table)
	}
	return retained
}

func TestNativeAuthorityUpgradesFrozenReleaseStores(t *testing.T) {
	for _, version := range []string{"1.0.0", "1.0.1", "1.0.2"} {
		t.Run(version, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", version, "store.db"))
			testutil.FailErr(t, "read immutable released store", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0o600))
			testutil.FailErr(t, "upgrade complete registered route", UpgradeStaged(t.Context(), path))
			testutil.FailErr(t, "reopen complete registered route", UpgradeStaged(t.Context(), path))
			upgraded, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "read upgraded released store", err)
			defer upgraded.Close()
			diff, err := BaselineShapeDiff(t.Context(), upgraded)
			testutil.FailErr(t, "compare released route with complete fresh target", err)
			if len(diff) != 0 {
				t.Fatalf("released route shape differs: %v", diff)
			}
			var count int
			testutil.FailErr(t, "read candidate migration ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='native-child-once-authority'`).Scan(&count))
			if count != 1 {
				t.Fatalf("native authority applied %d times", count)
			}
		})
	}
}
