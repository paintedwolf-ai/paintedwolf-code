package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPrepareCommandEmitsMeasuredManifestWithoutPublishing(t *testing.T) {
	directory := t.TempDir()
	testutil.FailErr(t, "create weights", os.WriteFile(filepath.Join(directory, "model.gguf"), []byte("weights"), 0600))
	testutil.FailErr(t, "create tokenizer", os.WriteFile(filepath.Join(directory, "tokenizer.json"), []byte("tokenizer"), 0600))
	args := []string{"prepare", "-bundle", directory, "-model", "gemma4-e4b-paintedwolf", "-revision", "test-v1", "-device", "vulkan", "-artifact", "model.gguf:gguf", "-file", "tokenizer.json:tokenizer"}
	var output bytes.Buffer
	testutil.FailErr(t, "prepare manifest", run(t.Context(), args, &output, &bytes.Buffer{}))
	var manifest modelstore.Manifest
	testutil.FailErr(t, "decode manifest", json.Unmarshal(output.Bytes(), &manifest))
	sum := sha256.Sum256([]byte("weights"))
	if manifest.Artifact.Digest != hex.EncodeToString(sum[:]) || manifest.Artifact.Bytes != 7 || len(manifest.Files) != 1 || manifest.Files[0].Name != "tokenizer.json" {
		t.Fatalf("manifest = %+v", manifest)
	}
	if _, err := os.Stat(filepath.Join(directory, "manifest.json")); !os.IsNotExist(err) {
		t.Fatalf("prepare published a manifest: %v", err)
	}
	for _, extra := range [][]string{{"unexpected"}, {"-artifact", "bad"}, {"-file", ""}, {"-file", "x:y:z"}, {"-file", "model.gguf:gguf"}, {"-file", "absent:config"}} {
		output.Reset()
		if err := run(t.Context(), append(append([]string(nil), args...), extra...), &output, &bytes.Buffer{}); err == nil || output.Len() != 0 {
			t.Fatalf("accepted %v: %v, %q", extra, err, output.String())
		}
	}
	if err := run(t.Context(), []string{"prepare"}, &output, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted missing inputs")
	}
}
