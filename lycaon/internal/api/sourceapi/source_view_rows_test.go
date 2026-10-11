package sourceapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/pagedview"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestSourceViewComparisonFramesPreserveSourceAnchors(t *testing.T) {
	server := newSourceHandlerFixture(t)
	defer server.Views.sourceReaders.Close()
	p := &project.Project{ID: uuid.NewString()}
	request := wire.SourceComparisonViewCreate{Kind: "comparison", ClientID: uuid.NewString(), OperationID: uuid.NewString(), Intent: wire.SourceComparisonIntent{Mode: "full"}}
	view := server.ComparisonViews.newComparisonView(pagedview.Scope{Person: "person", Project: p.ID}, p, request)
	view.id = uuid.NewString()
	defer view.close()
	text := strings.Repeat("const value = 1;\n", 500)
	comparison, err := loadTextComparison(wire.TextComparisonSource{Kind: "text", Path: "source.ts", Before: &text, After: &text})
	testutil.FailErr(t, "resolve text comparison", err)
	testutil.FailErr(t, "install comparison", server.ComparisonViews.installComparison(view, comparison))
	snapshot, err := view.snapshot(t.Context())
	testutil.FailErr(t, "read source view state", err)
	if snapshot.Comparison.State != "ready" || snapshot.Comparison.Extent.Rows != 500 {
		t.Fatalf("state=%+v", snapshot.Comparison)
	}
	read, release := view.read()
	defer release()
	progress := make(chan struct{})
	go func() { view.touch(); close(progress) }()
	select {
	case <-progress:
	case <-time.After(time.Second):
		t.Fatal("retained reader blocked progress publication")
	}
	r := httptest.NewRequest("GET", "/rows", nil)
	raw, err := json.Marshal(wire.SourceComparisonAnchor{Row: 400})
	testutil.FailErr(t, "encode source anchor", err)
	anchor := base64.RawURLEncoding.EncodeToString(raw)
	frame, err := read.comparisonFrame(r, sourceFrameQuery{limit: 20, anchor: anchor})
	testutil.FailErr(t, "rebase source anchor", err)
	if frame.Span.Start != 400 || frame.Span.End != 420 || frame.Anchor.Row != 400 {
		t.Fatalf("frame=%+v", frame)
	}
	relative, err := read.comparisonFrame(r, sourceFrameQuery{offset: 50, before: 10, limit: 20, anchor: anchor})
	testutil.FailErr(t, "read relative comparison range", err)
	if relative.Target == nil || *relative.Target != 450 || relative.Span.Start != 440 || relative.Span.End != 460 {
		t.Fatalf("relative frame=%+v", relative)
	}
	encoded, err := json.Marshal(frame)
	testutil.FailErr(t, "encode bounded frame", err)
	if len(encoded) > pagedview.MaxFrameBytes {
		t.Fatalf("frame bytes=%d", len(encoded))
	}
	if strings.Contains(string(encoded), "\"content\"") {
		t.Fatal("frame included an endpoint body")
	}
	window, err := read.comparisonFrame(r, sourceFrameQuery{limit: 20, anchor: anchor, before: 10})
	testutil.FailErr(t, "read comparison context before anchor", err)
	if window.Span.Start != 390 || window.Span.End != 410 || window.Anchor.Row != 390 {
		t.Fatalf("comparison viewport=%+v", window)
	}
}

func TestSourceFrameContextRequiresBoundedAnchor(t *testing.T) {
	for _, query := range []string{"context_before=1", "anchor=address&context_before=200", "anchor=address&context_before=10&limit=10", "anchor=address&context_before=-1"} {
		if _, err := parseSourceFrameQuery(httptest.NewRequest("GET", "/rows?"+query, nil)); err == nil {
			t.Fatalf("accepted invalid context query %s", query)
		}
	}
	query, err := parseSourceFrameQuery(httptest.NewRequest("GET", "/rows?anchor=address&context_before=30", nil))
	testutil.FailErr(t, "parse anchored viewport", err)
	if query.before != 30 || query.limit != 200 {
		t.Fatalf("query=%+v", query)
	}
}

func TestSourceAnchorRejectsForeignFieldsAndTrailingValues(t *testing.T) {
	for _, raw := range []string{`{"root_id":"root","path":".","row":1}`, `{"root_id":"root","path":"."} {}`, `null`, `{}`, `{"root_id":"root"}`} {
		encoded := base64.RawURLEncoding.EncodeToString([]byte(raw))
		_, err := decodeSourceAnchor[wire.SourceTreeAddress](encoded, "root_id", "path")
		if err == nil {
			t.Fatalf("accepted invalid anchor %s", raw)
		}
	}
}

func TestSourceViewsShareTextWhileKeepingSelectedEndpointMetadata(t *testing.T) {
	server := newSourceHandlerFixture(t)
	defer server.Views.sourceReaders.Close()
	p := &project.Project{ID: uuid.NewString()}
	scope := pagedview.Scope{Person: "person", Project: p.ID}
	request := wire.SourceComparisonViewCreate{Kind: "comparison", ClientID: "window:main", Intent: wire.SourceComparisonIntent{Mode: "full"}}
	first := server.ComparisonViews.newComparisonView(scope, p, request)
	defer first.close()
	second := server.ComparisonViews.newComparisonView(scope, p, request)
	defer second.close()
	before := readerTextSide("shared.go", "old\n")
	after := readerTextSide("shared.go", "new\n")
	before.RootID, after.RootID = "root-one", "root-one"
	before.VersionID, after.VersionID = "before-one", "after-one"
	testutil.FailErr(t, "install first endpoints", server.ComparisonViews.installComparison(first, wire.SourceComparison{InRange: true, Before: &before, After: &after}))
	before.RootID, after.RootID = "root-two", "root-two"
	before.VersionID, after.VersionID = "before-two", "after-two"
	testutil.FailErr(t, "install second endpoints", server.ComparisonViews.installComparison(second, wire.SourceComparison{InRange: true, Before: &before, After: &after}))
	if first.comparisonData.comparison != second.comparisonData.comparison {
		t.Fatal("identical source plans were not shared")
	}
	if second.comparisonData.comparisonBefore.RootID != "root-two" || second.comparisonData.comparisonAfter.VersionID != "after-two" {
		t.Fatal("shared plan replaced the selected endpoint metadata")
	}
	if first.comparisonData.comparisonBefore.RootID != "root-one" || first.comparisonData.comparisonAfter.VersionID != "after-one" {
		t.Fatal("new selection changed another view's endpoints")
	}
}

func TestSourceFrameRetentionValidatesBoundedProofs(t *testing.T) {
	valid := `{"end":200,"fingerprint":"` + strings.Repeat("a", 64) + `"}`
	for _, raw := range []string{"null", "[{}]", "[" + valid + "] {}", "[" + strings.Replace(valid, "200", "0", 1) + "]", "[" + strings.ReplaceAll(valid, "aaaa", "AAAA") + "]", "[" + strings.TrimSuffix(strings.Repeat(valid+",", 33), ",") + "]", "[" + strings.Replace(valid, `"end":200`, `"end":200,"foreign":1`, 1) + "]"} {
		_, err := parseTreeRetention(httptest.NewRequest("GET", "/rows?retain="+url.QueryEscape(raw), nil))
		if err == nil {
			t.Fatalf("accepted invalid retention %s", raw)
		}
	}
	proofs, err := parseTreeRetention(httptest.NewRequest("GET", "/rows?retain="+url.QueryEscape("["+valid+"]"), nil))
	testutil.FailErr(t, "parse prefix proof", err)
	if len(proofs) != 1 || proofs[0].End != 200 {
		t.Fatalf("proofs=%+v", proofs)
	}
}
