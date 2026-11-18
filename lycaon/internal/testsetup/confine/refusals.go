// Package confinefixture starts the runtime boundary for test fixtures.
package confinefixture

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func RequireRefusalReports(t *testing.T) {
	t.Helper()
	confine.StartRefusalWatch(t.Context(), "")
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	want := confine.WitnessKernel
	if runtime.GOOS == "linux" {
		want = confine.WitnessIncomplete
	}
	retry := time.NewTicker(50 * time.Millisecond)
	defer retry.Stop()
	for {
		boundary := &confine.Confinement{Roots: []string{root}, Network: confine.NetworkDeny}
		lease, err := confine.BindAction(boundary, confine.EgressCommand{SessionID: t.Name(), ToolName: "command"})
		testutil.FailErr(t, "bind refusal fixture", err)
		witness := lease.SettledRefusals(ctx).Witness
		lease.Close(ctx)
		if witness == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Skip("sandbox refusal reports are not readable on this host")
		case <-retry.C:
		}
	}
}
