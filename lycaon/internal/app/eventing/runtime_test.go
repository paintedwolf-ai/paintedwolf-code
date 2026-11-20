package eventing

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

type closerList struct{ closers []func(context.Context) error }

func (c *closerList) Track(_ string, _ int, fn func(context.Context) error) {
	c.closers = append(c.closers, fn)
}

func (c *closerList) close(t *testing.T) {
	t.Helper()
	for _, fn := range slices.Backward(c.closers) {
		testutil.FailErr(t, "release event runtime resource", fn(context.Background()))
	}
}

func TestBuildWiresTheOutboxIntoStoresOnlyWithADatabase(t *testing.T) {
	database := testdbfixture.Open(t, "store.db")

	t.Run("no database", func(t *testing.T) {
		sessions, projects := store.NewSQL(database), project.NewSQLRegistry(database)
		resources := &closerList{}
		runtime, err := Build(t.Context(), nil, sessions, projects, nil, nil, nil, nil, resources, nil)
		if err == nil || runtime != nil {
			t.Fatalf("Build without a database = (%v, %v), want refusal", runtime, err)
		}
		if sessions.MutationEventsOutboxed() || projects.MutationEventsOutboxed() {
			t.Fatal("refused build wired an outbox into a store")
		}
		if len(resources.closers) != 0 {
			t.Fatalf("refused build tracked %d resources", len(resources.closers))
		}
	})

	t.Run("database", func(t *testing.T) {
		sessions, projects := store.NewSQL(database), project.NewSQLRegistry(database)
		resources := &closerList{}
		runtime, err := Build(t.Context(), database, sessions, projects, nil, nil, nil, nil, resources, nil)
		testutil.FailErr(t, "build event runtime", err)
		t.Cleanup(func() { resources.close(t) })
		if runtime.Outbox == nil || !sessions.MutationEventsOutboxed() || !projects.MutationEventsOutboxed() {
			t.Fatal("built runtime did not wire its outbox into both stores")
		}
	})
}
