package scan

import (
	"sort"

	"github.com/lycaon/lycaon/internal/db"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
)

type seriesRuntimeKey struct {
	canonicalPath string
	scannerID     string
}

type seriesRuntimeState struct {
	consecutiveFailures         int
	unavailableReason           scancatalog.ConfigFaultReason
	unavailableDetail           string
	unavailableContractIdentity string
}

// Unavailable reports whether the series is paused due to a permanent scanner configuration fault.
func (row SeriesRow) Unavailable() bool {
	return row.UnavailableReason != "" || row.UnavailableDetail != ""
}

// ClearUnavailable resets any recorded permanent scanner configuration fault.
func (row *SeriesRow) ClearUnavailable() {
	if row == nil {
		return
	}
	row.UnavailableReason = ""
	row.UnavailableDetail = ""
	row.UnavailableContractIdentity = ""
}

func seriesFromDB(s *sqlRunStore, row db.ScanSeries) SeriesRow {
	out := seriesFromRow(row)
	applySeriesRuntimeState(s, &out)
	return out
}

func applySeriesRuntimeState(s *sqlRunStore, row *SeriesRow) {
	if s == nil || row == nil {
		return
	}
	raw, ok := s.seriesRuntimeStates.Load(seriesRuntimeKey{
		canonicalPath: row.CanonicalPath, scannerID: row.ScannerID,
	})
	if !ok {
		return
	}
	state := raw.(seriesRuntimeState)
	row.ConsecutiveFailures = state.consecutiveFailures
	row.UnavailableReason = state.unavailableReason
	row.UnavailableDetail = state.unavailableDetail
	row.UnavailableContractIdentity = state.unavailableContractIdentity
}

func storeSeriesRuntimeState(s *sqlRunStore, row SeriesRow) {
	if s == nil {
		return
	}
	key := seriesRuntimeKey{
		canonicalPath: row.CanonicalPath, scannerID: row.ScannerID,
	}
	if row.ConsecutiveFailures <= 0 && !row.Unavailable() {
		s.seriesRuntimeStates.Delete(key)
		return
	}
	s.seriesRuntimeStates.Store(key, seriesRuntimeState{
		consecutiveFailures:         max(row.ConsecutiveFailures, 0),
		unavailableReason:           row.UnavailableReason,
		unavailableDetail:           row.UnavailableDetail,
		unavailableContractIdentity: row.UnavailableContractIdentity,
	})
}

func finishDeleteSeries(s *sqlRunStore, canonicalPath, scannerID string, err error) error {
	if err == nil && s != nil {
		s.seriesRuntimeStates.Delete(seriesRuntimeKey{
			canonicalPath: canonicalPath, scannerID: scannerID,
		})
	}
	return err
}

func finishUpsertSeries(s *sqlRunStore, row SeriesRow, err error) error {
	if err == nil && s != nil {
		storeSeriesRuntimeState(s, row)
		s.SeriesChanged.Notify()
	}
	return err
}

// UnavailableSeriesPaths returns canonical project paths with at least one unavailable series.
func UnavailableSeriesPaths(s *SQLStore) []string {
	if s == nil {
		return nil
	}
	seen := make(map[string]struct{})
	s.seriesRuntimeStates.Range(func(k, v any) bool {
		key := k.(seriesRuntimeKey)
		state := v.(seriesRuntimeState)
		if state.unavailableReason != "" || state.unavailableDetail != "" {
			seen[key.canonicalPath] = struct{}{}
		}
		return true
	})
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for path := range seen {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
