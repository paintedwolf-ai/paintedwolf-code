package fileclone

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCloneProbeOwnsLifetimeAcrossPrimitiveOutcomes(t *testing.T) {
	primitiveError := errors.New("primitive I/O failure")
	canceled := errors.New("operation canceled")
	cleanupError := errors.New("cleanup unavailable")
	for _, supported := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			for _, cancelled := range []bool{false, true} {
				for _, cleanupFailed := range []bool{false, true} {
					name := fmt.Sprintf("supported=%v/error=%v/cancel=%v/cleanup=%v", supported, failed, cancelled, cleanupFailed)
					t.Run(name, func(t *testing.T) {
						root := t.TempDir()
						ctx, cancel := context.WithCancelCause(t.Context())
						defer cancel(nil)
						removed := 0
						var owned string
						got, err := probeSourceClone(ctx, "source", root, func(_, destination string) (bool, error) {
							// A platform primitive may leave a result even when it reports failure.
							if err := os.WriteFile(destination, []byte("partial clone"), 0600); err != nil {
								testutil.FailErr(t, "write file", err)
							}
							if cancelled {
								cancel(canceled)
							}
							if failed {
								return supported, primitiveError
							}
							return supported, nil
						}, func(directory string) error {
							removed++
							owned = directory
							if cleanupFailed {
								return cleanupError
							}
							return os.RemoveAll(directory)
						})
						if owned != "" {
							defer os.RemoveAll(owned)
						}
						if got != supported || removed != 1 {
							t.Fatalf("result=%v,cleanup calls=%d", got, removed)
						}
						for _, cause := range []struct {
							active bool
							err    error
						}{{failed, primitiveError}, {cancelled, canceled}, {cleanupFailed, cleanupError}} {
							if errors.Is(err, cause.err) != cause.active {
								t.Fatalf("lost/added cause %v: %v", cause.err, err)
							}
						}
						if !failed && !cancelled && !cleanupFailed && err != nil {
							t.Fatal(err)
						}
						entries, readErr := os.ReadDir(root)
						if readErr != nil {
							t.Fatal(readErr)
						}
						if !cleanupFailed && len(entries) != 0 {
							t.Fatalf("probe leaked owned files: %v", entries)
						}
						if cleanupFailed && len(entries) != 1 {
							t.Fatal("cleanup failure did not retain its identifiable owned directory")
						}
						if owned == root || filepath.Dir(owned) != root {
							t.Fatal("cleanup escaped its owned child directory")
						}
					})
				}
			}
		}
	}
}

func TestCanceledCloneProbeCreatesNothing(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancelCause(t.Context())
	cause := errors.New("canceled before admission")
	cancel(cause)
	called := false
	_, err := probeSourceClone(ctx, "source", root, func(string, string) (bool, error) { called = true; return false, nil }, func(string) error { called = true; return nil })
	if !errors.Is(err, cause) || called {
		t.Fatalf("canceled probe performed work: %v %v", called, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled probe leaked files: %v %v", entries, err)
	}
}
