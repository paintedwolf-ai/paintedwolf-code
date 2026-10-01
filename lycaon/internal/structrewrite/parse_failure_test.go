package structrewrite

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestCanceledPatternPreservesParserFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := Run(ctx, Request{LangName: "go", Source: []byte("package p\nfunc main() { f(1) }\n"), Pattern: "f($A)"})
	var failure *tsparse.Failure
	var pattern *PatternError
	if result != nil || !errors.As(err, &failure) || !errors.As(err, &pattern) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pattern = %+v, %v", result, err)
	}
}
