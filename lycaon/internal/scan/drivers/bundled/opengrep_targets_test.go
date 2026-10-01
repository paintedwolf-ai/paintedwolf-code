package bundleddriver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"syscall"
	"testing"

	scanoutput "github.com/lycaon/lycaon/internal/scan/output"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOpenGrepBatchesPreserveEveryTargetAndResult(t *testing.T) {
	targets := make([]string, 5000)
	for i := range targets {
		targets[i] = fmt.Sprintf("/repo/%s/space 世界\n%d.go", strings.Repeat("folder/", 15), i)
	}
	calls := 0
	result, err := scanOpengrepTargetBatches(t.Context(), targets, func(batch []string) (*scanoutput.Result, error) {
		calls++
		return &scanoutput.Result{
			ScannedPaths:  batch,
			FindingsCount: 1,
			Findings:      []api.SecurityFinding{{RuleID: batch[0]}},
			Categories:    []api.ScanCategory{api.ScanCategorySAST},
			Warnings:      []api.ScanWarning{{File: batch[0]}},
		}, nil
	})
	testutil.FailErr(t, "scan every target batch", err)
	if calls < 2 || !reflect.DeepEqual(result.ScannedPaths, targets) {
		t.Fatalf("target batching lost scope: calls=%d scanned=%d want=%d", calls, len(result.ScannedPaths), len(targets))
	}
	if len(result.Findings) != calls || result.FindingsCount != calls || len(result.Warnings) != calls || !reflect.DeepEqual(result.Categories, []api.ScanCategory{api.ScanCategorySAST}) {
		t.Fatalf("batch aggregation lost findings or coverage diagnostics: %+v", result)
	}
}

func TestOpenGrepSplitsOSArgumentRejectionsWithoutDroppingTargets(t *testing.T) {
	targets := []string{"one", "two", "three", "four", "five", "six"}
	rejections := 0
	result, err := scanOpengrepTargetBatches(t.Context(), targets, func(batch []string) (*scanoutput.Result, error) {
		if len(batch) > 2 {
			rejections++
			return nil, fmt.Errorf("launch scanner: %w", syscall.E2BIG)
		}
		return &scanoutput.Result{ScannedPaths: batch}, nil
	})
	testutil.FailErr(t, "recover operating system argument limit", err)
	if rejections == 0 || !reflect.DeepEqual(result.ScannedPaths, targets) {
		t.Fatalf("argument recovery lost targets: rejections=%d result=%+v", rejections, result)
	}
}

func TestOpenGrepBatchFailureNeverAdmitsEarlierResults(t *testing.T) {
	for _, cancelWork := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelWork), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("scanner failed")
			if cancelWork {
				failure = context.Canceled
			}
			calls := 0
			result, err := scanOpengrepTargetBatches(ctx, []string{strings.Repeat("a", 128<<10), "second"}, func(batch []string) (*scanoutput.Result, error) {
				calls++
				if calls == 2 {
					if cancelWork {
						cancel()
						return &scanoutput.Result{ScannedPaths: batch}, nil
					}
					return nil, failure
				}
				return &scanoutput.Result{ScannedPaths: batch}, nil
			})
			if result != nil || !errors.Is(err, failure) || calls != 2 {
				t.Fatalf("failed batch admitted earlier results: result=%+v err=%v calls=%d", result, err, calls)
			}
		})
	}
}

func TestOpenGrepCannotSplitASingleRejectedTarget(t *testing.T) {
	calls := 0
	result, err := scanOpengrepTargetBatches(t.Context(), []string{"one"}, func([]string) (*scanoutput.Result, error) {
		calls++
		return nil, syscall.E2BIG
	})
	if result != nil || !errors.Is(err, syscall.E2BIG) || calls != 1 {
		t.Fatalf("single target launch failure was lost or retried: result=%+v error=%v calls=%d", result, err, calls)
	}
}
