package security

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

func TestPromptReferencePathFileAccepted(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# hi\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	proj := createProjectHTTP(t, srv, dir)
	sess := createSessionForProjectHTTP(t, srv, proj.ID, wire.SessionPostureBuild)
	rootID := proj.Roots[0].ID

	payload, err := json.Marshal(map[string]any{
		"text": "look at this",
		"references": []map[string]any{
			{
				"kind":       "path-file",
				"project_id": proj.ID,
				"root_id":    rootID,
				"path":       "readme.md",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}

	var messages []wire.Message
	if !testutil.WaitForNoFatal(5*time.Second, func() bool {
		msgs := listMessagesHTTP(t, srv, sess.ID)
		messages = msgs
		for _, m := range msgs {
			if m.Role == wire.MessageRoleUser && strings.Contains(m.Content, "[User attached file: readme.md]") {
				return true
			}
		}
		return false
	}) {
		t.Fatalf("timed out waiting for reference fence; messages=%+v", messages)
	}
}

func TestPromptReferencePathOutOfJail(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	dir := t.TempDir()
	proj := createProjectHTTP(t, srv, dir)
	sess := createSessionForProjectHTTP(t, srv, proj.ID, wire.SessionPostureBuild)

	payload, err := json.Marshal(map[string]any{
		"text": "x",
		"references": []map[string]any{
			{
				"kind":       "path-file",
				"project_id": proj.ID,
				"root_id":    proj.Roots[0].ID,
				"path":       "../outside.txt",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	errResp := decodeAPIError(t, w)
	if errResp.Code != "reference_out_of_jail" {
		t.Fatalf("code = %q want reference_out_of_jail", errResp.Code)
	}
}

func TestPromptReferenceArtifactRelink(t *testing.T) {
	h := wiring.BuildForTest(t)
	srv := h.Server
	visualStore := h.Boards.Visual
	dir := t.TempDir()
	proj := createProjectHTTP(t, srv, dir)
	sess := createSessionForProjectHTTP(t, srv, proj.ID, wire.SessionPostureBuild)

	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	wireArt, err := visual.IngestUserImage(t.Context(), visualStore, sess.ID, uuid.NewString(), "image/png", buf.Bytes())
	if err != nil {
		t.Fatalf("IngestUserImage: %v", err)
	}

	payload, err := json.Marshal(map[string]any{
		"text": "again",
		"references": []map[string]any{
			{
				"kind":        "artifact",
				"project_id":  proj.ID,
				"artifact_id": wireArt.ID,
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}

	var messages []wire.Message
	if !testutil.WaitForNoFatal(5*time.Second, func() bool {
		msgs := listMessagesHTTP(t, srv, sess.ID)
		messages = msgs
		for _, m := range msgs {
			if m.Role == wire.MessageRoleUser {
				for _, id := range m.ArtifactIDs {
					if id == wireArt.ID {
						return true
					}
				}
			}
		}
		return false
	}) {
		t.Fatalf("timed out waiting for artifact re-link; messages=%+v", messages)
	}
}

func TestPromptReferenceSearchHitMissing(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	dir := t.TempDir()
	proj := createProjectHTTP(t, srv, dir)
	sess := createSessionForProjectHTTP(t, srv, proj.ID, wire.SessionPostureBuild)

	payload, err := json.Marshal(map[string]any{
		"text": "x",
		"references": []map[string]any{
			{
				"kind":       "search-hit",
				"project_id": proj.ID,
				"source_ref": "missing-ref",
				"hit_kind":   "message",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := authedRequest(t, http.MethodPost, "/v1/sessions/"+sess.ID+"/prompts", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	errResp := decodeAPIError(t, w)
	if errResp.Code != "unsupported_attachment" {
		t.Fatalf("code = %q want unsupported_attachment", errResp.Code)
	}
}
