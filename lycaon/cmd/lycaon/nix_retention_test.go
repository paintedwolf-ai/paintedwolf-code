package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestNixRetainerReadinessNamesTheRegisteredLock(t *testing.T) {
	var output bytes.Buffer
	err := runNixRetainer(context.Background(), "/private/session.lock", strings.NewReader(""), &output)
	testutil.FailErr(t, "retainer reaches EOF", err)
	var ready struct {
		FormatVersion int    `json:"format_version"`
		LockPath      string `json:"lock_path"`
	}
	testutil.FailErr(t, "decode readiness", json.Unmarshal(output.Bytes(), &ready))
	if ready.FormatVersion != 1 || ready.LockPath != "/private/session.lock" {
		t.Fatalf("ready = %+v", ready)
	}
	for _, path := range []string{"", "relative", "/private/../lock"} {
		output.Reset()
		if runNixRetainer(context.Background(), path, strings.NewReader(""), &output) == nil || output.Len() != 0 {
			t.Fatalf("unregistered lock %q announced ready", path)
		}
	}
	if runNixRetainer(context.Background(), "/private/session.lock", strings.NewReader(""), failingWriter{}) == nil {
		t.Fatal("failed readiness write succeeded")
	}
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close(); _ = pw.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output.Reset()
	testutil.FailErr(t, "retainer observes cancellation", runNixRetainer(ctx, "/private/session.lock", pr, &output))
	if run(context.Background(), []string{"internal-nix-retain"}) == nil {
		t.Fatal("unretained host accepted internal-nix-retain")
	}
}
