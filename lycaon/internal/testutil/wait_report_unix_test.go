//go:build unix

package testutil

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestPendingWaitReportsRunWhileWaitsComeAndGo(t *testing.T) {
	var out bytes.Buffer
	pendingWaits.Lock()
	previous := pendingWaits.out
	pendingWaits.out = &out
	pendingWaits.Unlock()
	t.Cleanup(func() {
		pendingWaits.Lock()
		pendingWaits.out = previous
		pendingWaits.Unlock()
	})

	const rounds = 200
	signals := make(chan os.Signal)
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		reportPendingWaits(signals, io.Discard)
	}()

	var waits sync.WaitGroup
	for worker := range 4 {
		waits.Go(func() {
			for round := range rounds {
				trackWait(t, fmt.Sprintf("worker %d round %d", worker, round))()
			}
		})
	}
	// testing.M.Run reassigns os.Stderr with no ordering against a signal.
	stderr := os.Stderr
	waits.Go(func() {
		for range rounds {
			os.Stderr = stderr
		}
	})
	for range rounds {
		signals <- os.Interrupt
	}
	waits.Wait()
	close(signals)
	<-reported

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) < rounds {
		t.Fatalf("got %d report lines for %d signals", len(lines), rounds)
	}
	for _, line := range lines {
		if line != "pending waits: none" && !strings.HasPrefix(line, "pending wait: "+t.Name()+": worker ") {
			t.Fatalf("unexpected report line %q", line)
		}
	}
}

func TestPendingWaitReportBeforeAnyWaitUsesInitStderr(t *testing.T) {
	pendingWaits.Lock()
	previous := pendingWaits.out
	pendingWaits.out = nil
	pendingWaits.Unlock()
	t.Cleanup(func() {
		pendingWaits.Lock()
		pendingWaits.out = previous
		pendingWaits.Unlock()
	})
	var initStderr bytes.Buffer
	if got := pendingWaitsOutput(&initStderr); got != &initStderr {
		t.Fatalf("output before any wait = %T, want the init stderr", got)
	}
}
