package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestStreamingSurveyVisitsEveryBatchAndHonorsCancellation(t *testing.T) {
	root := t.TempDir()
	for i := range 1025 {
		testutil.FailErr(t, "seed wide directory", os.WriteFile(filepath.Join(root, fmt.Sprintf("entry-%05d", i)), nil, 0600))
	}
	count := 0
	testutil.FailErr(t, "stream wide directory", SurveyWalk(t.Context(), root, SurveyOptions{Stream: true}, func(SurveyEntry) (SurveyAction, error) { count++; return SurveyContinue, nil }))
	if count != 1025 {
		t.Fatalf("stream stopped at a batch boundary: %d", count)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	count = 0
	err := SurveyWalk(ctx, root, SurveyOptions{Stream: true}, func(SurveyEntry) (SurveyAction, error) { count++; cancel(); return SurveyContinue, nil })
	if !errors.Is(err, context.Canceled) || count != 1 {
		t.Fatalf("cancellation count=%d error=%v", count, err)
	}
}

func TestSurveyReadFailuresRemainVisibleWhileSiblingsContinue(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream_%v", stream), func(t *testing.T) {
			root := t.TempDir()
			testutil.FailErr(t, "seed disappearing directory", os.Mkdir(filepath.Join(root, "gone"), 0700))
			testutil.FailErr(t, "seed readable sibling", os.WriteFile(filepath.Join(root, "kept"), nil, 0600))
			var boundaries []SurveyBoundary
			kept := false
			err := SurveyWalk(t.Context(), root, SurveyOptions{Stream: stream, OnBoundary: func(b SurveyBoundary) { boundaries = append(boundaries, b) }}, func(entry SurveyEntry) (SurveyAction, error) {
				if entry.Rel == "gone" {
					testutil.FailErr(t, "remove discovered directory", os.Remove(entry.Abs))
				}
				if entry.Rel == "kept" {
					kept = true
				}
				return SurveyContinue, nil
			})
			testutil.FailErr(t, "survey readable portions", err)
			if !kept || len(boundaries) != 1 || boundaries[0].Reason != BoundaryUnreadable || boundaries[0].Rel != "gone" {
				t.Fatalf("lost read failure or sibling: kept=%v boundaries=%v", kept, boundaries)
			}
		})
	}
}
