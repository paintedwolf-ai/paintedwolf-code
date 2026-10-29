package db

import (
	"context"
	"fmt"
	"math"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fileclone"
)

// UpgradeStorage bounds the private replacement, its rollback journal and
// recovery clone divergence. Existing files are already charged to free space.
type UpgradeStorage struct {
	Pages, PageBytes, SectorBytes uint64
	JournalBytes, CloneBytes      uint64
}

// InspectUpgradeStorage measures the replacement and the VFS on its volume.
// staged excludes recovery clone divergence when an archive is the recovery point.
func InspectUpgradeStorage(ctx context.Context, database DBTX, path string, staged bool) (UpgradeStorage, error) {
	var pages, pageBytes uint64
	if err := database.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return UpgradeStorage{}, err
	}
	if err := database.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageBytes); err != nil {
		return UpgradeStorage{}, err
	}
	sector, err := probeUpgradeSector(ctx, filepath.Dir(path))
	if err != nil {
		return UpgradeStorage{}, err
	}
	cloned := false
	if !staged {
		cloned, err = fileclone.SupportedAt(filepath.Dir(path))
		if err != nil {
			return UpgradeStorage{}, err
		}
	}
	return BoundUpgradeStorage(pages, pageBytes, sector, cloned)
}

// BoundUpgradeStorage includes sector-aligned headers between cache spills.
// One original page has one main-journal before-image; new pages have none.
func BoundUpgradeStorage(pages, pageBytes, sector uint64, cloned bool) (UpgradeStorage, error) {
	out := UpgradeStorage{Pages: pages, PageBytes: pageBytes, SectorBytes: sector}
	if pages == 0 || pageBytes < 512 || pageBytes > 65536 || pageBytes&(pageBytes-1) != 0 || sector < 512 || sector > 65536 || sector&(sector-1) != 0 {
		return out, fmt.Errorf("invalid SQLite storage geometry")
	}
	record := pageBytes + 8 + 2*sector
	if pages > (math.MaxUint64-2*sector)/record {
		return out, fmt.Errorf("upgrade journal bound overflows")
	}
	out.JournalBytes = pages*record + 2*sector
	if cloned {
		if pages > math.MaxUint64/pageBytes {
			return out, fmt.Errorf("upgrade clone bound overflows")
		}
		out.CloneBytes = pages * pageBytes
	}
	return out, nil
}

// RequiredBytes reserves auxiliary files before assigning any target growth.
func (s UpgradeStorage) RequiredBytes(scratch uint64) (uint64, error) {
	total := s.JournalBytes
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
