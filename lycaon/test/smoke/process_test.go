package smoke_test

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

type smokeProcess struct {
	done chan struct{}
	err  error
}

func startSmokeProcess(t *testing.T, proc *exec.Cmd) *smokeProcess {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sidecar.log")
	log, err := os.Create(path)
	testutil.FailErr(t, "create sidecar log", err)
	proc.Stdout, proc.Stderr = log, log
	t.Cleanup(func() {
		testutil.FailErr(t, "close sidecar log", log.Close())
		if t.Failed() {
			data, err := os.ReadFile(path)
			testutil.FailErr(t, "read sidecar log", err)
			t.Logf("sidecar output:\n%s", data)
		}
	})
	testutil.FailErr(t, "start sidecar", proc.Start())
	child := &smokeProcess{done: make(chan struct{})}
	go func() {
		child.err = proc.Wait()
		close(child.done)
	}()
	t.Cleanup(func() {
		_ = proc.Process.Kill()
		<-child.done
	})
	return child
}

func (p *smokeProcess) waitForHealth(t *testing.T, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	ready := testutil.WaitForNoFatal(30*time.Second, func() bool {
		select {
		case <-p.done:
			t.Fatalf("sidecar exited before health: %v", p.err)
		default:
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/health", nil)
		testutil.FailErr(t, "health request", err)
		resp, err := client.Do(req)
		if resp != nil {
			defer resp.Body.Close()
		}
		return err == nil && resp.StatusCode == http.StatusOK
	})
	if !ready {
		t.Fatalf("server at %s did not become healthy", baseURL)
	}
}

func TestSmokeStartupFailureReportsExitAndOutput(t *testing.T) {
	const mode = "PW_SMOKE_STARTUP_FIXTURE"
	switch os.Getenv(mode) {
	case "engine":
		fmt.Fprintln(os.Stderr, "fixture startup rejected")
		os.Exit(7)
	case "probe":
		proc := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSmokeStartupFailureReportsExitAndOutput$", "-test.short=true")
		proc.Env = append(os.Environ(), mode+"=engine")
		startSmokeProcess(t, proc).waitForHealth(t, "http://127.0.0.1:0")
		return
	}
	ctx := testutil.BoundedContext(t, 10*time.Second)
	probe := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSmokeStartupFailureReportsExitAndOutput$", "-test.short=true")
	probe.Env = append(os.Environ(), mode+"=probe")
	output, err := probe.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("startup failure did not finish cleanup: %v\n%s", ctx.Err(), output)
	}
	if err == nil || !strings.Contains(string(output), "sidecar exited before health: exit status 7") ||
		!strings.Contains(string(output), "fixture startup rejected") {
		t.Fatalf("startup failure lost its exit or output: err=%v\n%s", err, output)
	}
}
