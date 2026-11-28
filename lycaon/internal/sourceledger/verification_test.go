package sourceledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVerificationStateTracksContentNotWatcherNoise(t *testing.T) {
	ledger, ctx := openLedger(t)
	root := t.TempDir()
	path := filepath.Join(root, "input.txt")
	testutil.FailErr(t, "write initial input", os.WriteFile(path, []byte("first"), 0o600))
	before, identity := VerificationState(ctx, ledger.Inventory, root)
	if before == "" || identity == "" {
		t.Fatal("source state was unavailable for a stable tree")
	}
	repochange.Advance(root)
	deliverWrites(ctx, root, "input.txt")
	after, afterIdentity := VerificationState(ctx, ledger.Inventory, root)
	if after != before || afterIdentity != identity {
		t.Fatal("watcher activity invalidated unchanged source content")
	}
	testutil.FailErr(t, "change input", os.WriteFile(path, []byte("second revision"), 0o600))
	repochange.Advance(root)
	deliverWrites(ctx, root, "input.txt")
	changed, changedIdentity := VerificationState(ctx, ledger.Inventory, root)
	if changed == "" || changed == before || changedIdentity != identity {
		t.Fatal("changed content did not produce a new revision of the same root")
	}
}

func TestVerificationStateUnavailableDoesNotInventEvidence(t *testing.T) {
	if revision, root := VerificationState(t.Context(), nil, t.TempDir()); revision != "" || root != "" {
		t.Fatal("missing source store produced passing-evidence identity")
	}
	if revision, root := SettledVerificationState(t.Context(), nil, t.TempDir()); revision != "" || root != "" {
		t.Fatal("missing source store produced settled passing-evidence identity")
	}
}

func TestVerificationStateFailsClosedOnUnsettledWatcher(t *testing.T) {
	ledger, ctx := openLedger(t)
	root := t.TempDir()
	repochangetest.Watch(t, root)
	repochangetest.Acquire(t)

	path := filepath.Join(root, "gate.go")
	testutil.FailErr(t, "write initial gate.go", os.WriteFile(path, []byte("package gate\n"), 0o600))
	before, identity := SettledVerificationState(ctx, ledger.Inventory, root)
	if before == "" || identity == "" {
		t.Fatalf("initial SettledVerificationState empty: rev=%q root=%q", before, identity)
	}

	restoreDebounce := repochange.UseDebounce(ctx, 5*time.Second)
	restored := false
	restore := func() {
		if !restored {
			restored = true
			restoreDebounce()
		}
	}
	t.Cleanup(restore)

	testutil.FailErr(t, "external edit", os.WriteFile(path, []byte("package gate // changed\n"), 0o600))

	if rev, dig := SettledVerificationState(ctx, ledger.Inventory, root); rev != "" || dig != "" {
		t.Fatalf("SettledVerificationState returned stale revision during watcher lag: rev=%q root=%q", rev, dig)
	}
	if rev, dig := VerificationState(ctx, ledger.Inventory, root); rev != "" || dig != "" {
		t.Fatalf("VerificationState returned stale revision during watcher lag: rev=%q root=%q", rev, dig)
	}

	restore()
	after, afterID := SettledVerificationState(ctx, ledger.Inventory, root)
	if after == "" || after == before || afterID != identity {
		t.Fatalf("SettledVerificationState after flush = (%q, %q), want new revision on %q", after, afterID, identity)
	}
}
