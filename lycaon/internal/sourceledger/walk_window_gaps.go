package sourceledger

import (
	"context"
	"math"
	"sort"

	"github.com/lycaon/lycaon/internal/db"
)

// maxWalkGapWindows bounds the coverage-only windows one page lists.
const maxWalkGapWindows = 100

// withGapWindows adds the chat's windows in the page's ordinal span that left a
// folder uncompared. They may have recorded no change, and a chat's review
// still says what it could not see. They never move the page's cursor.
func (s *Walk) withGapWindows(ctx context.Context, projectID string, baseline Baseline, from, before int64, listed []CommandWindow) ([]CommandWindow, error) {
	if baseline.Kind != BaselineSession && baseline.Kind != BaselineTurn {
		return listed, nil
	}
	if before <= 0 {
		before = math.MaxInt64
	}
	turn := int64(0)
	if baseline.Kind == BaselineTurn {
		turn = int64(baseline.Turn)
	}
	ids, err := s.queries.ListSourceCommandWindowsWithGaps(ctx, db.ListSourceCommandWindowsWithGapsParams{
		ProjectID: projectID, SessionID: baseline.SessionID, Turn: turn,
		FromOrdinal: from, BeforeOrdinal: before, RowLimit: maxWalkGapWindows,
	})
	if err != nil || len(ids) == 0 {
		return listed, err
	}
	known := make(map[string]struct{}, len(listed))
	for _, window := range listed {
		known[window.ID] = struct{}{}
	}
	missing := ids[:0]
	for _, id := range ids {
		if _, ok := known[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return listed, nil
	}
	byID, err := s.commands.CommandWindows(ctx, missing)
	if err != nil {
		return listed, err
	}
	out := append([]CommandWindow(nil), listed...)
	for _, window := range byID {
		out = append(out, window)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ordinal > out[j].Ordinal })
	return out, nil
}
