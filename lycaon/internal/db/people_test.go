package db

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFreshStoreSeedsExactlyOneImmutableOwner(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	owner, err := New(sqlDB).GetHostOwner(t.Context())
	testutil.FailErr(t, "read host owner", err)
	if _, err := uuid.Parse(owner.ID); err != nil || owner.Role != hostOwnerRole {
		t.Fatalf("owner = %+v", owner)
	}
	var people int
	testutil.FailErr(t, "count people", sqlDB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM people`).Scan(&people))
	if people != 1 {
		t.Fatalf("people = %d, want 1", people)
	}

	_, err = sqlDB.ExecContext(t.Context(), `INSERT INTO people (id, role, created_at) VALUES (?, 'owner', '2026-01-01T00:00:00Z')`, uuid.NewString())
	assertConstraintContains(t, err, "UNIQUE")
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE people SET role = 'owner' WHERE id = ?`, owner.ID)
	assertConstraintContains(t, err, "people rows are immutable")
	_, err = sqlDB.ExecContext(t.Context(), `DELETE FROM people WHERE id = ?`, owner.ID)
	assertConstraintContains(t, err, "the host owner cannot be removed")
}

func TestSessionOwnershipIsSharedByChildrenAndImmutable(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "store.db"))
	testutil.FailErr(t, "open", err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "parent", testdbseed.DefaultProjectID)
	_, err = sqlDB.ExecContext(t.Context(), `
		INSERT INTO sessions (id, project_id, owner_person_id, posture, status, parent_session_id, created_at, activity_at, updated_at)
		VALUES ('child', ?, 'someone-else', 'build', 'idle', 'parent', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
		testdbseed.DefaultProjectID)
	assertConstraintContains(t, err, "a child session shares its parent's owner")
	_, err = sqlDB.ExecContext(t.Context(), `UPDATE sessions SET owner_person_id = owner_person_id WHERE id = 'parent'`)
	assertConstraintContains(t, err, "session ownership is immutable")
}
