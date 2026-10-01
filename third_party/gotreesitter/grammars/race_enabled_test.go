//go:build race

package grammars

// raceEnabled reports whether the race detector is compiled into this
// test binary. The race lane skips recovery-heavy known-failing corpus
// files whose error-recovery cost, multiplied by the race detector,
// exceeds the CI shard time budget. Their ratchet assertions still run
// in every non-race lane.
const raceEnabled = true
