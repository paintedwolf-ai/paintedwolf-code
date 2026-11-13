package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

// studentPayloads is what each device's bundle carries: the converter's MLX directory, or
// the quantized GGUF.
var studentPayloads = map[string][][2]string{
	"mlx": {{"model.safetensors", "safetensors"}, {"config.json", "config"}, {"tokenizer.json", "tokenizer"},
		{"tokenizer_config.json", "tokenizer"}, {"chat_template.jinja", "template"}, {"generation_config.json", "config"}},
	"vulkan": {{"model-Q4_K_M.gguf", "gguf"}},
}

func TestEveryStudentBundlePreparesSignsAndVerifiesForItsOwnSlot(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate fixture key", err)
	secrets := t.TempDir()
	raw, err := x509.MarshalPKCS8PrivateKey(private)
	testutil.FailErr(t, "encode fixture key", err)
	key := filepath.Join(secrets, "key.pem")
	testutil.FailErr(t, "write fixture key", os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}), 0o600))
	raw, err = json.Marshal(map[string]any{"format": 1, "keys": []any{map[string]string{"id": "fixture", "public_key": base64.StdEncoding.EncodeToString(public)}}})
	testutil.FailErr(t, "encode authority", err)
	keys := filepath.Join(secrets, "keys.json")
	testutil.FailErr(t, "write authority", os.WriteFile(keys, raw, 0o600))
	for _, model := range []string{"gemma4-e4b-paintedwolf", "gemma4-12b-paintedwolf", "gemma4-26b-paintedwolf"} {
		for _, device := range []string{"mlx", "vulkan"} {
			t.Run(model+"/"+device, func(t *testing.T) {
				bundle := t.TempDir()
				prepareArgs := []string{"prepare", "-bundle", bundle, "-model", model, "-revision", "campaign-1", "-device", device}
				for i, payload := range studentPayloads[device] {
					testutil.FailErr(t, "write payload", os.WriteFile(filepath.Join(bundle, payload[0]), []byte(model+payload[0]), 0o600))
					flag := "-file"
					if i == 0 {
						flag = "-artifact"
					}
					prepareArgs = append(prepareArgs, flag, payload[0]+":"+payload[1])
				}
				var prepared bytes.Buffer
				testutil.FailErr(t, "prepare bundle", run(t.Context(), prepareArgs, &prepared, io.Discard))
				unsigned := filepath.Join(t.TempDir(), "unsigned.json")
				testutil.FailErr(t, "write unsigned manifest", os.WriteFile(unsigned, prepared.Bytes(), 0o600))
				var signed bytes.Buffer
				signArgs := []string{"sign", "-bundle", bundle, "-manifest", unsigned, "-authority", keys, "-key-id", "fixture", "-private-key", key}
				testutil.FailErr(t, "sign bundle", run(t.Context(), signArgs, &signed, io.Discard))
				testutil.FailErr(t, "publish manifest", os.WriteFile(filepath.Join(bundle, "manifest.json"), signed.Bytes(), 0o600))
				var verified bytes.Buffer
				testutil.FailErr(t, "verify bundle", run(t.Context(), []string{"-bundle", bundle, "-authority", keys, "-model", model, "-device", device}, &verified, io.Discard))
				var manifest modelstore.SignedManifest
				testutil.FailErr(t, "decode verified manifest", json.Unmarshal(verified.Bytes(), &manifest))
				if manifest.Manifest.Revision != "campaign-1" || len(manifest.Manifest.Files) != len(studentPayloads[device])-1 {
					t.Fatalf("verified manifest = %+v", manifest.Manifest)
				}
				other := map[string]string{"mlx": "vulkan", "vulkan": "mlx"}[device]
				if err := run(t.Context(), []string{"-bundle", bundle, "-authority", keys, "-model", model, "-device", other}, io.Discard, io.Discard); err == nil {
					t.Fatalf("a %s bundle verified for %s", device, other)
				}
			})
		}
	}
}
