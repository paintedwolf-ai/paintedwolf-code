package searchadmin

import (
	"errors"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/search"
)

// projectReplaceStore adapts project source reads for replace planning.
type projectReplaceStore struct {
	project *project.Project
}

func (s projectReplaceStore) ReadReplaceContent(rootID, path string) (search.ReplaceContentRead, error) {
	read, err := project.ReadProjectSource(s.project, project.SourceReadRequest{
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
	case errors.Is(err, project.ErrSourceWriteConflict):
		return search.ErrReplaceWriteConflict
	case errors.Is(err, project.ErrSourceWriteTooLarge):
		return search.ErrReplaceTooLarge
	case errors.Is(err, project.ErrSourceBinary):
		return search.ErrReplaceBinary
	case errors.Is(err, project.ErrSourceNotFound):
		return search.ErrReplaceNotFound
	case errors.Is(err, project.ErrSourcePathDenied):
		return search.ErrReplacePathDenied
	default:
		return err
	}
}
