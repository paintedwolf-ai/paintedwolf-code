package evidence

import "testing"

func TestBuildEvidenceRecordCapturePageSetsDOMSurface(t *testing.T) {
	body := `{"state":{"url":"/"},"snapshot":{"role":"document"},"log":[]}`
	rec := BuildEvidenceRecord("/tmp/proj", "capture_page", map[string]any{"url": "http://127.0.0.1/"}, body)
	if rec.Kind != "page" {
		t.Fatalf("kind = %q want page", rec.Kind)
	}
	if rec.Shape != ShapeSurfaceSnapshot {
		t.Fatalf("shape = %q want %q", rec.Shape, ShapeSurfaceSnapshot)
	}
	if rec.Surface != SurfaceDOM {
		t.Fatalf("surface = %q want %q", rec.Surface, SurfaceDOM)
	}
	if len(rec.Body) == 0 {
		t.Fatal("expected captured body")
	}
}

func TestBuildEvidenceRecordTerminalSnapshotSetsTUISurface(t *testing.T) {
	body := `{"surface":"tui","state":{"cols":80,"rows":24},"snapshot":{"lines":["TITLE"]},"log":[]}`
	rec := BuildEvidenceRecord("/tmp/proj", "terminal_snapshot", map[string]any{"id": "pty-1"}, body)
	if rec.Kind != "terminal_session" {
		t.Fatalf("kind = %q want terminal_session", rec.Kind)
	}
	if rec.Shape != ShapeSurfaceSnapshot {
		t.Fatalf("shape = %q want %q", rec.Shape, ShapeSurfaceSnapshot)
	}
	if rec.Surface != SurfaceTUI {
		t.Fatalf("surface = %q want %q", rec.Surface, SurfaceTUI)
	}
	if len(rec.Body) == 0 {
		t.Fatal("expected captured body")
	}
}

func TestBuildEvidenceRecordCommandTerminalCaptureSetsTUISurface(t *testing.T) {
	body := `{"ok":true,"stages":[{"command":"./ntphealth","exit_code":0}],"terminal_capture":{"surface":"tui","state":{"cols":80,"rows":24},"snapshot":{"lines":["4 succeeded"]}}}`
	rec := BuildEvidenceRecord("/tmp/proj", "command", map[string]any{"terminal_capture": map[string]any{}}, body)
	if rec.Kind != "tui" {
		t.Fatalf("kind = %q want tui", rec.Kind)
	}
	if rec.Shape != ShapeSurfaceSnapshot {
		t.Fatalf("shape = %q want %q", rec.Shape, ShapeSurfaceSnapshot)
	}
	if rec.Surface != SurfaceTUI {
		t.Fatalf("surface = %q want %q", rec.Surface, SurfaceTUI)
	}
	if rec.Fidelity != FidelityStructured {
		t.Fatalf("fidelity = %q want %q", rec.Fidelity, FidelityStructured)
	}
	ok, _ := VerifyRecord(rec, Claim{Excerpt: "4 succeeded"})
	if !ok {
		t.Fatal("expected sealed terminal screen excerpt to verify")
	}
}

func TestBuildEvidenceRecordMeasurePageSetsPageGeometry(t *testing.T) {
	body := `{"units":"css_px","elements":[{"selector":"#box-a","rect":{"width":120}}]}`
	rec := BuildEvidenceRecord("/tmp/proj", "measure_page", map[string]any{"selectors": []any{"#box-a"}}, body)
	if rec.Kind != "page_geometry" {
		t.Fatalf("kind = %q want page_geometry", rec.Kind)
	}
	if rec.Shape != ShapePageGeometry {
		t.Fatalf("shape = %q want %q", rec.Shape, ShapePageGeometry)
	}
	ok, _ := VerifyRecord(rec, Claim{Excerpt: `"width":120`})
	if !ok {
		t.Fatal("expected geometry excerpt to verify")
	}
}

func TestRenderDoesNotSatisfySurfaceAltitude(t *testing.T) {
	render := BuildEvidenceRecord("/tmp/proj", "render_view", nil, `{"ok":true}`)
	page := BuildEvidenceRecord("/tmp/proj", "capture_page", map[string]any{"url": "http://x"}, `{"log":["ok"],"snapshot":{}}`)
	ev := Ledger{Handles: map[string]Record{
		"render#1": render,
		"page#1":   page,
	}}
	if !LedgerHasVisualIntent(ev) {
		t.Fatal("expected visual intent")
	}
	if !LedgerHasSurfaceSnapshot(ev) {
		t.Fatal("expected surface snapshot")
	}
	onlyRender := Ledger{Handles: map[string]Record{"render#1": render}}
	if LedgerHasSurfaceSnapshot(onlyRender) {
		t.Fatal("render must not count as surface_snapshot")
	}
	if !CitationResolvesToVisualIntent("render#1", onlyRender) {
		t.Fatal("expected visual intent citation")
	}
}
