package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func rewriteSecondaryLeaf(t *testing.T, path, name string, rewrite func([]byte)) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open secondary-index fixture", err)
	var page, size int64
	testutil.FailErr(t, "locate secondary tree", database.QueryRow("SELECT rootpage FROM sqlite_schema WHERE name=?", name).Scan(&page))
	testutil.FailErr(t, "read secondary page geometry", database.QueryRow("PRAGMA page_size").Scan(&size))
	testutil.FailErr(t, "close secondary fixture", database.Close())
	if page < 2 || size > 65536 || size < 512 {
		t.Fatalf("unexpected index page=%d size=%d", page, size)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	testutil.FailErr(t, "open secondary bytes", err)
	defer file.Close()
	block := make([]byte, size)
	_, err = file.ReadAt(block, (page-1)*size)
	testutil.FailErr(t, "read secondary leaf", err)
	if block[0] != 0x0a {
		t.Fatal("fixture requires a single index leaf")
	}
	rewrite(block)
	_, err = file.WriteAt(block, (page-1)*size)
	testutil.FailErr(t, "omit only secondary rows", err)
}

// An empty, well-formed secondary tree exposes the primary-cursor decision.
func omitSecondaryRows(t *testing.T, path, name string) {
	t.Helper()
	rewriteSecondaryLeaf(t, path, name, func(block []byte) {
		clear(block)
		block[0] = 0x0a
		binary.BigEndian.PutUint16(block[5:7], uint16(len(block)%65536))
	})
}

func TestMigrationActivationDefersSecondaryAuditWithoutLosingItsObligation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	database, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open activation fixture", err)
	_, err = database.Exec(upgradeTestSchema + `CREATE INDEX history_body ON history(body)`)
	testutil.FailErr(t, "seed durable activation fixture", err)
	first, err := inspectSchema(t.Context(), database)
	testutil.FailErr(t, "identify activation source", err)
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "derive activation destination", err)
	statement := "ALTER TABLE history ADD COLUMN label TEXT NOT NULL DEFAULT 'preserved'; PRAGMA user_version=2"
	_, err = tx.ExecContext(t.Context(), statement)
	testutil.FailErr(t, "derive destination schema", err)
	second, err := inspectSchema(t.Context(), tx)
	testutil.FailErr(t, "identify activation destination", err)
	testutil.FailErr(t, "retain source schema", tx.Rollback())
	testutil.FailErr(t, "close sealed activation source", database.Close())
	// Keep the same number of valid cells while substituting an equal-size key.
	// quick_check may count cells, but only the full audit checks correspondence.
	rewriteSecondaryLeaf(t, path, "history_body", func(block []byte) {
		original := []byte("the original text")
		changed := []byte("the modified text")
		if len(original) != len(changed) || bytes.Count(block, original) != 1 {
			t.Fatal("fixture requires one equal-size secondary key")
		}
		idx := bytes.Index(block, original)
		if idx < 0 {
			t.Fatal("missing secondary key")
		}
		copy(block[idx:], changed)
	})
	database, err = sql.Open("sqlite", path)
	testutil.FailErr(t, "open unaudited activation source", err)
	defer database.Close()
	testutil.FailErr(t, "admit readable primary rows", validateStoreStructure(t.Context(), database))
	sum := sha256.Sum256([]byte(statement))
	step := migrations.Step{ID: "preserve-history", Source: []byte(statement), Checksum: hex.EncodeToString(sum[:]), From: first, To: second,
		Apply: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, statement)
			return err
		}, Validate: func(ctx context.Context, tx *sql.Tx) error {
			var count int
			err := tx.QueryRowContext(ctx, "SELECT count(*) FROM history NOT INDEXED WHERE body='the original text' AND label='preserved'").Scan(&count)
			if err == nil && count != 1 {
				return errors.New("migration lost retained primary history")
			}
			return err
		}}
	registry, err := migrations.New(second, []migrations.Baseline{first}, []migrations.Step{step})
	testutil.FailErr(t, "register activation route", err)
	plan, err := registry.Plan(first)
	testutil.FailErr(t, "plan activation route", err)
	testutil.FailErr(t, "activate without exhaustive secondary audit", registry.Execute(t.Context(), database, plan, inspectSchema, nil))
	testutil.FailErr(t, "validate committed migration ledger", registry.CheckLedger(t.Context(), database, second.Revision))
	actual, err := inspectSchema(t.Context(), database)
	testutil.FailErr(t, "read committed destination", err)
	if actual != second {
		t.Fatal("activation changed the destination identity")
	}
	due, err := integrityAuditNeeded(t.Context(), database)
	testutil.FailErr(t, "read durable audit obligation", err)
	if !due {
		t.Fatal("clean-shutdown source lost the deferred audit obligation")
	}
	var damage *StoreIncompatibleError
	if err := auditSnapshot(t.Context(), database, nil); !errors.As(err, &damage) || damage.Reason != RecoveryReasonIntegrityFailed {
		t.Fatalf("explicit exhaustive audit did not classify secondary damage: %v", err)
	}
}

func TestRetentionSelectionUsesPrimaryTreesAndBoundedMetadataSorts(t *testing.T) {
	path, plan := completeRetainedFixture(t)
	database, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open primary-selection fixture", err)
	var index string
	testutil.FailErr(t, "find reference secondary index", database.QueryRow(`SELECT name FROM pragma_index_list('source_snapshot_chunks') WHERE origin='u'`).Scan(&index))
	testutil.FailErr(t, "close selection fixture", database.Close())
	omitSecondaryRows(t, path, index)
	database, err = sql.Open("sqlite", path)
	testutil.FailErr(t, "open incomplete secondary coverage", err)
	defer database.Close()
	complete, err := retentionComplete(t.Context(), database, plan)
	testutil.FailErr(t, "select through physical reference primary", err)
	if !complete {
		t.Fatal("secondary-index omission changed the retained selection")
	}
	primaries := map[int64]bool{}
	for _, table := range []string{"source_manifest_chunks", "source_manifest_staging", "source_snapshot_chunks"} {
		var page int64
		testutil.FailErr(t, "locate selection primary", database.QueryRow("SELECT rootpage FROM sqlite_schema WHERE name=?", table).Scan(&page))
		primaries[page] = true
	}
	rows, err := database.QueryContext(t.Context(), "EXPLAIN "+retainedCompletenessSQL)
	testutil.FailErr(t, "inspect pinned selection program", err)
	defer rows.Close()
	grouping := 0
	for rows.Next() {
		var address, p1, p2, p3, p5 int64
		var opcode string
		var p4, comment any
		testutil.FailErr(t, "read selection instruction", rows.Scan(&address, &opcode, &p1, &p2, &p3, &p4, &p5, &comment))
		switch opcode {
		case "OpenRead":
			if !primaries[p2] {
				t.Fatalf("selection reads an unaudited secondary tree at page %d", p2)
			}
		case "SorterOpen":
			grouping++
		case "OpenWrite", "OpenAutoindex", "OpenEphemeral", "IdxInsert", "IdxDelete":
			t.Fatalf("selection introduced an unbounded write class: %s", opcode)
		}
	}
	testutil.FailErr(t, "finish selection instructions", rows.Err())
	if grouping != 3 {
		t.Fatalf("selection changed its three bounded metadata sorts: %d", grouping)
	}
}

func TestRetentionSelectionRefusesDuplicatePrimaryReferences(t *testing.T) {
	path, plan := completeRetainedFixture(t)
	database, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open duplicate-reference fixture", err)
	var index string
	testutil.FailErr(t, "find reference uniqueness index", database.QueryRow(`SELECT name FROM pragma_index_list('source_snapshot_chunks') WHERE origin='u'`).Scan(&index))
	testutil.FailErr(t, "close duplicate fixture", database.Close())
	omitSecondaryRows(t, path, index)
	database, err = sql.Open("sqlite", path)
	testutil.FailErr(t, "open omitted uniqueness fixture", err)
	defer database.Close()
	// A latent uniqueness-index omission can admit a second primary row for a
	// chunk. Resource admission must not rely on the declared constraint alone.
	_, err = database.Exec(`WITH RECURSIVE bucket(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM bucket WHERE n<255),
 chosen AS (SELECT snapshot_id,chunk_id FROM source_snapshot_chunks INDEXED BY sqlite_autoindex_source_snapshot_chunks_1 GROUP BY snapshot_id HAVING count(*)<256 LIMIT 1)
 INSERT INTO source_snapshot_chunks(snapshot_id,bucket,chunk_id)
 SELECT c.snapshot_id,b.n,c.chunk_id FROM chosen c CROSS JOIN bucket b
 WHERE NOT EXISTS(SELECT 1 FROM source_snapshot_chunks INDEXED BY sqlite_autoindex_source_snapshot_chunks_1 WHERE snapshot_id=c.snapshot_id AND bucket=b.n) LIMIT 1`)
	testutil.FailErr(t, "admit a primary duplicate through the damaged unique index", err)
	if _, err := retentionComplete(t.Context(), database, plan); !errors.Is(err, migrations.ErrIntegrity) {
		t.Fatalf("declared uniqueness silently authorized an amplified sort: %v", err)
	}
}
