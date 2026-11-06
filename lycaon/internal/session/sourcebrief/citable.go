package sourcebrief

import (
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/evidence"
)

// citableBrief gives each row a handle, in render order from firstOrdinal, and
// returns the records they cite.
func citableBrief(brief inject.SourceChangeBrief, firstOrdinal int) (inject.SourceChangeBrief, []evidence.Record) {
	git := append([]inject.SourceGitLine(nil), brief.Git...)
	files := append([]inject.SourceChangeFile(nil), brief.Files...)
	records := make([]evidence.Record, 0, len(git)+len(files))
	mint := func() string {
		rec := evidence.NewSourceChangeRecord(firstOrdinal + len(records))
		records = append(records, rec)
		return rec.Handle
	}
	for i := range git {
		git[i].Handle = mint()
	}
	for i := range files {
		files[i].Handle = mint()
	}
	brief.Git, brief.Files = git, files
	return brief, records
}
