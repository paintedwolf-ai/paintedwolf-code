package progress

import "strings"

func (c *Coalescer) FlushNow(sessionID string) {
	c.flush(strings.TrimSpace(sessionID))
}
