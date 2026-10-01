// Package bytebound distinguishes transport, disk, and model-context byte limits.
package bytebound

// Transport bounds bytes accepted off the wire.
type Transport int64

// Int64 unwraps for stdlib calls that take a byte count.
func (b Transport) Int64() int64 { return int64(b) }

// Materialization bounds bytes permitted to land on disk.
type Materialization int64

// Int64 unwraps for stdlib calls that take a byte count.
func (b Materialization) Int64() int64 { return int64(b) }

// Prompt bounds bytes permitted into the model context.
type Prompt int

// Int unwraps for length comparisons against in-memory buffers.
func (b Prompt) Int() int { return int(b) }
