package db

import (
	"errors"
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUpgradeStorageReserveAndPageLimit(t *testing.T) {
	for _, geometry := range [][2]uint64{{512, 4096}, {4096, 512}, {65536, 65536}} {
		p := geometry[0]
		storage, err := BoundUpgradeStorage(17, p, UpgradeSharing{OriginalClone: true})
		testutil.FailErr(t, "BoundUpgradeStorage failed", err)
		want := 17*p + 123 + 64<<20
		reserve, err := storage.RequiredBytes(123)
		if err != nil || reserve != want {
			t.Fatalf("reserve=%d want %d: %v", reserve, want, err)
		}
		if limit, err := storage.PageLimit(reserve+3*p+p-1, 123); err != nil || limit != 20 {
			t.Fatalf("limit=%d: %v", limit, err)
		}
		if limit, err := storage.PageLimit(reserve, 123); err != nil || limit != 17 {
			t.Fatalf("exact reserve limit=%d: %v", limit, err)
		}
		_, err = storage.PageLimit(reserve-1, 123)
		var space *InsufficientSpaceError
		if !errors.As(err, &space) || space.NeededBytes != reserve {
			t.Fatalf("insufficient reserve: %v", err)
		}
		copied, err := BoundUpgradeStorage(17, p, UpgradeSharing{})
		if err != nil || copied.CloneBytes != 0 {
			t.Fatalf("copied=%+v: %v", copied, err)
		}
	}
}

func TestUpgradeStorageRejectsInvalidOrOverflowingGeometry(t *testing.T) {
	for _, geometry := range [][2]uint64{{0, 4096}, {1, 0}, {1, 1000}, {1, 131072}, {math.MaxUint64, 4096}} {
		if _, err := BoundUpgradeStorage(geometry[0], geometry[1], UpgradeSharing{RecoveryClone: true}); err == nil {
			t.Fatalf("accepted %v", geometry)
		}
	}
	storage, err := BoundUpgradeStorage(1, 4096, UpgradeSharing{OriginalClone: true})
	testutil.FailErr(t, "BoundUpgradeStorage failed", err)
	if _, err := storage.RequiredBytes(math.MaxUint64); err == nil {
		t.Fatal("accepted overflowing reserve")
	}
	if limit, err := storage.PageLimit(math.MaxUint64, 0); err != nil || limit != 4294967294 {
		t.Fatalf("SQLite ceiling=%d: %v", limit, err)
	}
}

func TestUpgradeSharingOwnersReserveOneRemainingDivergence(t *testing.T) {
	const pages, pageBytes, scratch = uint64(17), uint64(4096), uint64(123)
	const originalBytes = pages * pageBytes
	for _, original := range []bool{false, true} {
		for _, recovery := range []bool{false, true} {
			storage, err := BoundUpgradeStorage(pages, pageBytes, UpgradeSharing{OriginalClone: original, RecoveryClone: recovery})
			testutil.FailErr(t, "BoundUpgradeStorage failed", err)
			// With one old-file-sized budget and three new pages available, either
			// immutable owner consumes that budget; two owners never consume it twice.
			limit, err := storage.PageLimit((64<<20)+scratch+originalBytes+3*pageBytes, scratch)
			testutil.FailErr(t, "storage.PageLimit failed", err)
			want := pages + 3
			if !original && !recovery {
				want += pages
			}
			if limit != want {
				t.Fatalf("sharing original=%v recovery=%v admitted%d pages,want%d", original, recovery, limit, want)
			}
		}
	}
}

func TestUpgradeSourceFactsDoNotMasqueradeAsASQLOwner(t *testing.T) {
	source := UpgradeSource{SharedOriginal: true}
	if _, isSQL := any(source).(DBTX); isSQL {
		t.Fatal("recovery facts implicitly expose SQL ownership")
	}
}
