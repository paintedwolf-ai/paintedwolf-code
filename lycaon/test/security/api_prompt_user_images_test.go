package security

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"testing"

	"github.com/lycaon/lycaon/test/wiring"
)

// Active markup cannot enter the image route.
func TestPromptUserImageRejectsActiveSVG(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	dir := t.TempDir()
	sess := createSessionHTTP(t, srv, dir)

	_, w := uploadAttachment(t, srv, sess.ProjectID, "active.png", "image/png",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	errResp := decodeAPIError(t, w)
	if errResp.Code != "unsupported_attachment" {
		t.Fatalf("code = %q want unsupported_attachment", errResp.Code)
	}
}

func TestPromptUserImageRejectsURLMime(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	dir := t.TempDir()
	sess := createSessionHTTP(t, srv, dir)

	_, w := uploadAttachment(t, srv, sess.ProjectID, "remote.url", "text/uri-list",
		[]byte("https://evil.example/x.png"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestPromptUserImageAcceptsSafePNG(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	dir := t.TempDir()
	sess := createSessionHTTP(t, srv, dir)

	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	receipt, upload := uploadAttachment(t, srv, sess.ProjectID, "safe.png", "image/png", buf.Bytes())
	if upload.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body = %s", upload.Code, upload.Body.String())
	}
	w := sendPromptWithBlobIDs(t, srv, sess.ID, receipt.BlobID)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
}
