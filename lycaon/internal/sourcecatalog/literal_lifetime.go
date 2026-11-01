package sourcecatalog

import (
	"context"
	"errors"
)

var errLiteralDrained = errors.New("content observations are drained")

func (c *literalIndexCache) seal() {
	c.fileMu.Lock()
	defer c.fileMu.Unlock()
	if c.drained {
		return
	}
	c.drained = true
	c.files = literalFileCache{}
	for read := range c.fileReads {
		read.invalidated = true
	}
	close(c.available)
	c.available = make(chan struct{})
}

func (c *literalIndexCache) join(ctx context.Context) error {
	for {
		c.fileMu.Lock()
		pending := len(c.fileReads)
		available := c.available
		c.fileMu.Unlock()
		if pending == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-available:
		}
	}
}
