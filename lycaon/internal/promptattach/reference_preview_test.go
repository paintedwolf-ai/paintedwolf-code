package promptattach

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/pkg/api"
)

type endlessReferenceText struct{ read int }

func (r *endlessReferenceText) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	r.read += len(p)
	return len(p), nil
}

func TestExternalReferencePreviewBoundsReadsAndMarksTruncation(t *testing.T) {
	reader := &endlessReferenceText{}
	budget := &TurnPreviewBudget{remaining: 80, maxBodyPreview: 80, maxLargeText: 80, largeTextThreshold: 1}
	fence := externalReferencePreview(reader, "big.txt", "text/plain", "[User attached file: /big.txt]", 1<<40, 0, 0, budget)
	if reader.read > 84 || budget.remaining != 0 {
		t.Fatalf("read=%d remaining=%d", reader.read, budget.remaining)
	}
	if !strings.Contains(fence, `truncated="true"`) || !strings.Contains(fence, "1099511627776 bytes") {
		t.Fatalf("fence=%s", fence)
	}
}

func TestExternalReferenceSelectionStreamsToRequestedLines(t *testing.T) {
	budget := &TurnPreviewBudget{remaining: 100, maxBodyPreview: 100, maxLargeText: 100, largeTextThreshold: 1}
	fence := externalReferencePreview(strings.NewReader("one\ntwo\nthree\nfour"), "notes.txt", "text/plain", "[User attached file: /notes.txt:2-3]", 18, 2, 3, budget)
	if !strings.Contains(fence, "two\nthree") || strings.Contains(fence, "four") {
		t.Fatalf("fence=%s", fence)
	}
	reader := &endlessReferenceText{}
	if got := selectedReferencePreview(reader, "big.txt", "text/plain", "hint", 2, 3, 80); got != "" {
		t.Fatalf("long line preview=%s", got)
	}
	if reader.read > 80 {
		t.Fatalf("unbounded line read=%d", reader.read)
	}
}

func TestExternalReferenceRejectsSpecialFile(t *testing.T) {
	if !filepath.IsAbs(os.DevNull) {
		return
	}
	_, err := IngestReferences(ReferenceDeps{SessionProjectID: "project"}, nil, []api.PromptReferencePart{
		api.NewPromptReferenceExternalFile(api.PromptReferenceExternalFilePart{ProjectID: "project", Path: os.DevNull}),
	})
	if attacherr.CodeOf(err) != attacherr.CodeUnsupported {
		t.Fatalf("special file err=%v", err)
	}
}

var _ io.Reader = (*endlessReferenceText)(nil)
