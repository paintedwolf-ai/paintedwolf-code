package syntaxhealth

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestPythonUnterminatedStringsAreBroken(t *testing.T) {
	for _, src := range []string{"'''unclosed\n", "ok = '''closed'''\nf\"{value}\"\n'''unclosed\n"} {
		report := Analyze(context.Background(), "python", "", []byte(src), tsparse.Validation)
		if report.Status != StatusBroken {
			t.Fatalf("status = %q, want broken; report=%+v", report.Status, report)
		}
	}
	for _, src := range []string{"'''closed'''\n", "f\"{value}\"\n"} {
		report := Analyze(context.Background(), "python", "", []byte(src), tsparse.Validation)
		if report.Status != StatusClean {
			t.Fatalf("valid source status = %q; report=%+v", report.Status, report)
		}
	}
}
