//go:build !race

package grammars

// raceEnabled reports whether the race detector is compiled into this
// test binary. See race_enabled_test.go.
const raceEnabled = false
