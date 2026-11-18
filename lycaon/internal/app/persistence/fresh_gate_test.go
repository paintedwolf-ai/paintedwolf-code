package persistence

import (
	"context"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/version"
)

type trackedResource struct {
	order   int
	cleanup func(context.Context) error
}

// trackedResources releases what Open tracked in the order the runtime does.
type trackedResources struct{ tracked []trackedResource }

func (r *trackedResources) Track(_ string, order int, cleanup func(context.Context) error) {
	r.tracked = append(r.tracked, trackedResource{order: order, cleanup: cleanup})
}

func (r *trackedResources) SetDB(database *db.Store) { r.Track("database", 130, database.Shutdown) }

func (r *trackedResources) release(t *testing.T) {
	slices.SortStableFunc(r.tracked, func(a, b trackedResource) int { return a.order - b.order })
	for _, resource := range r.tracked {
		testutil.FailErr(t, "release runtime resource", resource.cleanup(context.Background()))
	}
}

// LYCAON_DB_FRESH wipes the selected store only on the development channel.
func TestFreshRequestWipesOnlyDevelopmentStores(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dev      string
		survives bool
	}{
		{name: "production channel", dev: "", survives: true},
		{name: "development channel", dev: "1", survives: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LYCAON_DB_FRESH", "1")
			t.Setenv(configdir.EnvConfigDir, "")
			t.Setenv("LYCAON_DEV", tc.dev)
			ctx := t.Context()
			dbPath := filepath.Join(t.TempDir(), "store.db")
			seeded := testdbfixture.OpenPath(t, dbPath)
			_, err := seeded.ExecContext(ctx, `INSERT INTO command_invocations(operation_id,input_digest,response_json,created_at) VALUES ('seeded','digest','{}','2000-01-01T00:00:00Z')`)
			testutil.FailErr(t, "seed row", err)
			testutil.FailErr(t, "stamp current application", db.New(seeded).UpsertStoreMeta(ctx, db.UpsertStoreMetaParams{Key: "app_version", Value: version.Version}))
			testutil.FailErr(t, "close seeded store", seeded.Close())

			runtime := &Runtime{}
			resources := &trackedResources{}
			testutil.FailErr(t, "open runtime storage", runtime.Open(ctx, dbPath, nil, slog.Default(), resources))
			defer resources.release(t)
			var rows int
			testutil.FailErr(t, "count seeded row", runtime.Database.QueryRowContext(ctx, `SELECT count(*) FROM command_invocations WHERE operation_id='seeded'`).Scan(&rows))
			if survived := rows == 1; survived != tc.survives {
				t.Fatalf("seeded rows = %d, want survival %t", rows, tc.survives)
			}
		})
	}
}
