package sourcecatalog

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"math"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// Checkpoint remapping and graph validation use a private temporary database.
type structuralCheckpointWorkspace struct {
	db   *sql.DB
	tx   *sql.Tx
	next int64
}

func newStructuralCheckpointWorkspace(ctx context.Context) (*structuralCheckpointWorkspace, error) {
	db, err := sql.Open("sqlite", "")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, err = db.ExecContext(ctx, `
PRAGMA cache_size=-2048;
PRAGMA temp_store=FILE;
PRAGMA journal_mode=OFF;
PRAGMA synchronous=OFF;
CREATE TABLE page_remap (
 source BLOB PRIMARY KEY, local INTEGER NOT NULL, target BLOB,
 visiting INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE page_facts (
 local INTEGER PRIMARY KEY, target BLOB NOT NULL,
 first_key BLOB NOT NULL, last_key BLOB NOT NULL, parent BLOB NOT NULL,
 weight INTEGER NOT NULL, item_count INTEGER NOT NULL, unresolved INTEGER NOT NULL,
 fingerprint BLOB NOT NULL, baseline_fingerprint BLOB NOT NULL,
 references_count INTEGER NOT NULL, height INTEGER NOT NULL
);`)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return &structuralCheckpointWorkspace{db: db, tx: tx}, nil
}

func (workspace *structuralCheckpointWorkspace) close() error {
	return errors.Join(workspace.tx.Rollback(), workspace.db.Close())
}

func structuralCheckpointPageKey(id uint64) []byte {
	var key [8]byte
	binary.LittleEndian.PutUint64(key[:], id)
	return key[:]
}

func structuralCheckpointLocalID(value int64) (uint64, error) {
	if value <= 0 {
		return 0, pagedview.ErrRange
	}
	return uint64(value), nil
}

// beginEncode returns an existing completed mapping or marks source visiting.
func (workspace *structuralCheckpointWorkspace) beginEncode(ctx context.Context, source uint64) (local uint64, found bool, err error) {
	var stored int64
	var visiting bool
	err = workspace.tx.QueryRowContext(ctx, "SELECT local,visiting FROM page_remap WHERE source=?", structuralCheckpointPageKey(source)).Scan(&stored, &visiting)
	if err == nil {
		if visiting || stored <= 0 {
			return 0, false, pagedview.ErrRange
		}
		return uint64(stored), true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	_, err = workspace.tx.ExecContext(ctx, "INSERT INTO page_remap(source,local,target,visiting) VALUES(?,0,NULL,1)", structuralCheckpointPageKey(source))
	return 0, false, err
}

func (workspace *structuralCheckpointWorkspace) beginCopy(ctx context.Context, source uint64) (target uint64, found bool, err error) {
	var stored []byte
	var visiting bool
	err = workspace.tx.QueryRowContext(ctx, "SELECT target,visiting FROM page_remap WHERE source=?", structuralCheckpointPageKey(source)).Scan(&stored, &visiting)
	if err == nil {
		if visiting || len(stored) != 8 {
			return 0, false, pagedview.ErrRange
		}
		return binary.LittleEndian.Uint64(stored), true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, err
	}
	_, err = workspace.tx.ExecContext(ctx, "INSERT INTO page_remap(source,local,target,visiting) VALUES(?,0,NULL,1)", structuralCheckpointPageKey(source))
	return 0, false, err
}

func (workspace *structuralCheckpointWorkspace) finishCopy(ctx context.Context, source, target uint64) error {
	result, err := workspace.tx.ExecContext(ctx, "UPDATE page_remap SET target=?,visiting=0 WHERE source=? AND visiting=1", structuralCheckpointPageKey(target), structuralCheckpointPageKey(source))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return pagedview.ErrRange
	}
	return nil
}

func (workspace *structuralCheckpointWorkspace) finishEncode(ctx context.Context, source uint64) (uint64, error) {
	if workspace.next == math.MaxInt64 {
		return 0, pagedview.ErrBudget
	}
	workspace.next++
	result, err := workspace.tx.ExecContext(ctx, "UPDATE page_remap SET local=?,visiting=0 WHERE source=? AND visiting=1", workspace.next, structuralCheckpointPageKey(source))
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if changed != 1 {
		return 0, pagedview.ErrRange
	}
	return structuralCheckpointLocalID(workspace.next)
}

func (workspace *structuralCheckpointWorkspace) addFact(ctx context.Context, facts structuralCheckpointPage) (uint64, error) {
	if workspace.next == math.MaxInt64 {
		return 0, pagedview.ErrBudget
	}
	workspace.next++
	_, err := workspace.tx.ExecContext(ctx, `INSERT INTO page_facts(
 local,target,first_key,last_key,parent,weight,item_count,unresolved,fingerprint,baseline_fingerprint,references_count,height)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, workspace.next, structuralCheckpointPageKey(facts.branch.Page), []byte(facts.branch.Key), []byte(facts.last), []byte(facts.parent),
		facts.branch.Weight, facts.branch.Count, facts.branch.Unresolved, facts.branch.Fingerprint[:], facts.branch.BaselineFingerprint[:], facts.references, facts.height)
	if err != nil {
		return 0, err
	}
	return structuralCheckpointLocalID(workspace.next)
}

func (workspace *structuralCheckpointWorkspace) fact(ctx context.Context, local uint64) (structuralCheckpointPage, error) {
	var facts structuralCheckpointPage
	var target, first, last, parent, fingerprint, baseline []byte
	var references int
	if local == 0 || local > math.MaxInt64 {
		return facts, pagedview.ErrRange
	}
	err := workspace.tx.QueryRowContext(ctx, `SELECT target,first_key,last_key,parent,weight,item_count,unresolved,
 fingerprint,baseline_fingerprint,references_count,height FROM page_facts WHERE local=?`, int64(local)).
		Scan(&target, &first, &last, &parent, &facts.branch.Weight, &facts.branch.Count, &facts.branch.Unresolved,
			&fingerprint, &baseline, &references, &facts.height)
	if errors.Is(err, sql.ErrNoRows) {
		return facts, pagedview.ErrRange
	}
	if err != nil || len(target) != 8 || len(fingerprint) != len(facts.branch.Fingerprint) || len(baseline) != len(facts.branch.BaselineFingerprint) || references < 0 || references > math.MaxUint8 {
		if err != nil {
			return facts, err
		}
		return facts, pagedview.ErrRange
	}
	facts.branch.Page = binary.LittleEndian.Uint64(target)
	facts.branch.Key, facts.last, facts.parent = string(first), string(last), string(parent)
	copy(facts.branch.Fingerprint[:], fingerprint)
	copy(facts.branch.BaselineFingerprint[:], baseline)
	facts.references = uint8(references)
	return facts, nil
}

func (workspace *structuralCheckpointWorkspace) reference(ctx context.Context, local uint64) error {
	if local == 0 || local > math.MaxInt64 {
		return pagedview.ErrRange
	}
	result, err := workspace.tx.ExecContext(ctx, "UPDATE page_facts SET references_count=1 WHERE local=? AND references_count=0", int64(local))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return pagedview.ErrRange
	}
	return nil
}

func (workspace *structuralCheckpointWorkspace) allReferenced(ctx context.Context) error {
	var unused int
	err := workspace.tx.QueryRowContext(ctx, "SELECT 1 FROM page_facts WHERE references_count<>1 LIMIT 1").Scan(&unused)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return pagedview.ErrRange
}
