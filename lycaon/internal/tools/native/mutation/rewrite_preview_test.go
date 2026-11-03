package mutation

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestRewritePreviewPreservesSourceAndOutputFailure(t *testing.T) {
	const before = "package p\nfunc f() int { return 1 + 2 }\n"
	h := newHarness(t, map[string]string{"main.go": before})
	result := h.runAs(h.invocation(), failingPreviewWriter{}, "", "code-rewrite", "--path", "main.go", "--pattern", "1 + 2", "--rewrite", "3", "--dry-run")
	if !errors.Is(result.err, commandprogram.ErrConsumerClosed) || h.read("main.go") != before || result.result.EffectCertainty != toolcontract.NoEffect {
		t.Fatalf("preview failure: %+v %v", result.result, result.err)
	}
	noMatch := h.run("", "code-rewrite", "--path", "main.go", "--pattern", "2 + 3", "--rewrite", "4", "--dry-run")
	if noMatch.err != nil || noMatch.stdout != "" {
		t.Fatalf("no match: %q %v", noMatch.stdout, noMatch.err)
	}
}

type failingPreviewWriter struct{}

func (failingPreviewWriter) Write([]byte) (int, error) { return 0, commandprogram.ErrConsumerClosed }

func TestRewritePreviewLineBoundaries(t *testing.T) {
	h := newHarness(t, nil)
	for _, tc := range []struct{ before, after, want string }{
		{"", "new", "@@ -0,0 +1,1 @@\n+new\n\\ No newline at end of file\n"},
		{"old\r\n", "new\r\n", "-old\r\n+new\r\n"},
		{"old\n", "", "@@ -1,1 +0,0 @@\n-old\n"},
	} {
		var out bytes.Buffer
		inv := &invocation{ctx: t.Context(), owners: h.owners, stdout: &out}
		if err := inv.previewRewrite("file", tc.before, tc.after); err != nil {
			t.Fatalf("preview: %v", err)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Fatalf("preview %q want %q", out.String(), tc.want)
		}
	}
}
