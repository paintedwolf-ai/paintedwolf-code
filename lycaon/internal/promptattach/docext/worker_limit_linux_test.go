package docext

import (
	"os"
	"os/exec"
	"testing"
)

// The limit binds memory the worker uses, so a runaway parse fails rather than
// growing past it.
func TestWorkerMemoryLimitStopsRunawayAllocation(t *testing.T) {
	if os.Getenv("DOCEXT_MEMORY_LIMIT_HELPER") == "1" {
		applyWorkerMemoryLimit(64 << 20)
		buf := make([]byte, 512<<20)
		for i := 0; i < len(buf); i += 4096 {
			buf[i] = 1
		}
		os.Exit(0)
	}
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWorkerMemoryLimitStopsRunawayAllocation$")
	cmd.Env = append(os.Environ(), "DOCEXT_MEMORY_LIMIT_HELPER=1")
	if err := cmd.Run(); err == nil {
		t.Fatal("a worker past its memory limit kept allocating")
	}
}
