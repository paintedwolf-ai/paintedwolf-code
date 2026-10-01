package bundleddriver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/scan/opengrep"
	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOpenGrepCancellationDiscardsUnfinishedReport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scanner fixture requires macOS or Linux")
	}
	for _, report := range []string{`{"results":[]}`, `{"results":[`} {
		t.Run(report, func(t *testing.T) {
			projectDir := t.TempDir()
			files, cleanup, err := newOpengrepRunFiles()
			testutil.FailErr(t, "create scanner output directory", err)
			t.Cleanup(cleanup)
			bin := filepath.Join(projectDir, "scanner")
			script := fmt.Sprintf(`#!/bin/sh
while [ "$#" -gt 0 ]; do
    if [ "$1" = --output ]; then
        shift
        printf '%%s' '%s' > "$1"
        exec /bin/sleep 300
    fi
    shift
done
exit 2
`, report)
			testutil.FailErr(t, "write scanner fixture", os.WriteFile(bin, []byte(script), 0o700))
			scanner := NewOpenGrepScanner(OpenGrepOptions{Jobs: 1})
			ctx, cancel := context.WithCancel(t.Context())
			type outcome struct {
				result *scanoutput.Result
				err    error
			}
			done := make(chan struct{})
			var got outcome
			go func() {
				defer close(done)
				got.result, got.err = scanner.scanTargets(ctx, projectDir, bin, []string{"fixture-rule"}, []string{projectDir}, nil, opengrep.Analysis{Mode: opengrep.Intrafile}, files)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(testutil.Timeout(15 * time.Second)):
					t.Error("scanner process did not stop after cancellation")
				}
			})
			testutil.WaitFor(t, 15*time.Second, func() bool {
				select {
				case <-done:
					t.Fatalf("scanner exited before cancellation: %v", got.err)
				default:
				}
				info, err := os.Stat(files.jsonPath)
				return err == nil && info.Size() > 0
			})
			cancel()
			select {
			case <-done:
			case <-time.After(testutil.Timeout(15 * time.Second)):
				t.Fatal("scanner did not return after cancellation")
			}
			if got.result != nil || !errors.Is(got.err, context.Canceled) {
				t.Fatalf("canceled scanner: result=%#v error=%v", got.result, got.err)
			}
		})
	}
}
