package sessions

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func tb09SyntheticToken(label string) string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	return "WEA_CANARY_TB09_H2_" + label + "_" + hex.EncodeToString(buf[:])
}

// TestWEATB09_A11_H2_CredentialIndexPathLeavesRootFailsClosed is the defensive
// custody invariant for TB01 historical lead H2 (`credential index path leaves root`):
//   1. Any path attempting to leave the configured project root (relative `..`,
//      `../...`, nested traversal, or absolute outside path) fails closed with
//      `credential index path leaves root` and never reads or leaks outside bytes.
//   2. A symlink inside the project root targeting an outside credential file is
//      never followed and never leaks outside bytes.
//   3. When a mixed watcher event contains an escaping path alongside an in-root
//      `.env` update, the index fails closed (`root.ready = false`) rather than
//      silently admitting unverified state, and the next `EnsureProject` barrier
//      reconciles and restores fresh in-root credential screening evidence.
func TestWEATB09_A11_H2_CredentialIndexPathLeavesRootFailsClosed(t *testing.T) {
	ctx := t.Context()
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)

	outsideDir := t.TempDir()
	inRootCanaryV1 := tb09SyntheticToken("inroot_v1")
	inRootCanaryV2 := tb09SyntheticToken("inroot_v2")
	outsideCanary := tb09SyntheticToken("outside")

	inRootEnv := filepath.Join(root, ".env")
	outsideEnv := filepath.Join(outsideDir, ".env")
	testutil.FailErr(t, "write in-root .env", os.WriteFile(inRootEnv, []byte("INROOT_KEY="+inRootCanaryV1+"\n"), 0o600))
	testutil.FailErr(t, "write outside .env", os.WriteFile(outsideEnv, []byte("OUTSIDE_KEY="+outsideCanary+"\n"), 0o600))
	testutil.FailErr(t, "create symlink to outside .env", os.Symlink(outsideEnv, filepath.Join(root, ".env.local")))

	// Initial admission captures in-root canary and ignores the symlinked outside file.
	testutil.FailErr(t, "initial EnsureProject", files.EnsureProject(ctx, "proj-1"))
	initial := files.harvest.FileValues("proj-1")
	if len(initial) != 1 || initial[0].Secret() != inRootCanaryV1 {
		t.Fatalf("initial admission mismatch: count=%d (must contain only in-root canary, never outside symlink)", len(initial))
	}

	// Direct escape attempts into scan/refresh must all fail closed with exact error.
	credRoot := files.index.roots["proj-1\x00r1"]
	if credRoot == nil {
		t.Fatalf("expected admitted credentialRoot for proj-1/r1")
	}
	escapePaths := []string{
		"..",
		"../outside/.env",
		"nested/../../outside/.env",
		outsideEnv,
	}
	for _, escapePath := range escapePaths {
		err := files.index.refresh(ctx, credRoot, []string{escapePath})
		if err == nil || !strings.Contains(err.Error(), "credential index path leaves root") {
			t.Fatalf("refresh(%q) error = %v, want 'credential index path leaves root'", escapePath, err)
		}
	}

	// Outside canary must never have entered harvested evidence.
	for _, v := range files.harvest.AllValues() {
		if v.Secret() == outsideCanary {
			t.Fatalf("H2 breach: outside-root canary bytes entered credential harvest")
		}
	}

	// Mixed watcher event (valid in-root .env update + escaping path) fails closed,
	// marks root unready, and the next EnsureProject barrier reconciles fresh in-root evidence.
	testutil.FailErr(t, "rotate in-root .env", os.WriteFile(inRootEnv, []byte("INROOT_KEY="+inRootCanaryV2+"\n"), 0o600))
	nextEpoch := repochange.Epoch{BootID: credRoot.epoch.BootID, Value: credRoot.epoch.Value + 1}
	err := files.index.changed(ctx, repochange.Event{
		ProjectDir:    root,
		Kind:          repochange.WorktreeChanged,
		Paths:         []string{".env", "../escape/.env"},
		EpochBootID:   nextEpoch.BootID,
		WorktreeEpoch: nextEpoch.Value,
	})
	if err == nil || !strings.Contains(err.Error(), "credential index path leaves root") {
		t.Fatalf("mixed escape watcher event error = %v, want 'credential index path leaves root'", err)
	}
	if credRoot.ready {
		t.Fatalf("expected credRoot.ready == false after failed escape refresh so admission barrier reconciles")
	}

	// Next admission barrier reconciles the unready root and captures the rotated in-root canary.
	testutil.FailErr(t, "post-escape EnsureProject reconciliation", files.EnsureProject(ctx, "proj-1"))
	sawV2 := false
	for _, v := range files.harvest.FileValues("proj-1") {
		if v.Secret() == outsideCanary {
			t.Fatalf("H2 breach after reconciliation: outside-root canary entered harvest")
		}
		if v.Secret() == inRootCanaryV2 && !v.Retired {
			sawV2 = true
		}
	}
	if !sawV2 {
		t.Fatalf("EnsureProject did not reconcile rotated in-root credential evidence after escape refusal")
	}
}
