package extensionstate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"testing"
)

func TestCandidateFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		rejected bool
	}{
		{"manifest", fmt.Errorf("manifest version is invalid"), true},
		{"storage", &fs.PathError{Op: "write", Path: "cache", Err: fs.ErrPermission}, false},
		{"network", &net.DNSError{Err: "unavailable", Name: "registry.example"}, false},
		{"canceled", context.Canceled, false},
		{"timeout", context.DeadlineExceeded, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := fmt.Errorf("candidate: %w", tc.err)
			got := candidateFailure(original)
			var rejected *RejectedError
			if errors.As(got, &rejected) != tc.rejected {
				t.Fatalf("classification = %T, rejected = %v", got, tc.rejected)
			}
			if !errors.Is(got, tc.err) {
				t.Fatal("candidate failure lost its diagnostic cause")
			}
		})
	}
}
