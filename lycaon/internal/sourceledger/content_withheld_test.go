package sourceledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
)

// AC-ER03-5: a withheld path keeps its identity in history with content not
// captured, while an ordinary path in the same root keeps its bytes.
func TestWithheldContentIsNeverRetained(t *testing.T) {
	store, ctx, _ := openLedgerOnDisk(t)
	store.SetContentWithheld(func(_ context.Context, projectID, rootID, rel string) bool {
		return projectID == "p1" && rootID == "r1" && rel == "id_ed25519"
	})
	secret := track(t, store, ctx, "id_ed25519", "private key\n")
	ordinary := track(t, store, ctx, "config", "Host example\n")

	for _, tc := range []struct {
		version  TrackedFile
		captured bool
	}{{secret, false}, {ordinary, true}} {
		row, err := store.queries.GetSourceVersion(ctx, tc.version.VersionID)
		testutil.FailErr(t, "load version", err)
		if got := row.CaptureState == "stored"; got != tc.captured {
			t.Errorf("%s: capture state %q, want captured=%v", row.Path, row.CaptureState, tc.captured)
		}
		if !tc.captured && row.CaptureReason != "content_not_captured" {
			t.Errorf("%s: capture reason %q", row.Path, row.CaptureReason)
		}
	}
	side, err := store.Comparisons.comparisonSide(ctx, "p1", secret.VersionID)
	testutil.FailErr(t, "comparison side", err)
	if side.Availability != ContentNotCaptured || side.Content != "" {
		t.Fatalf("withheld side = %+v, want content not captured", side)
	}
	if _, err := store.Content.GetSHA(sha("private key\n")); err == nil {
		t.Fatal("withheld bytes entered the content store")
	}
}

func track(t *testing.T, store *Store, ctx context.Context, rel, content string) TrackedFile {
	t.Helper()
	tracked, err := store.TrackFile(ctx, TrackInput{
		ProjectID: "p1", RootID: "r1", Path: rel, BranchID: sourcebranch.Trunk, EntryKind: EntryKindFile,
		SHA256: sha(content), Content: []byte(content), Size: int64(len(content)),
	})
	testutil.FailErr(t, "track "+rel, err)
	return tracked
}

func sha(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
