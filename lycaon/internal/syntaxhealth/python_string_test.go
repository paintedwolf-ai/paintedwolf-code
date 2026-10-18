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

func TestPythonShortStringLineBreaks(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		want Status
	}{
		{"plain newline", "x = 'one\ntwo'\n", StatusBroken},
		{"formatted newline", "print(f\"Words: {words}\nCharacters: {chars}\")\n", StatusBroken},
		{"triple quoted", "x = '''one\ntwo'''\n", StatusClean},
		{"formatted triple", "x = f'''{words}\n{chars}'''\n", StatusClean},
		{"escaped newline", "x = 'one\\\ntwo'\n", StatusClean},
		{"escaped character", "x = 'one\\ntwo'\n", StatusClean},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := Analyze(t.Context(), "python", "app.py", []byte(tc.source), tsparse.Validation)
			if report.Status != tc.want {
				t.Fatalf("status = %q, want %q; report=%+v", report.Status, tc.want, report)
			}
		})
	}
}
