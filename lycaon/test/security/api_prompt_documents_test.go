package security

import (
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"
)

func TestPromptDocumentRTFAccepted(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	dir := t.TempDir()
	sess := createSessionHTTP(t, srv, dir)

	rtf := []byte(`{\rtf1\ansi\deff0{\fonttbl{\f0 Arial;}}\pard hello from rtf\par}`)
	receipt, upload := uploadAttachment(t, srv, sess.ProjectID, "note.rtf", "application/rtf", rtf)
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body = %s", upload.Code, upload.Body.String())
	}
	w := sendPromptWithBlobIDs(t, srv, sess.ID, receipt.BlobID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestPromptDocumentMalformedRejected(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	dir := t.TempDir()
	sess := createSessionHTTP(t, srv, dir)

	receipt, upload := uploadAttachment(t, srv, sess.ProjectID, "bad.pdf", "application/pdf", []byte("%PDF-not-really"))
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body = %s", upload.Code, upload.Body.String())
	}
	w := sendPromptWithBlobIDs(t, srv, sess.ID, receipt.BlobID)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	errResp := decodeAPIError(t, w)
	if errResp.Code != "unsupported_attachment" {
		t.Fatalf("code = %q", errResp.Code)
	}
}
