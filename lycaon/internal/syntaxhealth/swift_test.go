package syntaxhealth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestCapturedSwiftSource(t *testing.T) {
	for _, name := range []string{"level.swift", "user-prompt.swift"} {
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile("testdata/swift/" + name)
			testutil.FailErr(t, "read captured Swift source", err)
			start := time.Now()
			report := Analyze(context.Background(), "swift", name, src, tsparse.Validation)
			t.Logf("bytes=%d elapsed=%s report=%+v", len(src), time.Since(start), report)
			if report.Status != StatusClean {
				t.Fatalf("captured Swift source did not parse cleanly: %+v", report)
			}
		})
	}
}

func TestSwiftConditionRecoveryPreservesSourceErrors(t *testing.T) {
	for _, header := range []string{"if !items.isEmpty", "if let item = maybeItem", "while !items.isEmpty", "while let item = nextItem()"} {
		t.Run(header, func(t *testing.T) {
			src := []byte("func run() {\n" + header + " {\nlet value =\n}\n}\n")
			report := Analyze(context.Background(), "swift", "broken.swift", src, tsparse.Validation)
			if report.Status != StatusBroken || len(report.Diagnostics) == 0 {
				t.Fatalf("control-flow recovery hid an incomplete initializer: %+v", report)
			}
		})
	}
}
