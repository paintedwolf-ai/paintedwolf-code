package docext_test

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/promptattach/docext"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRTFStripsMarkup(t *testing.T) {
	ex := docext.New(testBounds())
	rtf := []byte(`{\rtf1\ansi\deff0{\fonttbl{\f0 Arial;}}\pard hello\par world\par}`)
	res, err := ex.Extract(context.Background(), docext.Request{
		Filename: "note.rtf",
		MIME:     "application/rtf",
		Bytes:    rtf,
	})
	testutil.FailErr(t, "extract rtf", err)
	if !strings.Contains(res.Text, "hello") || !strings.Contains(res.Text, "world") {
		t.Fatalf("text = %q", res.Text)
	}
}

func TestRTFDecodesANSIAndUnicodeEscapes(t *testing.T) {
	ex := docext.New(testBounds())
	rtf := []byte(`{\rtf1\ansi\ansicpg1252 caf\'e9 \u26481?}`)
	res, err := ex.Extract(context.Background(), docext.Request{
		Filename: "unicode.rtf", MIME: "application/rtf", Bytes: rtf,
	})
	testutil.FailErr(t, "extract unicode rtf", err)
	if res.Text != "café 東" {
		t.Fatalf("text = %q want %q", res.Text, "café 東")
	}
}

func TestDocumentExtractor_boundsReturnedTextAtUTF8Boundary(t *testing.T) {
	bounds := testBounds()
	bounds.MaxExtractedBytes = len("café")
	ex := docext.New(bounds)
	rtf := []byte(`{\rtf1\ansi\ansicpg1252 caf\'e9 plus more}`)
	res, err := ex.Extract(context.Background(), docext.Request{
		Filename: "bounded.rtf", MIME: "application/rtf", Bytes: rtf,
	})
	testutil.FailErr(t, "extract bounded rtf", err)
	if res.Text != "café" {
		t.Fatalf("text = %q want %q", res.Text, "café")
	}
}

func TestODSDelimitedText(t *testing.T) {
	ex := docext.New(testBounds())
	raw := mustODFZip(t, `<?xml version="1.0"?>
<office:document-content xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0"
 xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0"
 xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0">
 <office:body><office:spreadsheet>
  <table:table>
   <table:table-row>
    <table:table-cell><text:p>a</text:p></table:table-cell>
    <table:table-cell><text:p>b</text:p></table:table-cell>
   </table:table-row>
  </table:table>
 </office:spreadsheet></office:body>
</office:document-content>`)
	res, err := ex.Extract(context.Background(), docext.Request{
		Filename: "sheet.ods",
		MIME:     "application/vnd.oasis.opendocument.spreadsheet",
		Bytes:    raw,
	})
	testutil.FailErr(t, "extract ods", err)
	if !strings.Contains(res.Text, "a") || !strings.Contains(res.Text, "b") {
		t.Fatalf("text = %q", res.Text)
	}
}

func mustODFZip(t *testing.T, contentXML string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("content.xml")
	testutil.FailErr(t, "create content.xml", err)
	_, err = w.Write([]byte(contentXML))
	testutil.FailErr(t, "write content.xml", err)
	testutil.FailErr(t, "close zip", zw.Close())
	return buf.Bytes()
}
