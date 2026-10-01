// Package board formats pack board snapshots with character budgets.
package board

import (
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/packboard"
	"github.com/lycaon/lycaon/pkg/api"
)

// CompactFormatter renders deterministic board orientation lines within char budgets.
type CompactFormatter struct{}

// FormatOpts controls line assembly for inject vs tool output.
type FormatOpts struct {
	MaxChars          int
	Inject            bool
	OmitDelegation    bool
	Now               time.Time
	TruncatedSections *[]string
}

// FormatInject renders the coordinator inject block (≤320 chars) with sentinel.
func (f *CompactFormatter) FormatInject(snapshot api.BoardSnapshot, omitDelegation bool, now time.Time) (string, bool, []string) {
	var truncated []string
	text := f.FormatWithOpts(snapshot, FormatOpts{
		MaxChars:          api.MaxBoardInjectChars,
		Inject:            true,
		OmitDelegation:    omitDelegation,
		Now:               now,
		TruncatedSections: &truncated,
	})
	return text, len(truncated) > 0, truncated
}

// FormatWithOpts assembles lines and truncates bottom-up when over budget.
func (f *CompactFormatter) FormatWithOpts(snapshot api.BoardSnapshot, opts FormatOpts) string {
	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	max := opts.MaxChars
	if max <= 0 {
		max = api.MaxBoardInjectChars
	}
	includeDetail := snapshot.DetailLevel == api.BoardDetailLevelFull
	lines := packboard.BuildOrientationLines(snapshot, packboard.OrientOpts{
		OmitDelegation: opts.OmitDelegation,
		Now:            now,
		IncludeDetail:  includeDetail,
	})
	lines, _ = packboard.FitOrientationBudget(lines, max, opts.TruncatedSections)
	body := strings.Join(lines, "\n")
	if opts.Inject {
		return packboard.PackBoardSentinel + "\n" + body
	}
	return body
}
