package promptadmin_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api/contractfixture"
	"github.com/lycaon/lycaon/internal/blobstore"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestUploadedAttachmentIsReusableOnlyInItsProject(t *testing.T) {
	server, _, session := contractfixture.NewCheckpointHandlerFixture(t)
	server.Admin.SessionAdmin.Lifecycle.Sessions.SetDataDir(t.TempDir())
	other := contractfixture.CreateProjectForTest(t, server, t.TempDir())
	const body = "retained attachment bytes\n"
	upload := func() string {
		t.Helper()
		req := contractfixture.NewAuthedRequest(http.MethodPost, "/v1/projects/"+session.ProjectID+"/attachments?filename=notes.txt", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/octet-stream")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("upload = %d %s", w.Code, w.Body.String())
		}
		var receipt wire.AttachmentUploadResponse
		testutil.FailErr(t, "decode receipt", json.Unmarshal(w.Body.Bytes(), &receipt))
		if receipt.Bytes != int64(len(body)) || receipt.Kind != wire.AttachmentKindText || receipt.BlobID == "" {
			t.Fatalf("upload receipt = %+v", receipt)
		}
		return receipt.BlobID
	}
	id := upload()
	if replay := upload(); replay != id {
		t.Fatalf("repeated upload changed blob ID from %q to %q", id, replay)
	}
	storage := func(id string) blobstore.Store {
		t.Helper()
		s, ok := server.Admin.Prompt.Attachments.AttachmentStore(t.Context(), id)
		if !ok {
			t.Fatal("project attachment store unavailable")
		}
		return s
	}
	source := storage(session.ProjectID)
	blob, err := source.Resolve(id)
	testutil.FailErr(t, "resolve staged attachment", err)
	reader, err := source.Open(blob)
	testutil.FailErr(t, "open staged attachment", err)
	got, err := io.ReadAll(reader)
	testutil.FailErr(t, "read staged attachment", err)
	testutil.FailErr(t, "close staged attachment", reader.Close())
	if string(got) != body {
		t.Fatalf("staged bytes = %q", got)
	}
	if _, err := storage(other.ID).Resolve(id); !errors.Is(err, blobstore.ErrNotFound) {
		t.Fatalf("other project lookup = %v", err)
	}
}
