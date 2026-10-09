package projectsource

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestSourceMoveRecoveryRefusesDamagedDestinationDuringCleanup(t *testing.T) {
	service, _, root, _ := sourceMutationFixture(t)
	held, destination := filepath.Join(root, "held"), filepath.Join(root, "destination")
	for _, path := range []string{held, destination} {
		testutil.FailErr(t, "create move tree", os.Mkdir(path, 0o700))
		testutil.FailErr(t, "seed complete child", os.WriteFile(filepath.Join(path, "file"), []byte("complete"), 0o600))
	}
	sha, err := sourceTreeFingerprint(t.Context(), destination)
	testutil.FailErr(t, "fingerprint complete destination", err)
	heldID, err := fspath.EntryIdentity(held)
	testutil.FailErr(t, "identify held source", err)
	destinationID, err := fspath.EntryIdentity(destination)
	testutil.FailErr(t, "identify destination", err)
	testutil.FailErr(t, "damage published destination", os.WriteFile(filepath.Join(destination, "file"), []byte("damaged"), 0o600))
	row := &sourceMutationRow{Plan: sourceMutationPlan{
		RootPath: root, FromAbs: filepath.Join(root, "source"), ToAbs: destination,
		HoldAbs: held, EntryIdentity: heldID, DestinationIdentity: destinationID,
		TreeSHA: sha, CrossVolume: true, HoldStarted: true, MoveCleanupStarted: true,
	}}
	if err := service.Effects.publishSourceMove(t.Context(), row); !errors.Is(err, ErrSourceMutationDiverged) {
		t.Fatalf("damaged destination accepted for cleanup: %v", err)
	}
	assertSourceHistoryFile(t, root, "held/file", "complete")
}

func TestSourceRestoreRefusesCorruptRecoveryBeforePublication(t *testing.T) {
	service, p, root, _ := sourceMutationFixture(t)
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(root, "file"), []byte("complete"), 0o600))
	id := uuid.NewString()
	testutil.FailErr(t, "trash source", service.Delete(t.Context(), id, p, SourceDeleteRequest{RootID: p.Roots[0].ID, Path: "file"}))
	// A missing manifest body cannot become a successful restoration.
	_, err := service.Journal.db.ExecContext(t.Context(), `UPDATE source_recovery_entries SET sha256=? WHERE recovery_id=?`, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", id)
	testutil.FailErr(t, "damage manifest reference", err)
	_, err = service.Undo(t.Context(), uuid.NewString(), p, SourceHistoryMutationRequest{ExpectedEntryID: id})
	if err == nil {
		t.Fatal("invalid recovery restored")
	}
	if _, err := os.Stat(filepath.Join(root, "file")); !os.IsNotExist(err) {
		t.Fatalf("invalid restoration published: %v", err)
	}
}

func TestSourceCapturePreservesRecordedContentExpectation(t *testing.T) {
	for _, kind := range []string{"create", "copy", "delete"} {
		t.Run(kind, func(t *testing.T) {
			service, p, root, _ := sourceMutationFixture(t)
			path := filepath.Join(root, "file")
			testutil.FailErr(t, "seed concurrent contents", os.WriteFile(path, []byte("external edit"), 0o600))
			plan := &sourceMutationPlan{
				sourceMutationAttribution: sourceMutationAttribution{ProjectID: p.ID},
				Kind:                      kind,
				RootPath:                  root,
				Path:                      "file",
				RecoveryID:                uuid.NewString(),
				EntryKind:                 SourceEntryFile,
			}
			expected := textfile.SHA256([]byte("recorded contents"))
			if kind == "delete" {
				plan.BaseSHA256 = expected
			} else {
				plan.AfterSHA = expected
			}
			if err := service.recovery.captureRecovery(t.Context(), plan, path); !errors.Is(err, ErrSourceMutationDiverged) {
				t.Fatalf("captured contents replaced recorded expectation: %v", err)
			}
			if plan.RecoveryCount != 0 {
				t.Fatal("diverged capture became complete")
			}
			assertSourceHistoryFile(t, root, "file", "external edit")
		})
	}
}
