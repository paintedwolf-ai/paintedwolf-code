package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textfile"
)

func TestCodeReadPreservesTextEncodings(t *testing.T) {
	for _, encoding := range []string{textfile.UTF8, textfile.UTF8BOM, textfile.UTF16LEBOM, textfile.UTF16BEBOM} {
		t.Run(encoding, func(t *testing.T) {
			const text = "hit café\n"
			raw, err := textfile.EncodeBounded(text, encoding, textfile.LimitsForRaw(1024))
			testutil.FailErr(t, "encode read fixture", err)
			path := filepath.Join(t.TempDir(), "generated.txt")
			testutil.FailErr(t, "write read fixture", os.WriteFile(path, raw, 0o600))
			content, status := readCodeContent(path, 1024)
			if status != codeOpenOK || content.text() != text {
				t.Fatalf("read status=%v text=%q", status, content.text())
			}
			doc, status := openCodeDocument(path, 1024)
			if status != codeOpenOK || doc.Encoding() != encoding || doc.RawSHA256() != textfile.SHA256(raw) {
				t.Fatalf("document status=%v encoding=%q digest=%q", status, doc.Encoding(), doc.RawSHA256())
			}
		})
	}
}

func TestPreviewReplaceReturnsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := settledReplacePreview(ctx, ReplacePreviewRequest{Query: TextExpr{Text: "hit"}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("preview error=%v, want cancellation", err)
	}
}
