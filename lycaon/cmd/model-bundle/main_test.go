package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRunVerifiesOnlyTheSelectedSignedBundle(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate release key", err)
	directory := t.TempDir()
	payload := []byte("weights")
	digest := sha256.Sum256(payload)
	signed := modelstore.SignedManifest{Manifest: modelstore.Manifest{
		Model: "e4b", Device: "mlx", Revision: "r1",
		Artifact: modelstore.Artifact{Name: "model.safetensors", Bytes: int64(len(payload)), Digest: hex.EncodeToString(digest[:])},
	}, KeyID: "fixture"}
	raw, err := modelstore.SigningBytes(signed.Manifest)
	testutil.FailErr(t, "encode signed fields", err)
	signed.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(private, raw))
	raw, err = json.Marshal(signed)
	testutil.FailErr(t, "encode manifest", err)
	testutil.FailErr(t, "write manifest", os.WriteFile(filepath.Join(directory, "manifest.json"), raw, 0o600))
	testutil.FailErr(t, "write payload", os.WriteFile(filepath.Join(directory, "model.safetensors"), payload, 0o600))
	keys := filepath.Join(t.TempDir(), "keys.json")
	raw, err = json.Marshal(map[string]any{"format": 1, "keys": []any{map[string]string{"id": "fixture", "public_key": base64.StdEncoding.EncodeToString(public)}}})
	testutil.FailErr(t, "encode release keys", err)
	testutil.FailErr(t, "write release keys", os.WriteFile(keys, raw, 0o600))
	args := []string{"-bundle", directory, "-authority", keys, "-model", "e4b", "-device", "mlx"}
	var output bytes.Buffer
	testutil.FailErr(t, "verify packaged seed", run(t.Context(), args, &output, io.Discard))
	var observed modelstore.SignedManifest
	testutil.FailErr(t, "decode verified manifest", json.Unmarshal(output.Bytes(), &observed))
	if observed.Signature != signed.Signature || observed.Manifest.Revision != "r1" {
		t.Fatalf("verified identity changed: %+v", observed)
	}
	for _, invalid := range [][]string{nil, {"-unknown"}, append(append([]string{}, args...), "-device", "vulkan")} {
		output.Reset()
		if err := run(t.Context(), invalid, &output, io.Discard); err == nil || output.Len() != 0 {
			t.Fatalf("accepted invalid invocation: %v", invalid)
		}
	}
}
