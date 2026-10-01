package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestEverySchemaTableDeclaresItsLifetime(t *testing.T) {
	t.Parallel()
	store := testdbfixture.Open(t, "store.db")

	cascadeParents := liveCascadeParents(t, store)
	lifetimes := db.TableLifetimes()
	var violations []string
	for table, parents := range cascadeParents {
		lifetime, ok := lifetimes[table]
		if !ok {
			violations = append(violations, table+": no entry in db.TableLifetimes")
			continue
		}
		switch lifetime.Class {
		case db.LifetimeCascadeOwned:
			if !parents[lifetime.Owner] {
				violations = append(violations, table+": no ON DELETE CASCADE foreign key to owner "+lifetime.Owner)
			}
		case db.LifetimeForever, db.LifetimeReceiptWithRetention, db.LifetimeProjection, db.LifetimeCache:
			if lifetime.Reason == "" || lifetime.Owner != "" {
				violations = append(violations, table+": "+string(lifetime.Class)+" needs a reason and no owner")
			}
		default:
			violations = append(violations, table+": unknown lifetime class "+string(lifetime.Class))
		}
	}
	for table := range lifetimes {
		if _, ok := cascadeParents[table]; !ok {
			violations = append(violations, table+": in db.TableLifetimes but not in the schema")
		}
	}
	contractcheck.FailViolations(t, "table lifetime violations", violations)
}

// liveCascadeParents maps every ordinary table in the opened store to the
// parent tables its ON DELETE CASCADE foreign keys reference.
func liveCascadeParents(t *testing.T, store *db.Store) map[string]map[string]bool {
	t.Helper()
	rows, err := store.QueryContext(t.Context(), `
		SELECT tl.name, COALESCE(fk."table", '')
		FROM pragma_table_list AS tl
		LEFT JOIN pragma_foreign_key_list(tl.name) AS fk ON fk.on_delete = 'CASCADE'
		WHERE tl.schema = 'main' AND tl.type = 'table' AND tl.name NOT LIKE 'sqlite_%'
	`)
	contractcheck.FailErr(t, "list tables and cascade parents", err)
	defer func() { _ = rows.Close() }()
	out := make(map[string]map[string]bool)
	for rows.Next() {
		var table, parent string
		contractcheck.FailErr(t, "scan table and cascade parent", rows.Scan(&table, &parent))
		if out[table] == nil {
			out[table] = make(map[string]bool)
		}
		if parent != "" {
			out[table][parent] = true
		}
	}
	contractcheck.FailErr(t, "iterate tables and cascade parents", rows.Err())
	if len(out) == 0 {
		t.Fatal("opened store has no tables")
	}
	return out
}
