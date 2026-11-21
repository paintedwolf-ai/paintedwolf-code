package bialy

import (
	"time"
)

// SetClockForTest replaces the clock the relaunch hold is measured on.
func (c *Client) SetClockForTest(now func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}
