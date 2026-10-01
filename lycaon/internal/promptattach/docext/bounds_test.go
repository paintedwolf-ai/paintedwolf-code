package docext_test

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/promptattach/docext"
	"github.com/lycaon/lycaon/internal/testutil"
)

// testBounds defines the extraction test budget.
func testBounds() docext.Bounds {
	return docext.Bounds{
		MaxPages:             50,
		MaxSlides:            50,
		MaxParse:             15 * time.Second,
		MaxExpansionRatio:    20,
		MaxBodyBytes:         16 << 20,
		MaxExtractedBytes:    16 << 10,
		MaxWorkerMemoryBytes: 512 << 20,
	}
}

func TestODF_noExternalEntityExpansion(t *testing.T) {
	raw := mustODFZip(t, `<?xml version="1.0"?>
<!DOCTYPE doc [
  <!ENTITY xxe SYSTEM "file:///etc/passwd">
]>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
 xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">
 <office:body><office:text><text:p>&xxe;</text:p></office:body>
</office:document-content>`)
	ex := docext.New(testBounds())
	res, err := ex.Extract(context.Background(), docext.Request{
		Filename: "xxe.ods",
		MIME:     "application/vnd.oasis.opendocument.spreadsheet",
		Bytes:    raw,
	})
	if err == nil && strings.Contains(res.Text, "root:") {
		t.Fatal("external entity resolved filesystem content")
	}
}

func TestZipBombRejected(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("content.xml")
	testutil.FailErr(t, "create zip member", err)
	_, err = w.Write(bytes.Repeat([]byte("A"), 200_000))
	testutil.FailErr(t, "write zip member", err)
	testutil.FailErr(t, "close zip", zw.Close())

	bounds := testBounds()
	bounds.MaxExpansionRatio = 2
	ex := docext.New(bounds)
	_, err = ex.Extract(context.Background(), docext.Request{
		Filename: "bomb.ods",
		MIME:     "application/vnd.oasis.opendocument.spreadsheet",
		Bytes:    buf.Bytes(),
	})
	if attacherr.CodeOf(err) != attacherr.CodeTooLarge {
		t.Fatalf("err = %v want ATTACHMENT_TOO_LARGE", err)
	}
}

func TestTabulaZipBombRejectedBeforeWorker(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	testutil.FailErr(t, "create zip member", err)
	_, err = w.Write(bytes.Repeat([]byte("A"), 200_000))
	testutil.FailErr(t, "write zip member", err)
	testutil.FailErr(t, "close zip", zw.Close())

	bounds := testBounds()
	bounds.MaxExpansionRatio = 2
	ex := docext.New(bounds)
	_, err = ex.Extract(context.Background(), docext.Request{
		Filename: "bomb.docx",
		MIME:     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		Bytes:    buf.Bytes(),
	})
	if attacherr.CodeOf(err) != attacherr.CodeTooLarge {
		t.Fatalf("err = %v want ATTACHMENT_TOO_LARGE", err)
	}
}

func TestBuiltInWorkerTimeoutIsKillableAndCleansStaging(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	bounds := testBounds()
	bounds.MaxParse = time.Millisecond
	bounds.MaxBodyBytes = 8 << 20
	raw := []byte(`{\rtf1 ` + strings.Repeat("word ", 1<<20) + `}`)
	started := time.Now()
	_, err := docext.New(bounds).Extract(context.Background(), docext.Request{
		Filename: "slow.rtf", MIME: "application/rtf", Bytes: raw,
	})
	if attacherr.CodeOf(err) != attacherr.CodeTooLarge {
		t.Fatalf("err = %v want ATTACHMENT_TOO_LARGE", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("worker timeout returned after %s", elapsed)
	}
	entries, err := os.ReadDir(tmp)
	testutil.FailErr(t, "read worker temp dir", err)
	if len(entries) != 0 {
		t.Fatalf("timed-out worker left temp entries: %v", entries)
	}
}
