package searchadmin

import (
	"errors"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/search"
)

// projectReplaceStore adapts project source reads for replace planning.
type projectReplaceStore struct {
	project *project.Project
}

func (s projectReplaceStore) ReadReplaceContent(rootID, path string) (search.ReplaceContentRead, error) {
	read, err := projectsource.ReadProjectSource(s.project, projectsource.SourceReadRequest{
		Path:   path,
		RootID: rootID,
	})
	if err != nil {
		return search.ReplaceContentRead{}, mapReplaceStoreErr(err)
	}
	return search.ReplaceContentRead{
		Content:   read.Content,
		SHA256:    read.SHA256,
		Encoding:  read.Encoding,
		Truncated: read.OverLimit,
		Binary:    read.Binary,
	}, nil
}

func mapReplaceStoreErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, projectsource.ErrSourceWriteConflict):
		return search.ErrReplaceWriteConflict
	case errors.Is(err, projectsource.ErrSourceWriteTooLarge):
		return search.ErrReplaceTooLarge
	case errors.Is(err, projectsource.ErrSourceBinary):
		return search.ErrReplaceBinary
	case errors.Is(err, projectsource.ErrSourceNotFound):
		return search.ErrReplaceNotFound
	case errors.Is(err, projectsource.ErrSourcePathDenied):
		return search.ErrReplacePathDenied
	default:
		return err
	}
}
