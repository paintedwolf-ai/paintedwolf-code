package contract

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/promptattach/attacherr"
	"github.com/lycaon/lycaon/internal/tooloutput"
	wire "github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func attachFixture(t *testing.T) (blobstore.Store, promptattach.Caps) {
	t.Helper()
	caps, err := promptattach.LoadCaps()
	contractcheck.FailErr(t, "load bundled attachment caps", err)
	return blobstore.Store{Root: t.TempDir(), Dir: tooloutput.AttachmentSpillDir}, caps
}

// minimalZip is a valid, empty zip container — enough to prove routing without
// standing up a real office package.
func minimalZip(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	contractcheck.FailErr(t, "create zip member", err)
	_, err = w.Write([]byte("<w:document/>"))
	contractcheck.FailErr(t, "write zip member", err)
	contractcheck.FailErr(t, "close zip", zw.Close())
	return buf.Bytes()
}

func upload(t *testing.T, store blobstore.Store, caps promptattach.Caps, name, mime string, body []byte) promptattach.Receipt {
	t.Helper()
	receipt, err := promptattach.Upload(context.Background(), store, caps, nil, name, mime, bytes.NewReader(body))
	contractcheck.FailErr(t, "upload "+name, err)
	return receipt
}

// Non-image attachments stay off the visual plane.
func TestPromptAttachmentVisualPlaneInvariant(t *testing.T) {
	t.Parallel()
	store, caps := attachFixture(t)
	cases := []struct {
		name, filename, mime string
		body                 []byte
	}{
		{"text family", "notes.txt", "text/plain", []byte("hello attachment")},
		{"rtf document", "note.rtf", "application/rtf", []byte(`{\rtf1\ansi\pard hello\par}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			receipt := upload(t, store, caps, tc.filename, tc.mime, tc.body)
			res, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), nil,
				[]wire.PromptAttachmentPart{{BlobID: receipt.BlobID}})
			contractcheck.FailErr(t, "ingest", err)
			if len(res.Fences()) != 1 {
				t.Fatalf("fences = %d want 1", len(res.Fences()))
			}
			if len(res.Images) != 0 {
				t.Fatalf("Images=%d; non-image content must stay off the Visual plane", len(res.Images))
			}
		})
	}
}

// URL-shaped filenames remain inert attachment names.
func TestPromptAttachmentFilenameIsNeverALocator(t *testing.T) {
	t.Parallel()
	store, caps := attachFixture(t)
	for _, tc := range []struct {
		name, mime string
		body       []byte
	}{
		{"https://example.com/secret.pdf", "application/pdf", []byte("%PDF-1.7\n%%EOF\n")},
		{"http://evil.example/a.txt", "text/plain", []byte("x")},
		{"file:///etc/passwd", "text/plain", []byte("x")},
		{"../../../../etc/passwd", "text/plain", []byte("x")},
	} {
		receipt := upload(t, store, caps, tc.name, tc.mime, tc.body)
		if strings.ContainsAny(receipt.Filename, "/\\") || strings.Contains(receipt.Filename, "..") {
			t.Fatalf("filename %q stored as %q; must reduce to a path-free leaf", tc.name, receipt.Filename)
		}
	}
}

// Content decides, not the extension: a binary body named .txt is refused.
func TestPromptAttachmentSniff_notExtensionTrust(t *testing.T) {
	t.Parallel()
	store, caps := attachFixture(t)
	_, err := promptattach.Upload(context.Background(), store, caps, nil, "evil.txt", "text/plain",
		bytes.NewReader([]byte{0x00, 0x01, 0xff, 0xfe}))
	if attacherr.CodeOf(err) != attacherr.CodeUnsupported {
		t.Fatalf("err=%v want UNSUPPORTED_ATTACHMENT", err)
	}
}

// Every payload fence names the full body's path, whether or not the preview was
// clipped: a body that fits inline is not therefore cheap to re-read, and a
// structured body cannot be queried without one.
func TestPromptAttachmentFenceAlwaysCarriesPath(t *testing.T) {
	t.Parallel()
	store, caps := attachFixture(t)
	receipt := upload(t, store, caps, "small.json", "application/json", []byte(`{"a":1}`))
	res, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), nil,
		[]wire.PromptAttachmentPart{{BlobID: receipt.BlobID}})
	contractcheck.FailErr(t, "ingest", err)
	fence := res.Fences()[0]
	if !strings.Contains(fence, `path="`+tooloutput.AttachmentSpillDir+"/") {
		t.Fatalf("fence has no host-data path attr:\n%s", fence)
	}
	part := res.Parts[0]
	if part.Path == "" || !strings.HasPrefix(part.Path, tooloutput.AttachmentSpillDir+"/") {
		t.Fatalf("FramedPart.Path = %q; payload must stamp a host-data path", part.Path)
	}
	if part.SizeBytes <= 0 {
		t.Fatalf("FramedPart.SizeBytes = %d; payload must stamp full body size", part.SizeBytes)
	}
	if !strings.Contains(fence, `truncated="false"`) {
		t.Fatalf("small body should not be marked truncated:\n%s", fence)
	}
	if !strings.Contains(fence, "jq(path=") {
		t.Fatalf("structured body should be pointed at jq:\n%s", fence)
	}
}

// A single-member container is decoded away at intake, so the prompt sees the
// inner file under its own name and type.
func TestPromptAttachmentUnwrapsSingleMemberContainer(t *testing.T) {
	t.Parallel()
	store, caps := attachFixture(t)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write([]byte(`{"records":[1,2,3]}`))
	contractcheck.FailErr(t, "write gzip member", err)
	contractcheck.FailErr(t, "close gzip", zw.Close())

	receipt := upload(t, store, caps, "trace.json.gz", "application/gzip", buf.Bytes())
	if receipt.Filename != "trace.json" {
		t.Fatalf("Filename = %q want trace.json", receipt.Filename)
	}
	if receipt.MIME != "application/json" {
		t.Fatalf("MIME = %q want application/json", receipt.MIME)
	}
}

func TestPromptAttachmentOfficePackageKeepsItsFormat(t *testing.T) {
	t.Parallel()
	store, caps := attachFixture(t)
	receipt, err := promptattach.Upload(context.Background(), store, caps, nil, "report.docx",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		bytes.NewReader(minimalZip(t)))
	contractcheck.FailErr(t, "upload docx", err)
	if receipt.Filename != "report.docx" || receipt.MIME != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" {
		t.Fatalf("receipt = %+v want a docx document", receipt)
	}
}

// Unknown blob IDs have a distinct rejection code.
func TestPromptAttachmentUnknownBlobIDRejects(t *testing.T) {
	t.Parallel()
	store, caps := attachFixture(t)
	_, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), nil,
		[]wire.PromptAttachmentPart{{BlobID: strings.Repeat("a", 64)}})
	if attacherr.CodeOf(err) != attacherr.CodeNotFound {
		t.Fatalf("err=%v want ATTACHMENT_NOT_FOUND", err)
	}
}

// Blob IDs cannot address filesystem paths.
func TestPromptAttachmentBlobIDCannotTraverse(t *testing.T) {
	t.Parallel()
	store, caps := attachFixture(t)
	for _, id := range []string{"../../etc", "..", "/etc/passwd", strings.Repeat("z", 64)} {
		_, err := promptattach.IngestAttachments(context.Background(), store, caps, promptattach.NewTurnPreviewBudget(caps), nil,
			[]wire.PromptAttachmentPart{{BlobID: id}})
		if attacherr.CodeOf(err) != attacherr.CodeNotFound {
			t.Fatalf("blob_id %q: err=%v want ATTACHMENT_NOT_FOUND", id, err)
		}
	}
}
