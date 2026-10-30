package sandbox

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSurveyTypedReadErrorContinuationAndStop(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, mode := range []string{"continue", "stop", "cancel", "strict"} {
			t.Run(fmt.Sprintf("%v/%s", stream, mode), func(t *testing.T) {
				root := t.TempDir()
				if err := os.Mkdir(filepath.Join(root, "gone"), 0700); err != nil {
					testutil.FailErr(t, "create survey fixture directory", err)
				}
				if err := os.WriteFile(filepath.Join(root, "kept"), nil, 0600); err != nil {
					testutil.FailErr(t, "write file", err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				sentinel := errors.New("stop traversal")
				calls, kept := 0, false
				options := SurveyOptions{Stream: stream, OnBoundary: func(SurveyBoundary) { t.Error("typed hook lost precedence") }}
				if mode == "strict" {
					options.OnBoundary = nil
				} else {
					options.OnReadError = func(rel string, err error) error {
						calls++
						if rel != "gone" || !errors.Is(err, fs.ErrNotExist) {
							t.Fatalf("read failure %s %v", rel, err)
						}
						if mode == "stop" {
							return fmt.Errorf("read: %w", sentinel)
						}
						return nil
					}
				}
				err := SurveyWalk(ctx, root, options, func(entry SurveyEntry) (SurveyAction, error) {
					if entry.Rel == "gone" {
						if err := os.Remove(entry.Abs); err != nil {
							testutil.FailErr(t, "remove surveyed fixture entry", err)
						}
						if mode == "cancel" {
							cancel()
						}
					}
					if entry.Rel == "kept" {
						kept = true
					}
					return SurveyContinue, nil
				})
				switch mode {
				case "continue":
					if err != nil || !kept || calls != 1 {
						t.Fatalf("continued: kept=%v calls=%d err=%v", kept, calls, err)
					}
				case "stop":
					if !errors.Is(err, sentinel) || calls != 1 {
						t.Fatalf("stop calls=%d err=%v", calls, err)
					}
				case "cancel":
					if !errors.Is(err, context.Canceled) || calls != 0 {
						t.Fatalf("cancel calls=%d err=%v", calls, err)
					}
				case "strict":
					if !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("strict error %v", err)
					}
				}
			})
		}
	}
}
