package promptadmin

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/promptattach"
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
	srv := &Handler{Caps: promptattach.Active(), Deps: Deps{VisualStore: failingVisualStore{}}}
	ids, err := srv.ingestPromptImages(t.Context(), "session-1", operationID, group, []promptattach.InlineImage{{
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
