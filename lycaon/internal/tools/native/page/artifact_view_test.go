package page

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/visual"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestImageArtifactUsesResolvedRootAndKeepsStoredRasterReference(t *testing.T) {
	store := visual.NewMemoryStore()
	artifact, err := store.Put(t.Context(), "root", visual.Entry{Meta: wire.VisualArtifact{Mime: "image/png", Source: wire.VisualArtifactSourceCapture, Caption: "fixture", Perceive: true}, Bytes: visual.TestPNG1x1Bytes()})
	if err != nil {
		testutil.FailErr(t, "store image artifact", err)
	}
	effect := &tools.ToolInvocationOut{}
	tc := tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: "child"}, Effects: tools.InvocationEffects{Out: effect}}
	handler := ViewImageHandler(ViewImageDeps{VisualStore: store, RootSessionID: func(_ context.Context, id string) string {
		if id != "child" {
			t.Fatal("incorrect session lookup")
		}
		return "root"
	}})
	raw, err := handler(t.Context(), map[string]any{"handle": artifact.ID}, tc)
	if err != nil || !json.Valid([]byte(raw)) || effect.Visual == nil || effect.Visual.ArtifactID != artifact.ID || len(effect.Visual.Bytes) != 0 || effect.Visual.Caption != "fixture" {
		t.Fatalf("artifact reference lost: raw=%q err=%v visual=%+v", raw, err, effect.Visual)
	}
	effect.Visual = nil
	foreign := ViewImageHandler(ViewImageDeps{VisualStore: store})
	raw, err = foreign(t.Context(), map[string]any{"handle": artifact.ID}, tc)
	var reject *toolrejection.ToolReject
	if raw != "" || !errors.As(err, &reject) || reject.Code != "RENDER_HANDLE_NOT_FOUND" || effect.Visual != nil {
		t.Fatalf("foreign root exposed artifact: raw=%q err=%v", raw, err)
	}
}
