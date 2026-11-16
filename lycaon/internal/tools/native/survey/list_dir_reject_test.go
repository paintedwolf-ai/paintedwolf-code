package survey

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"
)

func assertListDirRejectCode(t *testing.T, err error, code string) {
	t.Helper()
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != code {
		t.Fatalf("error = %v, want %s ToolReject", err, code)
	}
}
