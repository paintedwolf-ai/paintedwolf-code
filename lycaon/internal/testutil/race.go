package testutil

import (
	"flag"
	"strings"
	"testing"
)

// SkipIfRace skips tests that race instrumentation breaks, not merely slows.
func SkipIfRace(t testing.TB, reason string) {
	t.Helper()
	if RaceEnabled {
		t.Skipf("skipped under -race: %s", reason)
	}
}

// SelectRaceTests sets the default race tests without overriding a targeted run.
func SelectRaceTests(names ...string) {
	if !RaceEnabled {
		return
	}
	if !flag.Parsed() {
		flag.Parse()
	}
	explicit := false
	flag.Visit(func(f *flag.Flag) {
		explicit = explicit || f.Name == "test.run" || f.Name == "test.bench"
	})
	if !explicit {
		if err := flag.Set("test.run", "^("+strings.Join(names, "|")+")$"); err != nil {
			panic(err)
		}
	}
}
