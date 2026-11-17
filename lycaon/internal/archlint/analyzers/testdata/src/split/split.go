package split

import "sync"

type core struct{}

func (c *core) run() {}

type plain struct{ n int }

func (p plain) size() int { return p.n }

type promotion struct {
	*core // want `promotion embeds core from its own package`
	plain
	sync.Mutex
}

func (p *promotion) promote() {}

// A data holder without methods composes freely.
type record struct {
	*core
}
