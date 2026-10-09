package sourceledger

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
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
	after, afterIdentity := VerificationState(ctx, ledger.Inventory, root)
	if after != before || afterIdentity != identity {
		t.Fatal("watcher activity invalidated unchanged source content")
	}
	testutil.FailErr(t, "change input", os.WriteFile(path, []byte("second revision"), 0o600))
	repochange.Advance(root)
	changed, changedIdentity := VerificationState(ctx, ledger.Inventory, root)
	if changed == "" || changed == before || changedIdentity != identity {
		t.Fatal("changed content did not produce a new revision of the same root")
	}
}

func TestVerificationStateUnavailableDoesNotInventEvidence(t *testing.T) {
	if revision, root := VerificationState(t.Context(), nil, t.TempDir()); revision != "" || root != "" {
		t.Fatal("missing source store produced passing-evidence identity")
	}
}
