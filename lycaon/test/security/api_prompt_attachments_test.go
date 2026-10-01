package security

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// uploadAttachment returns the receipt and HTTP response. mimeHint is the
// file's own media type, sent as the `mime` detection hint.
func uploadAttachment(t *testing.T, srv *api.Server, projectID, filename, mimeHint string, body []byte) (wire.AttachmentUploadResponse, *httptest.ResponseRecorder) {
	t.Helper()
	target := "/v1/projects/" + projectID + "/attachments?filename=" + filename
	if mimeHint != "" {
		target += "&mime=" + url.QueryEscape(mimeHint)
	}
	req := authedRequest(t, http.MethodPost, target, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	var out wire.AttachmentUploadResponse
	if w.Code == http.StatusCreated {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode upload receipt: %v (body %s)", err, w.Body.String())
		}
	}
	return out, w
}

func sendPromptWithBlobIDs(t *testing.T, srv *api.Server, sessionID string, blobIDs ...string) *httptest.ResponseRecorder {
	t.Helper()
	parts := make([]map[string]any, 0, len(blobIDs))
	for _, id := range blobIDs {
		parts = append(parts, map[string]any{"blob_id": id})
	}
	payload, err := json.Marshal(map[string]any{"text": "review this", "attachments": parts})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sessionID+"/prompts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestPromptTextAttachmentAcceptedAndFenced(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	receipt, up := uploadAttachment(t, srv, sess.ProjectID, "main.go", "text/plain", []byte("package main\n"))
	if up.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body = %s", up.Code, up.Body.String())
	}
	if receipt.Kind != wire.AttachmentKindText {
		t.Fatalf("kind = %q want text", receipt.Kind)
	}
	if w := sendPromptWithBlobIDs(t, srv, sess.ID, receipt.BlobID); w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestPromptAttachmentUploadRejectsUnknownProject(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	_, up := uploadAttachment(t, srv, "00000000-0000-4000-8000-000000000001", "note.txt", "text/plain", []byte("hello"))
	if up.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", up.Code, up.Body.String())
	}
	if code := decodeAPIError(t, up).Code; code != "project_not_found" {
		t.Fatalf("code = %q want project_not_found", code)
	}
}

func TestRejectedPromptDoesNotLeaveVisualArtifact(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sess := createSessionHTTP(t, srv, t.TempDir())
	receipt, up := uploadAttachment(t, srv, sess.ProjectID, "image.png", "image/png", visual.TestPNG1x1Bytes())
	if up.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body = %s", up.Code, up.Body.String())
	}
	payload, err := json.Marshal(map[string]any{
		"text":        "review this",
		"attachments": []map[string]any{{"blob_id": receipt.BlobID}},
		"references": []map[string]any{{
			"kind": "path-file", "project_id": sess.ProjectID, "path": "../../escape.txt",
		}},
	})
	if err != nil {
		t.Fatalf("marshal prompt: %v", err)
	}
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rejected := httptest.NewRecorder()
	srv.ServeHTTP(rejected, req)
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("prompt status = %d body = %s", rejected.Code, rejected.Body.String())
	}
	galleryReq := authedRequest(t, http.MethodGet, "/v1/projects/"+sess.ProjectID+"/artifacts", nil)
	gallery := httptest.NewRecorder()
	srv.ServeHTTP(gallery, galleryReq)
	if gallery.Code != http.StatusOK {
		t.Fatalf("gallery status = %d body = %s", gallery.Code, gallery.Body.String())
	}
	var listed wire.ArtifactListResponse
	if err := json.Unmarshal(gallery.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode gallery: %v", err)
	}
	if len(listed.Artifacts) != 0 {
		t.Fatalf("rejected prompt left artifacts: %+v", listed.Artifacts)
	}
}

// Binary content cannot claim the text route.
func TestPromptTextAttachmentRejectsBinaryContent(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	_, up := uploadAttachment(t, srv, sess.ProjectID, "evil.txt", "text/plain", []byte{0x00, 0x01, 0xff})
	if up.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", up.Code, up.Body.String())
	}
	if code := decodeAPIError(t, up).Code; code != "unsupported_attachment" {
		t.Fatalf("code = %q want unsupported_attachment", code)
	}
}

// URL-shaped filenames remain inert names.
func TestPromptAttachmentURLFilenameIsSanitizedNotFetched(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	receipt, up := uploadAttachment(t, srv, sess.ProjectID, "https:%2F%2Fevil.example%2Fx.txt", "text/plain", []byte("hello"))
	if up.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", up.Code, up.Body.String())
	}
	if strings.ContainsAny(receipt.Filename, "/\\") {
		t.Fatalf("stored filename %q must be a path-free leaf", receipt.Filename)
	}
}

func TestPromptAttachmentTooMany(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	const maxAttachments = 8 // prompt_attachments.counts.max_attachments (host-contract §11)
	blobIDs := make([]string, 0, maxAttachments+1)
	for i := 0; i <= maxAttachments; i++ {
		// Distinct bodies, so content addressing does not collapse them to one blob.
		receipt, up := uploadAttachment(t, srv, sess.ProjectID, fmt.Sprintf("f%d.txt", i), "text/plain", []byte(fmt.Sprintf("body %d", i)))
		if up.Code != http.StatusCreated {
			t.Fatalf("upload %d status = %d body = %s", i, up.Code, up.Body.String())
		}
		blobIDs = append(blobIDs, receipt.BlobID)
	}
	w := sendPromptWithBlobIDs(t, srv, sess.ID, blobIDs...)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if code := decodeAPIError(t, w).Code; code != "attachment_too_large" {
		t.Fatalf("code = %q want attachment_too_large", code)
	}
}

// Unknown blob IDs use a distinct rejection code.
func TestPromptAttachmentUnknownBlobIDRejected(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	w := sendPromptWithBlobIDs(t, srv, sess.ID, strings.Repeat("b", 64))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if code := decodeAPIError(t, w).Code; code != "attachment_not_found" {
		t.Fatalf("code = %q want attachment_not_found", code)
	}
}

// Blob IDs cannot address filesystem paths.
func TestPromptAttachmentBlobIDTraversalRejected(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	for _, id := range []string{"../../../etc/passwd", "..", "/etc/passwd"} {
		w := sendPromptWithBlobIDs(t, srv, sess.ID, id)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("blob_id %q: status = %d body = %s", id, w.Code, w.Body.String())
		}
	}
}

// Single-body compression is decoded at intake.
func TestPromptAttachmentGzipDecodedAtUpload(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	sess := createSessionHTTP(t, srv, t.TempDir())

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"trace":[1,2,3]}`)); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}

	receipt, up := uploadAttachment(t, srv, sess.ProjectID, "recording.json.gz", "application/gzip", buf.Bytes())
	if up.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", up.Code, up.Body.String())
	}
	if receipt.Filename != "recording.json" || receipt.Mime != "application/json" {
		t.Fatalf("receipt = %+v want recording.json / application/json", receipt)
	}
	if w := sendPromptWithBlobIDs(t, srv, sess.ID, receipt.BlobID); w.Code != http.StatusAccepted {
		t.Fatalf("prompt status = %d body = %s", w.Code, w.Body.String())
	}
}
