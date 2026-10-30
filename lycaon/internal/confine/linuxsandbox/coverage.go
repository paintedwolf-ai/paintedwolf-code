package linuxsandbox

import (
	"os"
	"runtime/coverage"
)

// FlushCoverage writes this process's coverage counters to GOCOVERDIR when it
// is set. A helper is the test binary run again and ends by os.Exit or a
// signal, which such a binary never records on its own; a build without
// coverage reports an error that is ignored.
func FlushCoverage() {
	dir := os.Getenv("GOCOVERDIR")
	if dir == "" {
		return
	}
	if coverage.WriteMetaDir(dir) == nil {
		_ = coverage.WriteCountersDir(dir)
	}
}

// coverageEnvironment is the environment of a process this helper starts from
// its own binary with no inherited variables: only the coverage directory, so
// an instrumented build records there instead of warning on the shared stderr.
func coverageEnvironment() []string {
	dir := os.Getenv("GOCOVERDIR")
	if dir == "" {
		return []string{}
	}
	return []string{"GOCOVERDIR=" + dir}
}
