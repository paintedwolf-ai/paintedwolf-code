package fileoutline_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fileoutline"
	"github.com/lycaon/lycaon/internal/logoutline"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestResultLogDigestOmittedWhenNil(t *testing.T) {
	dir := t.TempDir()
	src := "package main\n\nfunc main() {}\n"
	path := filepath.Join(dir, "main.go")
	testutil.FailErr(t, "write", os.WriteFile(path, []byte(src), 0o644))

	out, err := fileoutline.Build(context.Background(), dir, "main.go")
	testutil.FailErr(t, "Build", err)
	raw, err := json.Marshal(out)
	testutil.FailErr(t, "marshal", err)
	if strings.Contains(string(raw), "log_digest") {
		t.Fatalf("log_digest should be omitted: %s", raw)
	}
}

func TestResultMarshalsLogDigestWhenSet(t *testing.T) {
	out := fileoutline.Result{
		Path:       "app.log",
		TotalLines: 3,
		Source:     "log",
		LogDigest: &logoutline.Digest{
			Format:      logoutline.FormatLogfmt,
			RecordCount: 3,
		},
	}
	raw, err := json.Marshal(out)
	testutil.FailErr(t, "marshal", err)
	body := string(raw)
	if !strings.Contains(body, `"log_digest"`) || !strings.Contains(body, `"format":"logfmt"`) {
		t.Fatalf("body = %s", body)
	}
}
