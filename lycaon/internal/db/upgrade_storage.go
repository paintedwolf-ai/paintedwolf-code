package db

import (
	"context"
	"fmt"
	"math"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fileclone"
)

// UpgradeStorage bounds the private replacement, temporary sorting and
// recovery clone divergence. Existing files are already charged to free space.
type UpgradeStorage struct {
	Pages, PageBytes uint64
	CloneBytes       uint64
}

// UpgradeSharing records immutable readers that share current replacement pages.
type UpgradeSharing struct{ OriginalClone, RecoveryClone bool }

// InspectUpgradeStorage measures the retained replacement and its sort work.
// staged excludes recovery clone divergence when an archive is the recovery point.
func InspectUpgradeStorage(ctx context.Context, database DBTX, path string, staged, sharedOriginal bool) (UpgradeStorage, error) {
	var pages, pageBytes uint64
	if err := database.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return UpgradeStorage{}, err
	}
	if err := database.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageBytes); err != nil {
		return UpgradeStorage{}, err
	}
	sharing := UpgradeSharing{OriginalClone: sharedOriginal}
	var err error
	if !staged {
		var supported bool
		supported, err = fileclone.SupportedAt(filepath.Dir(path))
		sharing.RecoveryClone = supported
		if err != nil {
			return UpgradeStorage{}, err
		}
	}
	return BoundUpgradeStorage(pages, pageBytes, sharing)
}

// BoundUpgradeStorage reserves divergence of a verified recovery clone.
// The disposable replacement has no journal; failed work is discarded.
func BoundUpgradeStorage(pages, pageBytes uint64, sharing UpgradeSharing) (UpgradeStorage, error) {
	out := UpgradeStorage{Pages: pages, PageBytes: pageBytes}
	if pages == 0 || pageBytes < 512 || pageBytes > 65536 || pageBytes&(pageBytes-1) != 0 || pages > math.MaxUint64/pageBytes {
		return out, fmt.Errorf("invalid SQLite storage geometry")
	}
	if sharing.OriginalClone || sharing.RecoveryClone {
		out.CloneBytes = pages * pageBytes
	}
	return out, nil
}

// RequiredBytes reserves auxiliary files before assigning any target growth.
func (s UpgradeStorage) RequiredBytes(scratch uint64) (uint64, error) {
	total := uint64(0)
	for _, value := range []uint64{s.CloneBytes, scratch, 64 << 20} {
		if value > math.MaxUint64-total {
			return 0, fmt.Errorf("upgrade storage reserve overflows")
		}
		total += value
	}
	return total, nil
}

// PageLimit lets SQLite account for actual records, indexes and pointer maps.
// It never guesses how many times snapshots will share a new chunk.
func (s UpgradeStorage) PageLimit(available, scratch uint64) (uint64, error) {
	reserve, err := s.RequiredBytes(scratch)
	if err != nil {
		return 0, err
	}
	if available < reserve {
		return 0, &InsufficientSpaceError{NeededBytes: reserve, AvailableBytes: available}
	}
	extra := (available - reserve) / s.PageBytes
	maximum := uint64(4294967294)
	if s.Pages > maximum {
		return 0, fmt.Errorf("store exceeds SQLite page limit")
	}
	return s.Pages + min(extra, maximum-s.Pages), nil
}

func storageSum(values ...uint64) (uint64, error) {
	var out uint64
	for _, value := range values {
		if value > math.MaxUint64-out {
			return 0, fmt.Errorf("upgrade storage bound overflows")
		}
		out += value
	}
	return out, nil
}
