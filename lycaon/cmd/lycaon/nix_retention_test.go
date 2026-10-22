package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

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
}
