package promptadmin

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/bytebound"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

type failingVisualStore struct{ visual.Store }

func (failingVisualStore) Put(context.Context, string, visual.Entry) (wire.VisualArtifact, error) {
	return wire.VisualArtifact{}, errors.New("injected visual write failure")
}

func TestIngestPromptImagesReturnsFailingArtifactID(t *testing.T) {
	const (
		operationID = "operation-1"
		group       = "attached"
	)
	srv := &Handler{Submission: &Submission{Caps: promptattach.Active(), VisualStore: failingVisualStore{}}}
	ids, err := srv.Submission.ingestPromptImages(t.Context(), "session-1", operationID, group, []promptattach.InlineImage{{
		Mime:  "image/png",
		Bytes: visual.TestPNG1x1Bytes(),
	}})
	if err == nil {
		t.Fatal("expected visual write failure")
	}
	want := uuid.NewSHA1(uuid.NameSpaceOID, []byte(operationID+":"+group+":0")).String()
	if len(ids) != 1 || ids[0] != want {
		t.Fatalf("artifact ids = %v, want [%s]", ids, want)
	}
}

func TestPromptAdmissionProjectsEachImageBlobOnce(t *testing.T) {
	handler, sessions, attachments, sess := newPromptAttachmentRecoveryFixture(t)
	handler.Submission.Caps = promptattach.Active()
	handler.Submission.References = &References{Caps: handler.Submission.Caps}
	handler.Submission.Store = sessions
	handler.Submission.VisualStore = visual.NewMemoryStore()
	input := wire.PromptRequest{OperationID: uuid.NewString(), Text: "Compare the attachments."}
	for i, name := range []string{"first.png", "second.png"} {
		pixels := image.NewRGBA(image.Rect(0, 0, 1, 1))
		pixels.SetRGBA(0, 0, color.RGBA{R: uint8(i), A: 255})
		var body bytes.Buffer
		testutil.FailErr(t, "encode image", png.Encode(&body, pixels))
		blob, err := attachments.Put(name, &body, bytebound.Materialization(4096))
		testutil.FailErr(t, "stage image", err)
		input.Attachments = append(input.Attachments, wire.PromptAttachmentPart{BlobID: blob.ID})
	}
	input.Attachments = append(input.Attachments, wire.PromptAttachmentPart{BlobID: " " + input.Attachments[0].BlobID + " "})
	response := httptest.NewRecorder()
	prepared, ok := handler.Submission.preparePromptAdmission(
		response, httptest.NewRequest("POST", "/", nil),
		sess.ID, sess, input, attachments, input.Text, input.Text, false,
	)
	if !ok {
		t.Fatalf("prepare admission: %d %s", response.Code, response.Body.String())
	}
	if len(prepared.input.ArtifactIDs) != 2 {
		t.Fatalf("artifact IDs = %v", prepared.input.ArtifactIDs)
	}
	var blobIDs []string
	for _, part := range prepared.input.ContentParts {
		if part.BlobID != "" {
			index := len(blobIDs)
			if index >= len(prepared.input.ArtifactIDs) || part.ArtifactID != prepared.input.ArtifactIDs[index] {
				t.Fatalf("attachment projection at index %d has artifact %q", index, part.ArtifactID)
			}
			blobIDs = append(blobIDs, part.BlobID)
		}
	}
	if !slices.Equal(blobIDs, []string{input.Attachments[0].BlobID, input.Attachments[1].BlobID}) {
		t.Fatalf("attachment blob IDs = %v", blobIDs)
	}
}
