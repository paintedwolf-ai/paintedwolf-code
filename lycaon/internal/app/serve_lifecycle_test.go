package app

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	"github.com/lycaon/lycaon/internal/resourcelifecycle"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Boot recovery runs first and structural reconcile precedes every cleanup
// that waits on the integrity audit.
func TestServeRunnerOrderPutsRecoveryBeforeMaintenance(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range serveRunnerOrder {
		if seen[name] {
			t.Fatalf("serveRunnerOrder lists %q twice", name)
		}
		seen[name] = true
	}
	if len(serveRunnerOrder) == 0 || serveRunnerOrder[0] != "boot-recovery" {
		t.Fatalf("serveRunnerOrder = %v, want boot-recovery first", serveRunnerOrder)
	}
	reconcile := slices.Index(serveRunnerOrder, "store-coupled-reconcile")
	if reconcile < 0 {
		t.Fatal("serveRunnerOrder has no structural reconcile")
	}
	maintenance := 0
	for index, name := range serveRunnerOrder {
		if name == "store-coupled-reconcile" || !cleanupRequiresIntegrity(name) {
			continue
		}
		maintenance++
		if index < reconcile {
			t.Errorf("maintenance runner %q starts before structural reconcile", name)
		}
	}
	if maintenance == 0 {
		t.Fatal("no maintenance runner waits on the integrity audit")
	}
}

func buildServeAppForTest(t *testing.T) *ServeApp {
	t.Helper()
	testutil.SkipIfShort(t, "assembles the full app graph")
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	app, err := Build(t.Context(), testBuildConfig(t, configlayout.FindModuleRoot()))
	testutil.FailErr(t, "build", err)
	t.Cleanup(func() { testutil.FailErr(t, "close", app.Close()) })
	return app
}

// A built app registers its runners in serveRunnerOrder.
func TestBuiltAppRegistersRunnersInServeOrder(t *testing.T) {
	app := buildServeAppForTest(t)
	last := -1
	for _, runner := range app.runners.runners {
		index := slices.Index(serveRunnerOrder, runner.name)
		if index <= last {
			t.Fatalf("runner %q registered out of serveRunnerOrder", runner.name)
		}
		last = index
	}
	if len(app.runners.runners) < 2 || app.runners.runners[0].name != "boot-recovery" {
		t.Fatalf("registered runners start with %+v, want boot-recovery among several", app.runners.runners)
	}
}

// Session runtime resources, which may still write durable state, close
// before the database.
func TestBuiltAppClosesDurableStateLast(t *testing.T) {
	app := buildServeAppForTest(t)
	order := app.resources.lifecycle.Tracked(resourcelifecycle.DeviceScope())
	database := slices.Index(order, "database")
	if database < 0 {
		t.Fatalf("release order %v has no database", order)
	}
	for _, name := range []string{"session-host", "held-calls", "background-processes", "llm-service", "mcp", "event-outbox", "event-publisher"} {
		index := slices.Index(order, name)
		if index < 0 {
			t.Errorf("release order has no %s (order %v)", name, order)
		} else if index > database {
			t.Errorf("%s releases after the database (order %v)", name, order)
		}
	}
}

// Boot recovery is the first startup runner of an app whose approval gate
// sealed during Build, and its serve phase settles on the wired app.
func TestBootRecoveryRunsOnTheSealedApp(t *testing.T) {
	app := buildServeAppForTest(t)
	if len(app.runners.runners) == 0 {
		t.Fatal("built app registered no runners")
	}
	first := app.runners.runners[0]
	if first.name != "boot-recovery" || first.mode != runnerStartupOnce {
		t.Fatalf("first runner = %s (mode %d), want startup-once boot-recovery", first.name, first.mode)
	}
	testutil.FailErr(t, "run serve-phase recovery", first.run(t.Context()))
}
