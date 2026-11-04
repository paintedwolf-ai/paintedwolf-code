package textsearch

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func foldedReference(record, pattern []byte, from int, maximum int64, work *int64) (int, error) {
	if len(pattern) == 0 {
		if *work >= maximum {
			return -1, &BudgetError{Resource: "regex_work_steps", Maximum: maximum, Observed: *work + 1}
		}
		*work++
		return from, nil
	}
	for at := from; at <= len(record)-len(pattern); at++ {
		matched := true
		for offset, want := range pattern {
			if *work >= maximum {
				return -1, &BudgetError{Resource: "regex_work_steps", Maximum: maximum, Observed: *work + 1}
			}
			*work++
			if asciiFold(record[at+offset]) != asciiFold(want) {
				matched = false
				break
			}
		}
		if matched {
			return at, nil
		}
	}
	return -1, nil
}

func TestFoldedCandidatesPreserveComparisonWork(t *testing.T) {
	for _, record := range []string{"", "a", "!aAAa-bB", "aaabaaaab", "...AB...aB...", "a\x00A", strings.Repeat("!", 4096) + "Aa"} {
		for _, pattern := range []string{"", "a", "B", "aa", "aab", "!", "\x00", "aaaaaaa"} {
			for _, from := range []int{0, len(record) / 2, len(record)} {
				for _, maximum := range []int64{0, 1, 3, 12, 4096, 10000} {
					engine := literalEngine{limits: Limits{WorkSteps: maximum}}
					var gotWork, wantWork int64
					got, err := engine.foldedIndex(context.Background(), []byte(record), []byte(pattern), from, &gotWork)
					want, wantErr := foldedReference([]byte(record), []byte(pattern), from, maximum, &wantWork)
					if got != want || gotWork != wantWork || fmt.Sprint(err) != fmt.Sprint(wantErr) {
						t.Fatalf("length %d pattern %q from %d ceiling %d: got %d work %d err %v want %d work %d err %v", len(record), pattern, from, maximum, got, gotWork, err, want, wantWork, wantErr)
					}
				}
			}
		}
	}
}
