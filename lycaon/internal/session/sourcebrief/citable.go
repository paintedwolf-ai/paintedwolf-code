package sourcebrief

import (
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/evidence"
)

// citableBrief gives each row a handle, in render order from firstOrdinal, and
// returns the records they cite. A commit subject stays out of the record: it is
// third-party text, and the citation says the ref moved, not what the subject claims.
func citableBrief(brief inject.SourceChangeBrief, firstOrdinal int) (inject.SourceChangeBrief, []evidence.Record, error) {
	git := append([]inject.SourceGitLine(nil), brief.Git...)
	files := append([]inject.SourceChangeFile(nil), brief.Files...)
	records := make([]evidence.Record, 0, len(git)+len(files))
	mint := func(row any) (string, error) {
		rec, err := evidence.NewSourceChangeRecord(firstOrdinal+len(records), row)
		if err != nil {
			return "", err
		}
		records = append(records, rec)
		return rec.Handle, nil
	}
	var err error
	for i := range git {
		fact := git[i]
		fact.Detail = ""
		if git[i].Handle, err = mint(fact); err != nil {
			return inject.SourceChangeBrief{}, nil, err
		}
	}
	for i := range files {
		if files[i].Handle, err = mint(files[i]); err != nil {
			return inject.SourceChangeBrief{}, nil, err
		}
	}
	brief.Git, brief.Files = git, files
	return brief, records, nil
}
