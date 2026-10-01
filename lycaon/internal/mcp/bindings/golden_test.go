package bindings_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp/bindings"
	"github.com/lycaon/lycaon/internal/testutil"
)

const goldenWidgetJSON = `{"status":"ready","count":2}`

func TestGoldenFixtureWidgetApply(t *testing.T) {
	list, err := bindings.LoadDir(goldenBindingsDir(t))
	testutil.FailErr(t, "LoadDir", err)
	matched, fields, err := bindings.Apply(list, "fixture", "widget", goldenWidgetJSON)
	testutil.FailErr(t, "Apply", err)
	if !matched {
		t.Fatal("expected match")
	}
	if fields["widget_status"] != "ready" {
		t.Fatalf("widget_status=%v", fields["widget_status"])
	}
	if fields["widget_count"] != int64(2) {
		t.Fatalf("widget_count=%v", fields["widget_count"])
	}
	if fields["is_ready"] != true {
		t.Fatalf("is_ready=%v", fields["is_ready"])
	}
}

func TestGoldenFixtureWidgetWrongJSON(t *testing.T) {
	list, err := bindings.LoadDir(goldenBindingsDir(t))
	testutil.FailErr(t, "LoadDir", err)
	matched, fields, err := bindings.Apply(list, "fixture", "widget", `{"status":"ready"}`)
	testutil.FailErr(t, "Apply", err)
	if matched || fields != nil {
		t.Fatalf("schema should fail without count: matched=%v fields=%v", matched, fields)
	}
}

func goldenBindingsDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "testdata", "mcp_bindings")
}
