package smoke_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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
