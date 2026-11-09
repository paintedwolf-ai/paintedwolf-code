package syntaxhealth

import (
	"bytes"
	"testing"

	"github.com/lycaon/lycaon/internal/tsparse"
)

func TestDockerfileEndOfFile(t *testing.T) {
	for _, source := range []string{"FROM nginx:alpine", "FROM nginx:alpine\nCOPY index.html /usr/share/nginx/html/index.html", "FROM nginx:alpine\nEXPOSE 80\nCMD [\"nginx\", \"-g\", \"daemon off;\"]"} {
		for _, ending := range []string{"", "\n", "\r\n"} {
			t.Run(source+ending, func(t *testing.T) {
				original := []byte(source + ending)
				copy := bytes.Clone(original)
				got := Analyze(t.Context(), "dockerfile", "Dockerfile", original, tsparse.Validation)
				if got.Status != StatusClean {
					t.Fatalf("valid Dockerfile rejected: %+v", got)
				}
				if !bytes.Equal(original, copy) {
					t.Fatal("analysis mutated source")
				}
			})
		}
	}
}

func TestDockerfileMalformedInstructionStillRejected(t *testing.T) {
	for _, source := range []string{"FROM", "FROM\n", "FROM\r\n"} {
		report := Analyze(t.Context(), "dockerfile", "Dockerfile", []byte(source), tsparse.Validation)
		if report.Status != StatusBroken {
			t.Fatalf("malformed Dockerfile %q: %+v", source, report)
		}
		for _, diagnostic := range report.Diagnostics {
			if diagnostic.StartByte > len(source) || diagnostic.EndByte > len(source) {
				t.Fatalf("synthetic terminator leaked into diagnostics: %+v", diagnostic)
			}
		}
	}
}
