package backup

import (
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedCaptureAllocationSeparatesSharingAndSnapshotCopy(t *testing.T) {
	const pages, pageBytes, scratch, payloadBytes = uint64(17), uint64(4096), uint64(123), uint64(99)
	database := pages * pageBytes
	for _, staged := range []bool{false, true} {
		for _, original := range []bool{false, true} {
			for _, snapshotClone := range []bool{false, true} {
				sharing := db.UpgradeSharing{OriginalClone: original, RecoveryClone: !staged && snapshotClone}
				storage, err := db.BoundUpgradeStorage(pages, pageBytes, sharing)
				testutil.FailErr(t, "db.BoundUpgradeStorage failed", err)
				reserve, err := storage.RequiredBytes(scratch)
				testutil.FailErr(t, "storage.RequiredBytes failed", err)
				needed := reserve
				if !staged {
					needed, err = retainedCaptureBytes(reserve, database+payloadBytes, database, snapshotClone)
					testutil.FailErr(t, "retainedCaptureBytes failed", err)
				}
				want := scratch + (64 << 20)
				if original || (!staged && snapshotClone) {
					want += database
				}
				if !staged {
					want += payloadBytes
					if !snapshotClone {
						want += database
					}
				}
				if needed != want {
					t.Fatalf("staged=%v original=%v snapshotClone=%v: needed%d,want%d", staged, original, snapshotClone, needed, want)
				}
			}
		}
	}
	if _, err := retainedCaptureBytes(0, 1, 2, true); err == nil {
		t.Fatal("accepted snapshot subtraction larger than payload")
	}
}
