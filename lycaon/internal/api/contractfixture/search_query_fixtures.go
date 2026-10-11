package contractfixture

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
)

func FormatSearchTS(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func SeedSearchRow(t *testing.T, sqlDB db.Handle, row search.IndexRow) {
	t.Helper()
	ctx := context.Background()
	tx, err := sqlDB.BeginTx(ctx, nil)
	testutil.FailErr(t, "begin tx", err)
	defer func() { _ = tx.Rollback() }()
	store := search.NewStore()
	testutil.FailErr(t, "upsert row", store.UpsertRows(ctx, tx, []search.IndexRow{row}))
	testutil.FailErr(t, "commit tx", tx.Commit())
}
