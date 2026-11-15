package split

import "sync"

type core struct{}

func (c *core) run() {}

type plain struct{ n int }

type promotion struct {
	*core // want `promotion embeds core from its own package`
	plain
	sync.Mutex
}
