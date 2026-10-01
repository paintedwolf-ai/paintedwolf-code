package safecmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyreceipt"
)

func TestRejectReturnsToolReject(t *testing.T) {
	err := Reject("JQ_TIMEOUT", map[string]any{"query": "."})
	var rej *tools.ToolReject
	if !errors.As(err, &rej) {
		t.Fatalf("got %T, want *tools.ToolReject", err)
	}
	if rej.Code != "JQ_TIMEOUT" {
		t.Fatalf("Code = %q", rej.Code)
	}
	if rej.Data["query"] != "." {
		t.Fatalf("Data = %#v", rej.Data)
	}
}

func TestRejectNilDataIsEmptyMap(t *testing.T) {
	err := Reject("JQ_PATH_REQUIRED", nil)
	var rej *tools.ToolReject
	if !errors.As(err, &rej) {
		t.Fatal("expected ToolReject")
	}
	if rej.Data == nil {
		t.Fatal("Data must be non-nil empty map")
	}
}

func TestResolvePathRejectsDotDot(t *testing.T) {
	dir := t.TempDir()
	b := testBoundary(t)
	_, err := ResolvePath(context.Background(), b, testCtx(dir), "../etc/passwd")
	var rej *tools.ToolReject
	if !errors.As(err, &rej) || rej.Code != "SURVEY_PATH_ESCAPE" {
		t.Fatalf("err = %v, want SURVEY_PATH_ESCAPE", err)
	}
}

func TestResolvePathReadsUnderRoot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte("hi"), 0o644))
	b := testBoundary(t)
	resolved, err := ResolvePath(context.Background(), b, testCtx(dir), "a.txt")
	testutil.FailErr(t, "resolve", err)
	if resolved.Abs != path {
		t.Fatalf("Abs = %q want %q", resolved.Abs, path)
	}
}

func TestResolvePathAllowsDotsInsideFilename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes..draft.txt")
	testutil.FailErr(t, "write dotted file", os.WriteFile(path, []byte("hi"), 0o644))
	resolved, err := ResolvePath(context.Background(), testBoundary(t), testCtx(dir), "notes..draft.txt")
	testutil.FailErr(t, "resolve dotted file", err)
	if resolved.Abs != path {
		t.Fatalf("abs = %q want %q", resolved.Abs, path)
	}
}

func TestCapsEnforceInputBytes(t *testing.T) {
	c := Caps{InputBytes: 10}
	if err := c.EnforceInputBytes(5, "JQ_INPUT_TOO_LARGE", map[string]any{"path": "x"}); err != nil {
		t.Fatalf("under cap: %v", err)
	}
	err := c.EnforceInputBytes(11, "JQ_INPUT_TOO_LARGE", map[string]any{"path": "x"})
	var rej *tools.ToolReject
	if !errors.As(err, &rej) || rej.Code != "JQ_INPUT_TOO_LARGE" {
		t.Fatalf("err = %v", err)
	}
	if rej.Data["bytes"] != int64(11) || rej.Data["max_bytes"] != int64(10) {
		t.Fatalf("Data = %#v", rej.Data)
	}
}

func TestCapsWithTimeout(t *testing.T) {
	c := Caps{Timeout: 5 * time.Millisecond}
	ctx, cancel := c.WithTimeout(context.Background())
	defer cancel()
	select {
	case <-ctx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout did not fire")
	}

	unset := Caps{}
	ctx2, cancel2 := unset.WithTimeout(context.Background())
	defer cancel2()
	if ctx2.Err() != nil {
		t.Fatal("unset timeout must not cancel")
	}
}

func TestShapeAttachesReceipt(t *testing.T) {
	type resp struct {
		Results []string `json:"results"`
	}
	out, err := Shape(ShapeInput{
		Tool:         "stat",
		Path:         "a.txt",
		PathsTouched: 1,
		Value:        resp{Results: []string{"a.txt"}},
	})
	testutil.FailErr(t, "shape", err)
	if _, ok := surveyreceipt.Parse(out); !ok {
		t.Fatalf("missing receipt in %q", out)
	}
	var got map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &got))
	if _, ok := got["results"]; !ok {
		t.Fatalf("body = %#v", got)
	}
}

func TestShapePatchesCoverage(t *testing.T) {
	type resp struct {
		Note string `json:"note"`
	}
	out, err := Shape(ShapeInput{
		Tool:         "find",
		Path:         ".",
		PathsTouched: 0,
		Selected:     0,
		Total:        100,
		Value:        resp{Note: "zoomed"},
	})
	testutil.FailErr(t, "shape", err)
	var got map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &got))
	if got["selected"] != float64(0) || got["total"] != float64(100) {
		t.Fatalf("coverage = %#v", got)
	}
}

func TestConfineDelegates(t *testing.T) {
	c, ok := Confine(nil)
	if ok || c != nil {
		t.Fatalf("empty roots: got (%v, %v), want (nil, false)", c, ok)
	}
}

func testBoundary(t *testing.T) *sandbox.Boundary {
	t.Helper()
	return sandbox.NewBoundary(sandbox.Config{
		ProjectRootRequired: true,
		RejectSymlinkEscape: true,
	}, []sandbox.ToolProfile{{
		ID:    tools.DefaultToolProfileID,
		Tools: map[string]bool{"stat": true},
	}})
}

func testCtx(dir string) tools.ToolContext {
	return tools.ToolContext{
		Roots:              []projectroot.RootRef{{ID: "r1", Label: "root", Path: dir, IsPrimary: true}},
		ActiveRootID:       "r1",
		Agent:              tools.DefaultToolProfileID,
		SessionID:          "test-session",
		RepoFileCount:      100,
		RepoFileCountKnown: true,
	}
}
