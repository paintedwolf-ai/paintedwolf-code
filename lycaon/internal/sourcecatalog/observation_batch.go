package sourcecatalog

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/backgroundwork"
)

const DirectoryBatchLimit = 1024

type directoryDiscovery struct {
	nodes       []indexNode
	observation DirectoryObservation
}

// Foreground directories share observations independently of the recursive scanner.
func (c *Catalog) ObserveDirectories(ctx context.Context, project string, root Root, dirs []string, priority backgroundwork.Priority) error {
	if len(dirs) > DirectoryBatchLimit {
		return errors.New("directory observation batch exceeds limit")
	}
	for _, dir := range dirs {
		observation, err := c.ObserveDirectory(ctx, project, root, dir, DirectoryRead{Priority: priority})
		if err != nil && observation.Failure == "" {
			return err
		}
	}
	return nil
}
