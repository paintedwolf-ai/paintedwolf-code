package forward

import "upstream"

type Record = upstream.Record // want `type Record re-exports upstream.Record`

type Local = local

type local struct{}

var OpenFile = upstream.Open // want `OpenFile re-exports upstream.Open`

func Open(path string, mode int) error { // want `Open forwards to upstream.Open`
	return upstream.Open(path, mode)
}

func Close() { // want `Close forwards to upstream.Close`
	upstream.Close()
}

// Reordered arguments are a translation, not a forward.
func OpenSwapped(mode int, path string) error { return upstream.Open(path, mode) }

func OpenRead(path string) error { return upstream.Open(path, 0) }

func unexported(path string, mode int) error { return upstream.Open(path, mode) }
