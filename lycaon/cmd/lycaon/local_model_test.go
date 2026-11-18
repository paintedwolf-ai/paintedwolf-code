package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/clisocket"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLocalModelCLI(t *testing.T) {
	dir := t.TempDir()
	manifest := fmt.Sprintf(`{"manifest":{"model":"dense","revision":"r1","device":"mlx","artifact":{"name":"weights","format":"safetensors","bytes":1,"digest":"%s"}}}`, strings.Repeat("a", 64))
	testutil.FailErr(t, "write model fixture", os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0600))
	for _, tc := range []struct {
		args []string
		op   string
	}{{[]string{"import", dir}, clisocket.OpImportModel}, {[]string{"opt-out"}, clisocket.OpOptOutModel}} {
		called := false
		send := func(_ context.Context, r clisocket.Request) (clisocket.Response, error) {
			called = true
			if r.Op != tc.op || (r.Op == clisocket.OpImportModel && (r.Model != "dense" || r.Target != dir)) {
				t.Fatalf("unexpected request %+v", r)
			}
			return clisocket.Response{Status: clisocket.StatusOK}, nil
		}
		testutil.FailErr(t, "send local model request", runLocalModel(t.Context(), tc.args, io.Discard, send))
		if !called {
			t.Fatal("missing request")
		}
	}
	for _, args := range [][]string{nil, {"import"}, {"import", filepath.Join(dir, "missing")}, {"opt-out", "extra"}} {
		if err := runLocalModel(t.Context(), args, io.Discard, nil); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, transportErr := range []error{errors.New("transport stopped"), nil} {
		send := func(context.Context, clisocket.Request) (clisocket.Response, error) {
			return clisocket.Response{Status: clisocket.StatusError, Message: "refused"}, transportErr
		}
		if err := runLocalModel(t.Context(), []string{"opt-out"}, io.Discard, send); err == nil {
			t.Fatal("missing request failure")
		}
	}
}
