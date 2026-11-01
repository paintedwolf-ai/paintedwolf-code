package localmodels_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/app/localmodels"
	"github.com/lycaon/lycaon/internal/decide/bialy"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func installedFixture(t *testing.T) localmodels.Options {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		testutil.FailErr(t, "write installed fixture", os.WriteFile(path, []byte(body), 0o700))
		return path
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	testutil.FailErr(t, "generate fixture release key", err)
	payload := []byte("generation fixture")
	digest := sha256.Sum256(payload)
	manifest := modelstore.Manifest{
		Model: nativegen.DefaultModelID, Device: "mlx", Revision: "fixture-v1",
		Artifact: modelstore.Artifact{Name: "model.safetensors", Digest: hex.EncodeToString(digest[:]), Bytes: int64(len(payload)), Format: "safetensors"},
	}
	signing, err := modelstore.SigningBytes(manifest)
	testutil.FailErr(t, "encode fixture manifest", err)
	signed := modelstore.SignedManifest{Manifest: manifest, KeyID: "fixture", Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, signing))}
	raw, err := json.Marshal(signed)
	testutil.FailErr(t, "encode signed fixture", err)
	write("manifest.json", string(raw))
	write("model.safetensors", string(payload))
	authority := write("release-keys.json", `{"format":1,"keys":[{"id":"fixture","public_key":"`+base64.StdEncoding.EncodeToString(public)+`"}]}`)
	binary := write("nativegen", "#!/bin/sh\nprintf '%s\\n' '{\"protocol\":1,\"backend\":\"mlx\",\"devices\":[{\"physical_device\":{\"kind\":\"apple_silicon\"},\"name\":\"fixture\",\"unified_memory\":true,\"total_bytes\":8589934592,\"recommended_bytes\":6442450944}]}'\n")
	decisionDir := t.TempDir()
	testutil.FailErr(t, "create decision encoder", os.Mkdir(filepath.Join(decisionDir, "encoder"), 0o700))
	for name, body := range map[string]string{
		"encoder/config.json": `{"model_type":"modernbert","hidden_size":768,"num_hidden_layers":22,"num_attention_heads":12,"intermediate_size":1152,"vocab_size":256000}`,
		"rl_agent_config.json": `{"max_len":1024,"head_layers":2}`,
		"model.safetensors": "decision fixture",
	} {
		testutil.FailErr(t, "write decision fixture", os.WriteFile(filepath.Join(decisionDir, name), []byte(body), 0o600))
	}
	return localmodels.Options{Binary: binary, MetalLibrary: "fixture.metallib", Authority: authority, Seed: dir, StoreRoot: filepath.Join(t.TempDir(), "models"), PhysicalRAM: 8 << 30, Decision: bialy.Config{ModelDir: decisionDir}}
}

func TestBuildAuthenticatesSeedAndSharesDecisionResources(t *testing.T) {
	options := installedFixture(t)
	r, err := localmodels.Build(t.Context(), options)
	testutil.FailErr(t, "compose installed local models", err)
	defer r.Resources.Close()
	defer r.Generation.Close()
	if r.Decision.Resources != r.Resources.Decision || r.Backend != "mlx" || r.Store.Status(nativegen.DefaultModelID, "mlx").State != modelstore.StateReady {
		t.Fatalf("incomplete installed composition: %+v", r)
	}
	testutil.FailErr(t, "remove bundled seed after installation", os.Remove(filepath.Join(options.Seed, "manifest.json")))
	second, err := localmodels.Build(t.Context(), options)
	testutil.FailErr(t, "reuse authenticated installed revision", err)
	defer second.Resources.Close()
	defer second.Generation.Close()
}

func TestBuildRejectsUntrustedAndUnsupportedInstallations(t *testing.T) {
	for _, name := range []string{"authority", "signature", "device", "checkpoint", "capacity", "binary", "disabled"} {
		t.Run(name, func(t *testing.T) {
			options := installedFixture(t)
			switch name {
			case "authority":
				testutil.FailErr(t, "damage authority", os.WriteFile(options.Authority, []byte(`{}`), 0o600))
			case "signature":
				testutil.FailErr(t, "damage seed signature", os.WriteFile(filepath.Join(options.Seed, "manifest.json"), []byte(`{}`), 0o600))
			case "device":
				options.Decision.Device = "vulkan:0"
			case "checkpoint":
				testutil.FailErr(t, "remove decision metadata", os.Remove(filepath.Join(options.Decision.ModelDir, "encoder", "config.json")))
			case "capacity":
				options.PhysicalRAM = 4 << 30
			case "binary":
				options.Binary = ""
			case "disabled":
				options.Decision.Disabled = true
			}
			if r, err := localmodels.Build(t.Context(), options); err == nil {
				_ = r.Generation.Close()
				r.Resources.Close()
				t.Fatal("invalid installation composed")
			}
		})
	}
}
