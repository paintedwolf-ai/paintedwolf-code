package sourcefeed

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// TestCF05_F04_HostWritesAndPublicationFailureInProcess runs 200 deterministically seeded F04 instances
// (seed 20261127) verifying:
// 1. Transactional writes publish only after DeliverCommitted() and are idempotent (never 2 effects).
// 2. When publication fails in-process, echo suppression is NOT recorded so the watcher still observes the write.
// 3. Echo suppression compares bytes/mode, so a distinct outside edit immediately after a host write is NOT suppressed.
func TestCF05_F04_HostWritesAndPublicationFailureInProcess(t *testing.T) {
	dir := t.TempDir()
	sink := &cf05CaptureSink{}
	t.Cleanup(Bind(sink))

	const instances = 200
	for i := 0; i < instances; i++ {
		rng := cf05SeedRNG("20261127", "F04", i)
		filePath := filepath.Join(dir, fmt.Sprintf("f04_%04d.txt", i))
		body := []byte(fmt.Sprintf("host write payload idx=%d r=%d\n", i, rng.Int64()))
		if err := os.WriteFile(filePath, body, 0o644); err != nil {
			t.Fatalf("instance %d write file: %v", i, err)
		}

		ch := Change{
			ProjectID:     "00000000-0000-4000-8000-000000000404",
			WorkspaceID:   "ws-f04",
			WorkspaceKind: api.SourceWorkspaceKindProject,
			RootID:        "r1",
			Path:          filepath.Base(filePath),
			AbsPath:       filePath,
			Op:            api.SourceChangeOpWrite,
			Origin:        api.SourceChangeOriginUser,
		}

		switch i % 3 {
		case 0:
			// Publication fails in-process -> watcher MUST still observe (not suppressed).
			sink.err = errors.New("injected publication failure")
			err := Emit(context.Background(), ch)
			sink.err = nil
			if err == nil {
				t.Fatalf("instance %d expected publication error", i)
			}
			if isRecentHostWrite(filePath) {
				t.Fatalf("instance %d failed publication recorded echo suppression; watcher would drop change", i)
			}
			if got := sink.take(); len(got) != 0 {
				t.Fatalf("instance %d failed publication emitted %d events", i, len(got))
			}

		case 1:
			// Transactional staging: no echo suppression or wake until DeliverCommitted(), and idempotent.
			sink.err = nil
			wakesBefore := sink.wakes
			staged, err := EmitTx(context.Background(), nil, ch)
			if err != nil {
				t.Fatalf("instance %d EmitTx: %v", i, err)
			}
			if isRecentHostWrite(filePath) {
				t.Fatalf("instance %d echo suppression recorded before commit", i)
			}
			if sink.wakes != wakesBefore {
				t.Fatalf("instance %d Deliver called before commit", i)
			}
			staged.DeliverCommitted()
			staged.DeliverCommitted() // second call must be a no-op
			if sink.wakes != wakesBefore+1 {
				t.Fatalf("instance %d Deliver wakes=%d want %d", i, sink.wakes-wakesBefore, 1)
			}
			if !isRecentHostWrite(filePath) {
				t.Fatalf("instance %d committed host write was not echo-suppressed for identical bytes", i)
			}
			if got := sink.take(); len(got) != 1 || len(got[0].Changes) != 1 {
				t.Fatalf("instance %d committed tx emitted %+v, want 1 change", i, got)
			}

		case 2:
			// Succeeding host write followed immediately by distinct outside bytes -> NOT suppressed.
			sink.err = nil
			if err := Emit(context.Background(), ch); err != nil {
				t.Fatalf("instance %d Emit: %v", i, err)
			}
			if !isRecentHostWrite(filePath) {
				t.Fatalf("instance %d host write should be suppressed while bytes match", i)
			}
			if err := os.WriteFile(filePath, append(body, []byte("outside\n")...), 0o644); err != nil {
				t.Fatalf("instance %d outside edit: %v", i, err)
			}
			if isRecentHostWrite(filePath) {
				t.Fatalf("instance %d outside edit with different bytes was falsely suppressed by time window", i)
			}
			if got := sink.take(); len(got) != 1 {
				t.Fatalf("instance %d host emit count=%d want 1", i, len(got))
			}
		}
	}
}
