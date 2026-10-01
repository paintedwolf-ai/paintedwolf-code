package docext_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/promptattach/docext"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTabulaPDF_fixtureQuality(t *testing.T) {
	path := filepath.Join("testdata", "hello.pdf")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	ex := docext.New(testBounds())
	res, err := ex.Extract(context.Background(), docext.Request{
		Filename: "hello.pdf",
		MIME:     "application/pdf",
		Bytes:    raw,
	})
	testutil.FailErr(t, "extract pdf", err)
	if !strings.Contains(strings.ToLower(res.Text), "hello") {
		t.Fatalf("PDF extract quality too low: %q", res.Text)
	}
}
