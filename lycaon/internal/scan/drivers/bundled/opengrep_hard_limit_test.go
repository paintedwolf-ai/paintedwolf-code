package bundleddriver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/exec"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/scan/opengrep"
	"github.com/lycaon/lycaon/internal/testutil"
)

const slowFixtureScript = `#!/bin/sh
if [ -n "${SCANNER_DIAGNOSTIC:-}" ]; then
    printf '%s\n' "$SCANNER_DIAGNOSTIC" >&2
fi
if [ -n "${SCANNER_MARKER:-}" ]; then
    printf 'started\n' > "$SCANNER_MARKER"
fi
exec /bin/sleep 30
`

func writeSlowFixtureBinary(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scanner fixture requires macOS or Linux")
	}
	bin := filepath.Join(dir, "scanner")
	testutil.FailErr(t, "write slow scanner fixture", os.WriteFile(bin, []byte(slowFixtureScript), 0o700))
	return bin
}

func fixturePrepare(t *testing.T, onPrepare func()) func() (opengrepRunFiles, func(), []string, error) {
	t.Helper()
	return func() (opengrepRunFiles, func(), []string, error) {
		if onPrepare != nil {
			onPrepare()
		}
		files, cleanup, err := newOpengrepRunFiles()
		if err != nil {
			return opengrepRunFiles{}, nil, nil, err
		}
		rulePath := filepath.Join(files.dir(), "rules.yaml")
		if err := os.WriteFile(rulePath, []byte("rules: []\n"), 0o600); err != nil {
			cleanup()
			return opengrepRunFiles{}, nil, nil, err
		}
		return files, cleanup, []string{rulePath}, nil
	}
}

func TestOpenGrepHardLimitClassification(t *testing.T) {
	t.Run("caller cancellation", func(t *testing.T) {
		root := t.TempDir()
		bin := writeSlowFixtureBinary(t, root)
		marker := filepath.Join(root, "started")
		t.Setenv("SCANNER_MARKER", marker)
		t.Setenv("SCANNER_DIAGNOSTIC", "")

		scanner := NewOpenGrepScanner(OpenGrepOptions{
			Jobs:          1,
			RuntimePolicy: scancatalog.RuntimePolicy{SoftLimitSec: 30, HardLimitSec: 60, CPUUnits: 1, Parallelism: 1},
		})
		ctx, cancel := context.WithCancel(t.Context())
		errCh := make(chan error, 1)
		go func() {
			_, err := scanner.runWithPolicy(ctx, root, bin, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intrafile}, fixturePrepare(t, nil))
			errCh <- err
		}()
		t.Cleanup(func() {
			cancel()
		})
		testutil.WaitFor(t, "slow fixture starts", func() bool {
			_, err := os.Stat(marker)
			return err == nil
		})
		cancel()
		err := <-errCh
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("caller cancellation returned %v, want context.Canceled", err)
		}
		var timeout *exec.TimeoutError
		if errors.As(err, &timeout) {
			t.Fatalf("caller cancellation misclassified as TimeoutError: %#v", timeout)
		}
	})

	t.Run("caller deadline shorter than hard limit", func(t *testing.T) {
		root := t.TempDir()
		bin := writeSlowFixtureBinary(t, root)
		t.Setenv("SCANNER_MARKER", "")
		t.Setenv("SCANNER_DIAGNOSTIC", "")

		scanner := NewOpenGrepScanner(OpenGrepOptions{
			Jobs:          1,
			RuntimePolicy: scancatalog.RuntimePolicy{SoftLimitSec: 30, HardLimitSec: 60, CPUUnits: 1, Parallelism: 1},
		})
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		res, err := scanner.runWithPolicy(ctx, root, bin, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intrafile}, fixturePrepare(t, nil))
		if res != nil {
			t.Fatalf("unexpected result on caller deadline: %#v", res)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("caller deadline returned %v, want context.DeadlineExceeded", err)
		}
		var timeout *exec.TimeoutError
		if errors.As(err, &timeout) && timeout.CommandDeadline {
			t.Fatalf("shorter caller deadline misclassified as CommandDeadline: %#v", timeout)
		}
		if errors.Is(err, scancatalog.ErrHardLimitExceeded) {
			t.Fatalf("shorter caller deadline misclassified with ErrHardLimitExceeded: %v", err)
		}
	})

	t.Run("hard limit expires during execution", func(t *testing.T) {
		root := t.TempDir()
		bin := writeSlowFixtureBinary(t, root)
		t.Setenv("SCANNER_MARKER", "")
		t.Setenv("SCANNER_DIAGNOSTIC", "")

		scanner := NewOpenGrepScanner(OpenGrepOptions{
			Jobs:          1,
			RuntimePolicy: scancatalog.RuntimePolicy{SoftLimitSec: 1, HardLimitSec: 1, CPUUnits: 1, Parallelism: 1},
		})
		res, err := scanner.runWithPolicy(t.Context(), root, bin, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intrafile}, fixturePrepare(t, nil))
		if res != nil {
			t.Fatalf("unexpected result on hard limit: %#v", res)
		}
		var timeout *exec.TimeoutError
		if !errors.As(err, &timeout) || !timeout.CommandDeadline {
			t.Fatalf("hard limit during execution returned %v, want *exec.TimeoutError{CommandDeadline: true}", err)
		}
		if !errors.Is(err, exec.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("hard limit error %v must unwrap ErrTimeout and context.DeadlineExceeded", err)
		}
		if timeout.Deadline.IsZero() {
			t.Fatalf("hard limit timeout metadata missing deadline: %#v", timeout)
		}
	})

	t.Run("hard limit after setup preserves diagnostics while child runs", func(t *testing.T) {
		root := t.TempDir()
		bin := writeSlowFixtureBinary(t, root)
		marker := filepath.Join(root, "started")
		t.Setenv("SCANNER_MARKER", marker)
		t.Setenv("SCANNER_DIAGNOSTIC", "fixture-stderr-diagnostic")

		scanner := NewOpenGrepScanner(OpenGrepOptions{
			Jobs:          1,
			RuntimePolicy: scancatalog.RuntimePolicy{SoftLimitSec: 30, HardLimitSec: 60, CPUUnits: 1, Parallelism: 1},
		})
		ctx, cancelCause := context.WithCancelCause(t.Context())
		defer cancelCause(nil)

		setupCompleted := make(chan struct{})
		errCh := make(chan error, 1)
		go func() {
			_, err := scanner.runWithPolicy(ctx, root, bin, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intrafile}, fixturePrepare(t, func() {
				close(setupCompleted)
			}))
			errCh <- err
		}()

		<-setupCompleted
		testutil.WaitFor(t, "slow fixture starts after setup", func() bool {
			_, err := os.Stat(marker)
			return err == nil
		})
		cancelCause(scancatalog.ErrHardLimitExceeded)
		err := <-errCh
		var timeout *exec.TimeoutError
		if !errors.As(err, &timeout) || !timeout.CommandDeadline {
			t.Fatalf("hard limit after setup returned %v, want *exec.TimeoutError{CommandDeadline: true}", err)
		}
		if !errors.Is(err, exec.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("hard limit after setup %v must unwrap ErrTimeout and context.DeadlineExceeded", err)
		}
		if !strings.Contains(err.Error(), "fixture-stderr-diagnostic") {
			t.Fatalf("hard limit after setup lost scanner diagnostics: %v", err)
		}
	})

	t.Run("hard limit exhausted during setup before child spawn", func(t *testing.T) {
		root := t.TempDir()
		bin := writeSlowFixtureBinary(t, root)
		marker := filepath.Join(root, "started")
		t.Setenv("SCANNER_MARKER", marker)
		t.Setenv("SCANNER_DIAGNOSTIC", "")

		scanner := NewOpenGrepScanner(OpenGrepOptions{
			Jobs:          1,
			RuntimePolicy: scancatalog.RuntimePolicy{SoftLimitSec: 30, HardLimitSec: 60, CPUUnits: 1, Parallelism: 1},
		})
		ctx, cancelCause := context.WithCancelCause(t.Context())
		defer cancelCause(nil)

		res, err := scanner.runWithPolicy(ctx, root, bin, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intrafile}, fixturePrepare(t, func() {
			cancelCause(scancatalog.ErrHardLimitExceeded)
		}))
		if res != nil {
			t.Fatalf("unexpected result when setup exhausted hard limit: %#v", res)
		}
		var timeout *exec.TimeoutError
		if !errors.As(err, &timeout) || !timeout.CommandDeadline {
			t.Fatalf("hard limit exhausted during setup returned %v, want *exec.TimeoutError{CommandDeadline: true}", err)
		}
		if !errors.Is(err, exec.ErrTimeout) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("hard limit exhausted during setup %v must unwrap ErrTimeout and context.DeadlineExceeded", err)
		}
		if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("child spawned despite setup exhausting hard limit: stat=%v", statErr)
		}
	})

	t.Run("inherited policy context from runner preserves hard limit", func(t *testing.T) {
		root := t.TempDir()
		bin := writeSlowFixtureBinary(t, root)
		t.Setenv("SCANNER_MARKER", "")
		t.Setenv("SCANNER_DIAGNOSTIC", "")

		policy := scancatalog.RuntimePolicy{SoftLimitSec: 1, HardLimitSec: 1, CPUUnits: 1, Parallelism: 1}
		runnerCtx, cancelRunner := policy.Context(t.Context())
		defer cancelRunner()

		scanner := NewOpenGrepScanner(OpenGrepOptions{
			Jobs:          1,
			RuntimePolicy: policy,
		})
		res, err := scanner.runWithPolicy(runnerCtx, root, bin, []string{root}, nil, opengrep.Analysis{Mode: opengrep.Intrafile}, fixturePrepare(t, nil))
		if res != nil {
			t.Fatalf("unexpected result with runner policy context: %#v", res)
		}
		var timeout *exec.TimeoutError
		if !errors.As(err, &timeout) || !timeout.CommandDeadline {
			t.Fatalf("runner policy hard limit returned %v, want *exec.TimeoutError{CommandDeadline: true}", err)
		}
	})
}
