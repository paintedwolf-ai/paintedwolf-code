package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/browserengine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestBrowserEnsureReportsResolvedArtifact(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "browser with spaces")
	testutil.FailErr(t, "create browser fixture", os.WriteFile(bin, []byte("fixture"), 0o700))
	testutil.FailErr(t, "create browser resources", os.WriteFile(filepath.Join(dir, "icudtl.dat"), nil, 0o600))
	t.Setenv(browserengine.EnvBrowserBin, bin)
	t.Setenv(browserengine.EnvBrowserSkipDownload, "1")
	var out bytes.Buffer
	testutil.FailErr(t, "resolve browser artifact", runBrowserEnsure(t.Context(), []string{"--json", "--cache-dir", t.TempDir()}, &out))
	var artifact map[string]string
	testutil.FailErr(t, "decode browser artifact", json.Unmarshal(out.Bytes(), &artifact))
	if artifact["path"] != bin || artifact["source"] != browserengine.SourceEnv {
		t.Fatalf("resolved artifact = %v", artifact)
	}
}
