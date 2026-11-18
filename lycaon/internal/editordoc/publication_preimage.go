package editordoc

import (
	"errors"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/projectsource"
)

func readPublicationPreimage(root, path string) ([]byte, error) {
	return readBoundPublicationPreimage(root, path, "")
}

func readBoundPublicationPreimage(root, path, identity string) ([]byte, error) {
	var file *os.File
	var err error
	if identity != "" {
		file, err = projectsource.OpenBoundOutsideSourceFile(path, identity)
		if errors.Is(err, projectsource.ErrSourceNotFound) {
			return nil, os.ErrNotExist
		}
	} else {
		file, err = fseffect.OpenRead(fseffect.Location{Root: root, Rel: path})
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	content, err := io.ReadAll(io.LimitReader(file, projectsource.SourceReadMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > projectsource.SourceReadMaxBytes {
		return nil, projectsource.ErrSourceWriteTooLarge
	}
	return content, nil
}
