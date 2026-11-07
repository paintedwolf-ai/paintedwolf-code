package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSignCommandProducesVerifiableBundleManifest(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate fixture key", err)
	directory := t.TempDir()
	write := func(name string, raw []byte) string {
		t.Helper()
		path := filepath.Join(directory, name)
		testutil.FailErr(t, "write signing input", os.WriteFile(path, raw, 0o600))
		return path
	}
	payload := []byte("fixture weights")
	write("model.gguf", payload)
	digest := sha256.Sum256(payload)
	manifest := modelstore.Manifest{Model: "e4b", Revision: "r1", Device: "vulkan",
		Artifact: modelstore.Artifact{Name: "model.gguf", Format: "gguf", Bytes: int64(len(payload)), Digest: hex.EncodeToString(digest[:])}}
	raw, err := json.Marshal(manifest)
	testutil.FailErr(t, "encode export manifest", err)
	unsigned := write("unsigned.json", raw)
	raw, err = x509.MarshalPKCS8PrivateKey(private)
	testutil.FailErr(t, "encode fixture key", err)
	key := write("key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))
	raw, err = json.Marshal(map[string]any{"format": 1, "keys": []any{map[string]string{"id": "fixture", "public_key": base64.StdEncoding.EncodeToString(public)}}})
	testutil.FailErr(t, "encode authority", err)
	keys := write("keys.json", raw)
	args := []string{"sign", "-bundle", directory, "-manifest", unsigned, "-authority", keys, "-key-id", "fixture", "-private-key", key}
	var output bytes.Buffer
	testutil.FailErr(t, "sign export", run(t.Context(), args, &output, io.Discard))
	write("manifest.json", output.Bytes())
	output.Reset()
	testutil.FailErr(t, "verify signed export", run(t.Context(), []string{"-bundle", directory, "-authority", keys, "-model", "e4b", "-device", "vulkan"}, &output, io.Discard))
	for _, fault := range []string{"key", "manifest", "authority", "payload"} {
		t.Run(fault, func(t *testing.T) {
			badArgs := append([]string{}, args...)
			switch fault {
			case "key":
				badArgs = append(badArgs, "-private-key", unsigned)
			case "manifest":
				badArgs = append(badArgs, "-manifest", keys)
			case "authority":
				badArgs = append(badArgs, "-key-id", "untrusted")
			case "payload":
				write("model.gguf", []byte("changed"))
			}
			output.Reset()
			if err := run(t.Context(), badArgs, &output, io.Discard); err == nil || output.Len() != 0 {
				t.Fatalf("invalid signing input produced output: %v", err)
			}
		})
	}
}
