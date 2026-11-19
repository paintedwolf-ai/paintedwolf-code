package app

import (
	"context"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/app/boards"
	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/app/sessions"
	"github.com/lycaon/lycaon/internal/app/workerruntime"
	"github.com/lycaon/lycaon/internal/bootrecovery"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolhost"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestCoordinatorWiringPreservesExistingOrderedRecovery(t *testing.T) {
	db := testdbfixture.Open(t, "recovery.db")
	b := &serveBuilder{
		startup:  startupBootstrap{ctx: t.Context(), recovery: bootrecovery.New()},
		sessions: &sessions.Runtime{Manager: session.NewHost(store.NewSQL(db), session.Models{Limits: settings.DefaultSessionLimits()}, nil)},
		security: &security.Runtime{}, boards: &boards.Runtime{}, workerRuntime: &workerruntime.Runtime{},
	}
	b.execution.Host = &toolhost.Runtime{Registry: tools.NewDefaultRegistry(), Commands: &toolhost.CommandServices{}}
	order := []string{"tool-invocations", "command-output-publications", "agent-wait-leases"}
	var called []string
	for i, name := range order {
		entry := bootrecovery.Entry{Name: name, Phase: bootrecovery.PhaseServe, Kind: bootrecovery.KindReconcile, Run: func(context.Context) error {
			called = append(called, name)
			return nil
		}}
		if i > 0 {
			entry.After = []string{order[i-1]}
		}
		testutil.FailErr(t, "install recovery dependency", b.registerRecovery(entry))
	}
	if err := b.wireCoordinatorRuntime(); err == nil {
		t.Fatal("coordinator accepted conflicting recovery ownership")
	}
	report, err := b.startup.recovery.Run(t.Context(), bootrecovery.PhaseServe)
	testutil.FailErr(t, "run retained recovery", err)
	testutil.FailErr(t, "settle retained dependencies", report.Err())
	if !slices.Equal(called, order) {
		t.Fatalf("original recovery order = %v, want %v", called, order)
	}
}
