//go:build scanstress

package scanstress

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/pkg/api"
)

// envVictim marks the child invocation that owns a long-running scan and is
// then interrupted by its parent.
const envVictim = "SCANSTRESS_VICTIM"

// envVictimHandler makes the victim install the sidecar's signal handler.
const envVictimHandler = "SCANSTRESS_VICTIM_HANDLER"

// TestShutdownVictimScan is the host under test in the shutdown cases. It runs
// only when its parent asks for it, and it never asserts anything itself. When
// asked, it installs the sidecar's own signal handling (cmd/lycaon/main.go
// notifies a context on SIGINT and SIGTERM) so the graceful case exercises the
// shipped shutdown path rather than Go's default terminate-on-signal.
func TestShutdownVictimScan(t *testing.T) {
	if os.Getenv(envVictim) == "" {
		t.Skip("victim runs only when a shutdown case starts it")
	}
	ctx := t.Context()
	if os.Getenv(envVictimHandler) != "" {
		signalled, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-signalled.Done()
			fmt.Fprintf(os.Stderr, "victim-signal received\n")
			cancel()
		}()
	}
	scanner := newScanner(t, "stress-victim", scancatalog.RuntimePolicy{})
	project := writeCorpus(t, corpusSpec{Files: corpusFiles(), VulnPerFile: 3})
	fmt.Fprintf(os.Stderr, "victim-ready pid=%d\n", os.Getpid())
	_, err := scanner.Run(ctx, scan.ScanRequest{
		ProjectDir: project,
		Categories: []api.ScanCategory{api.ScanCategorySAST},
	})
	fmt.Fprintf(os.Stderr, "victim-done err=%v\n", err)
}

// pidAlive reports whether a pid is still in the process table. Observation
// only: nothing outside this test's own victim tree is ever signalled.
func pidAlive(t *testing.T, pid int) bool {
	t.Helper()
	_, ok := processTable(t)[pid]
	return ok
}

// runShutdownCase starts a victim process, waits until it owns live engine
// processes, sends it exactly one signal, and reports what survived.
func runShutdownCase(t *testing.T, sig syscall.Signal, handler bool) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	cmd := exec.Command(self, "-test.run", "^TestShutdownVictimScan$", "-test.v", "-test.timeout", "20m")
	cmd.Env = append(os.Environ(), envVictim+"=1")
	if handler {
		cmd.Env = append(cmd.Env, envVictimHandler+"=1")
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start victim: %v", err)
	}
	victim := cmd.Process.Pid
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	reaped := false
	t.Cleanup(func() {
		// Only ever this test's own victim pid.
		if pidAlive(t, victim) {
			_ = cmd.Process.Kill()
		}
		if !reaped {
			<-waitErr
		}
	})

	var engine []procRow
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		engine = engineProcesses(descendants(processTable(t), victim))
		if len(engine) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(engine) == 0 {
		t.Fatalf("victim %d never started an engine; output:\n%s", victim, stderr.String())
	}
	// Steady-state engine work before the interrupt.
	time.Sleep(5 * time.Second)
	engine = engineProcesses(descendants(processTable(t), victim))
	runDirs := runDirsFromRows(descendants(processTable(t), victim))
	enginePIDs := make([]int, 0, len(engine))
	for _, row := range engine {
		enginePIDs = append(enginePIDs, row.PID)
	}

	killAt := time.Now()
	if err := syscall.Kill(victim, sig); err != nil {
		t.Fatalf("signal victim %d: %v", victim, err)
	}
	select {
	case <-waitErr:
		reaped = true
	case <-time.After(90 * time.Second):
		t.Fatalf("victim %d did not exit within 90s of %s", victim, sig)
	}
	hostGone := time.Since(killAt)

	// Poll only the engine pids this victim owned.
	var survivors []int
	drainDeadline := time.Now().Add(60 * time.Second)
	for {
		survivors = survivors[:0]
		table := processTable(t)
		for _, pid := range enginePIDs {
			if row, ok := table[pid]; ok && strings.Contains(row.Command, "opengrep") {
				survivors = append(survivors, pid)
			}
		}
		if len(survivors) == 0 || time.Now().After(drainDeadline) {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	drain := time.Since(killAt) - hostGone
	leaked := survivingDirs(runDirs)

	reportf(t, "signal=%s handler=%t victim=%d engine_pids=%v host_exit=%s engine_drain=%s orphans=%v leaked_run_dirs=%v",
		sig, handler, victim, enginePIDs, hostGone.Round(time.Millisecond), drain.Round(time.Millisecond), survivors, leaked)

	if len(survivors) > 0 {
		// Reap this test's own orphans so the host is left clean.
		for _, pid := range survivors {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		t.Errorf("%s to the scan host orphaned engine processes %v; they outlived the host by more than 60s", sig, survivors)
	}
	if len(leaked) > 0 {
		for _, dir := range leaked {
			_ = os.RemoveAll(dir)
		}
		t.Errorf("%s to the scan host left run directories behind: %v", sig, leaked)
	}
}

// TestGracefulShutdownReapsEngine models the sidecar receiving SIGTERM while a
// scan is executing: the shipped handler cancels the scan context and the exec
// layer tears the engine process group down.
func TestGracefulShutdownReapsEngine(t *testing.T) {
	runShutdownCase(t, syscall.SIGTERM, true)
}

// TestUnhandledTerminationReapsEngine models a host that dies without running
// its handler — a crash, an OOM kill, or SIGTERM to a build with no handler.
// Only kernel-level ownership can reap the engine here.
func TestUnhandledTerminationReapsEngine(t *testing.T) {
	runShutdownCase(t, syscall.SIGKILL, false)
}
