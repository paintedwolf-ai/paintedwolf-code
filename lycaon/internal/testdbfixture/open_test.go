package testdbfixture_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOpenReturnsPreparedIsolatedStores(t *testing.T) {
	first := testdbfixture.Open(t, "first.db")
	second := testdbfixture.Open(t, "second.db")

	for label, store := range map[string]interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	}{"first": first, "second": second} {
		var one int
		if err := store.QueryRowContext(t.Context(), "SELECT 1").Scan(&one); err != nil {
			testutil.FailErr(t, "query "+label+" test database", err)
		}
		if one != 1 {
			t.Fatalf("query %s test database = %d, want 1", label, one)
		}
	}
}
