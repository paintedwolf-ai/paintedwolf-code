package layeredfs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func newBoundedBarrierCore(t *testing.T) (*Core, chan time.Time) {
	t.Helper()
	tmp := t.TempDir()
	trunk, backing := filepath.Join(tmp, "trunk"), filepath.Join(tmp, "backing")
	for _, dir := range []string{trunk, backing} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(dir, 0o755))
	}
	expiry := make(chan time.Time, 1)
	core, err := New(Config{
		ViewID: "view-bound", BackingDir: backing,
		Roots:      []RootSpec{{ID: "primary", Label: "primary", TrunkPath: trunk, IsPrimary: true}},
		FaultHooks: &FaultHooks{BarrierExpiry: func() <-chan time.Time { return expiry }},
	})
	testutil.FailErr(t, "New Core", err)
	t.Cleanup(func() { _ = core.ReleaseView() })
	return core, expiry
}

// A barrier held past its bound lets writes through again, and its holder learns it did not hold.
func TestBarrierPastItsBoundAdmitsWritesAndFailsItsRelease(t *testing.T) {
	t.Parallel()
	core, expiry := newBoundedBarrierCore(t)
	token, err := core.BeginCheckpointBarrier(t.Context())
	testutil.FailErr(t, "begin", err)
	qp := QualifiedPath{RootID: "primary", Rel: "held.txt"}
	if _, err := core.WriteFile(qp, []byte("x"), 0o644); !errors.Is(err, ErrBarrierActive) {
		t.Fatalf("a write under the barrier = %v, want it held", err)
	}

	expiry <- time.Time{}
	waitFor(t, func() bool { return core.Stats().BarrierExpiries == 1 })
	if _, err := core.WriteFile(qp, []byte("x"), 0o644); err != nil {
		t.Fatalf("a write after the bound passed: %v", err)
	}
	if err := core.EndCheckpointBarrier(token); !errors.Is(err, ErrBarrierExpired) {
		t.Fatalf("release after the bound = %v, want ErrBarrierExpired", err)
	}
}

// A barrier released within its bound is unaffected by it.
func TestBarrierReleasedInTimeIsNotExpired(t *testing.T) {
	t.Parallel()
	core, expiry := newBoundedBarrierCore(t)
	token, err := core.BeginCheckpointBarrier(t.Context())
	testutil.FailErr(t, "begin", err)
	testutil.FailErr(t, "release", core.EndCheckpointBarrier(token))
	expiry <- time.Time{}
	if got := core.Stats().BarrierExpiries; got != 0 {
		t.Fatalf("a released barrier expired %d times", got)
	}
	if core.BarrierHoldBound() != DefaultBarrierHoldBound {
		t.Fatalf("bound = %s", core.BarrierHoldBound())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline, ok := t.Deadline()
	for !cond() {
		if ok && time.Now().After(deadline.Add(-5*time.Second)) {
			t.Fatal("condition never held")
		}
		time.Sleep(time.Millisecond)
	}
}
