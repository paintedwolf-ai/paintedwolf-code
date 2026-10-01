package visual

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestMemoryStorePutGetRoundTrip(t *testing.T) {
	store := NewMemoryStore()
	root := "root-session"
	wire, err := store.Put(t.Context(), root, Entry{
		Meta: api.VisualArtifact{
			Mime:   "image/png",
			Source: api.VisualArtifactSourceRender,
			PageID: "page-1",
		},
		Bytes: TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "put artifact", err)
	if !wire.StoreRef || wire.ID == "" {
		t.Fatalf("wire = %+v", wire)
	}
	if wire.PageID != "page-1" {
		t.Fatalf("wire page_id = %q want page-1", wire.PageID)
	}
	got := store.Resolve(t.Context(), root, wire.ID)
	if !got.IsPresent() {
		t.Fatalf("resolve: %s", got.Note())
	}
	if got.Meta().Mime != "image/png" || got.Meta().PageID != "page-1" || len(got.Bytes()) != len(TestPNG1x1Bytes()) {
		t.Fatalf("got = %+v", got.Meta())
	}
}

func TestMemoryStoreCrossTreeForeign(t *testing.T) {
	store := NewMemoryStore()
	wire, err := store.Put(t.Context(), "tree-a", Entry{
		Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
		Bytes: TestPNG1x1Bytes(),
	})
	testutil.FailErr(t, "put cross-tree artifact", err)
	res := store.Resolve(t.Context(), "tree-b", wire.ID)
	if res.IsPresent() || res.Reason() != AbsenceForeign {
		t.Fatalf("cross-tree resolve = present:%v reason:%q want foreign", res.IsPresent(), res.Reason())
	}
}

func TestMemoryStoreDropsOldestEntry(t *testing.T) {
	store := NewMemoryStore()
	root := "root"
	chunk := append(TestPNG1x1Bytes(), make([]byte, 512*1024)...)
	var victimID string
	for i := 0; i < 260; i++ {
		wire, err := store.Put(t.Context(), root, Entry{
			Meta:  api.VisualArtifact{Mime: "image/png", Source: api.VisualArtifactSourceRender},
			Bytes: chunk,
		})
		testutil.FailErr(t, "put bounded artifact", err)
		if i == 0 {
			victimID = wire.ID
		}
	}
	res := store.Resolve(t.Context(), root, victimID)
	if res.IsPresent() || res.Reason() != AbsenceUnknown {
		t.Fatalf("dropped resolve = present:%v reason:%q want unknown", res.IsPresent(), res.Reason())
	}
}

func TestAttachToolResultStripsBytesOnWire(t *testing.T) {
	store := NewMemoryStore()
	tr := &api.ToolResult{ToolCallID: "call_1"}
	stamped, err := AttachToolResult(t.Context(), store, "root", "root", "call_1", tr, &tools.VisualCapture{
		Mime:      "image/png",
		Bytes:     TestPNG1x1Bytes(),
		Source:    api.VisualArtifactSourceRender,
		Projected: true,
	}, `{"mime":"image/png"}`)
	testutil.FailErr(t, "attach tool result", err)
	if tr.Visual == nil || !tr.Visual.StoreRef || len(tr.Visual.Bytes) != 0 {
		t.Fatalf("wire visual = %+v", tr.Visual)
	}
	if tr.Visual.ID == "" || !strings.Contains(stamped, `"artifact_id":"`+tr.Visual.ID+`"`) {
		t.Fatalf("stamped content must carry store id: visual=%+v content=%s", tr.Visual, stamped)
	}
}

func TestAttachProjectedCapturePreservesArtifactAndPerceptionBytes(t *testing.T) {
	store := NewMemoryStore()
	tr := &api.ToolResult{ToolCallID: "capture-call"}
	raw := TestPNG1x1Bytes()
	_, err := AttachToolResult(t.Context(), store, "root", "worker", "capture-call", tr, &tools.VisualCapture{
		Mime: "image/png", Bytes: raw, Source: api.VisualArtifactSourceCapture,
		Perceive: true, Projected: true,
	}, `{"mime":"image/png"}`)
	testutil.FailErr(t, "attach projected capture", err)
	if tr.Visual == nil || !tr.Visual.StoreRef || !tr.Visual.Perceive {
		t.Fatalf("capture wire metadata = %+v", tr.Visual)
	}
	resolved := store.Resolve(t.Context(), "root", tr.Visual.ID)
	if !resolved.IsPresent() {
		t.Fatalf("resolve attached capture: %s", resolved.Note())
	}
	if !bytes.Equal(resolved.Bytes(), raw) {
		t.Fatal("artifact store changed the projected pixels before gallery or model resolution")
	}
	if !resolved.Meta().Perceive {
		t.Fatal("stored capture lost its model perception flag")
	}
}

func TestAttachToolResultReferencesAStoredArtifactShownAgain(t *testing.T) {
	store := NewMemoryStore()
	first := &api.ToolResult{ToolCallID: "capture-call"}
	_, err := AttachToolResult(t.Context(), store, "root", "root", "capture-call", first, &tools.VisualCapture{
		Mime: "image/png", Bytes: TestPNG1x1Bytes(), Source: api.VisualArtifactSourceCapture,
		Perceive: true, Projected: true,
	}, `{"mime":"image/png"}`)
	testutil.FailErr(t, "attach capture", err)

	again := &api.ToolResult{ToolCallID: "view-call"}
	stamped, err := AttachToolResult(t.Context(), store, "root", "root", "view-call", again, &tools.VisualCapture{
		Mime: "image/png", Source: api.VisualArtifactSourceCapture, Perceive: true, Projected: true,
		ArtifactID: first.Visual.ID,
	}, `{"format":"png"}`)
	testutil.FailErr(t, "attach stored artifact", err)
	if again.Visual == nil || again.Visual.ID != first.Visual.ID || !again.Visual.StoreRef || !again.Visual.Perceive {
		t.Fatalf("re-view visual = %+v, want a reference to %s", again.Visual, first.Visual.ID)
	}
	if !strings.Contains(stamped, `"artifact_id":"`+first.Visual.ID+`"`) {
		t.Fatalf("stamped content = %s", stamped)
	}
	items, err := store.ListTree(t.Context(), "root")
	testutil.FailErr(t, "list artifacts", err)
	if len(items) != 1 {
		t.Fatalf("artifacts = %d, want the one capture", len(items))
	}
}

func TestAttachToolResultRejectsAMissingStoredArtifact(t *testing.T) {
	tr := &api.ToolResult{ToolCallID: "view-call"}
	_, err := AttachToolResult(t.Context(), NewMemoryStore(), "root", "root", "view-call", tr, &tools.VisualCapture{
		Mime: "image/png", Source: api.VisualArtifactSourceCapture, Perceive: true, Projected: true, ArtifactID: "gone",
	}, `{}`)
	if err == nil || tr.Visual != nil {
		t.Fatalf("missing artifact: err=%v visual=%+v", err, tr.Visual)
	}
}

func TestAttachToolResultRejectsUnprojectedCapture(t *testing.T) {
	store := NewMemoryStore()
	tr := &api.ToolResult{ToolCallID: "call_unsafe"}
	_, err := AttachToolResult(t.Context(), store, "root", "root", "call_unsafe", tr, &tools.VisualCapture{
		Mime:   "image/png",
		Bytes:  TestPNG1x1Bytes(),
		Source: api.VisualArtifactSourceCapture,
	}, `{"mime":"image/png"}`)
	if err == nil || !strings.Contains(err.Error(), "not a safe projection") {
		t.Fatalf("unprojected capture error = %v", err)
	}
	if tr.Visual != nil {
		t.Fatalf("unsafe capture was stamped onto the tool result: %+v", tr.Visual)
	}
}
