package nativegen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/modelstore"
	"github.com/lycaon/lycaon/pkg/api"
)

var (
	builtNativegenOnce sync.Once
	builtNativegenPath string
	builtNativegenErr  error
)

func ensureRustNativegenBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("NATIVEGEN_BIN"); bin != "" {
		if _, err := os.Stat(bin); err == nil {
			return bin
		}
	}
	builtNativegenOnce.Do(func() {
		crateDir, err := filepath.Abs(filepath.Join("..", "..", "..", "generation", "native"))
		if err != nil {
			builtNativegenErr = err
			return
		}
		targetDir := filepath.Join(os.TempDir(), "lycaon-nativegen-test-target")
		cmd := exec.Command("cargo", "build", "--manifest-path", filepath.Join(crateDir, "Cargo.toml"), "--target-dir", targetDir)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			builtNativegenErr = err
			return
		}
		builtNativegenPath = filepath.Join(targetDir, "debug", "nativegen")
	})
	if builtNativegenErr != nil {
		t.Fatalf("build rust nativegen binary: %v", builtNativegenErr)
	}
	return builtNativegenPath
}

func writeTinyCheckpointFixture(t *testing.T, dir string) (weightsBytes, configBytes, tokenizerBytes []byte) {
	t.Helper()
	cfg := map[string]any{
		"hidden_size":         4,
		"num_attention_heads": 2,
		"num_key_value_heads": 1,
		"head_dim":            2,
		"full_head_dim":       2,
		"num_hidden_layers":   1,
		"rope_theta":          10000.0,
		"vocab_size":          6,
		"rms_norm_eps":        1e-5,
		"sliding_window":      8,
		"eos_token_id":        4,
	}
	var err error
	configBytes, err = json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), configBytes, 0o600); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	type tensorSpec struct {
		name  string
		shape []int
	}
	specs := []tensorSpec{
		{"model.embed_tokens.weight", []int{6, 4}},
		{"model.norm.weight", []int{4}},
		{"model.layers.0.input_layernorm.weight", []int{4}},
		{"model.layers.0.self_attn.q_proj.weight", []int{4, 4}},
		{"model.layers.0.self_attn.k_proj.weight", []int{2, 4}},
		{"model.layers.0.self_attn.v_proj.weight", []int{2, 4}},
		{"model.layers.0.self_attn.o_proj.weight", []int{4, 4}},
		{"model.layers.0.self_attn.q_norm.weight", []int{2}},
		{"model.layers.0.self_attn.k_norm.weight", []int{2}},
		{"model.layers.0.pre_feedforward_layernorm.weight", []int{4}},
		{"model.layers.0.mlp.gate_proj.weight", []int{8, 4}},
		{"model.layers.0.mlp.up_proj.weight", []int{8, 4}},
		{"model.layers.0.mlp.down_proj.weight", []int{4, 8}},
	}
	header := make(map[string]any, len(specs))
	var data bytes.Buffer
	var fbuf [4]byte
	binary.LittleEndian.PutUint32(fbuf[:], math.Float32bits(0.1))
	for _, sp := range specs {
		count := 1
		for _, dim := range sp.shape {
			count *= dim
		}
		start := data.Len()
		for i := range count {
			val := float32(0.1)
			if sp.name == "model.embed_tokens.weight" {
				if i < 4 {
					val = 1.0
				} else {
					val = -1.0
				}
			}
			binary.LittleEndian.PutUint32(fbuf[:], math.Float32bits(val))
			_, _ = data.Write(fbuf[:])
		}
		end := data.Len()
		header[sp.name] = map[string]any{
			"dtype":        "F32",
			"shape":        sp.shape,
			"data_offsets": []int{start, end},
		}
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal safetensors header: %v", err)
	}
	var st bytes.Buffer
	var hlen [8]byte
	binary.LittleEndian.PutUint64(hlen[:], uint64(len(headerBytes)))
	_, _ = st.Write(hlen[:])
	_, _ = st.Write(headerBytes)
	_, _ = st.Write(data.Bytes())
	weightsBytes = st.Bytes()
	if err := os.WriteFile(filepath.Join(dir, "model.safetensors"), weightsBytes, 0o600); err != nil {
		t.Fatalf("write model.safetensors: %v", err)
	}

	tok := map[string]any{
		"version":        "1.0",
		"truncation":     nil,
		"padding":        nil,
		"added_tokens":   []any{},
		"normalizer":     nil,
		"pre_tokenizer":  map[string]any{"type": "Whitespace"},
		"post_processor": nil,
		"decoder":        nil,
		"model": map[string]any{
			"type": "WordLevel",
			"vocab": map[string]uint32{
				"▁Hello": 0,
				"▁world": 1,
				"test":   2,
				"ok":     3,
				"<eos>":  4,
				"<|tool_call>call:read_file{path:<|\"|>a.go<|\"|>}<tool_call|>": 5,
			},
			"unk_token": "▁Hello",
		},
	}
	tokenizerBytes, err = json.Marshal(tok)
	if err != nil {
		t.Fatalf("marshal tokenizer.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokenizer.json"), tokenizerBytes, 0o600); err != nil {
		t.Fatalf("write tokenizer.json: %v", err)
	}
	return weightsBytes, configBytes, tokenizerBytes
}

func sha256HexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestAdapterRoundTrip drives the compiled nativegen binary through Provider's frame codec.
func TestAdapterRoundTrip(t *testing.T) {
	bin := ensureRustNativegenBinary(t)
	modelDir := os.Getenv("NATIVEGEN_MODEL")
	if modelDir == "" {
		modelDir = t.TempDir()
		writeTinyCheckpointFixture(t, modelDir)
	}

	p := New("nativegen-test", Options{BinaryPath: bin, ModelDir: modelDir, Device: "cpu"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer p.stop()

	if err := p.hello(ctx); err != nil {
		t.Fatalf("hello: %v", err)
	}
	if err := p.load(ctx); err != nil {
		t.Fatalf("load: %v", err)
	}
}

type acceptAllSigner struct{}

func (acceptAllSigner) Verify(modelstore.SignedManifest) error { return nil }

// TestRustServiceInteroperabilityContract verifies end-to-end framing, modelstore checkpoint
// resolution, tokenizer decoding, and malformed-request error correlation against the Rust service.
func TestRustServiceInteroperabilityContract(t *testing.T) {
	bin := ensureRustNativegenBinary(t)
	fixtureDir := t.TempDir()
	weightsBytes, configBytes, tokenizerBytes := writeTinyCheckpointFixture(t, fixtureDir)

	storeRoot := t.TempDir()
	store := modelstore.New(storeRoot, acceptAllSigner{})

	stageBlob := func(name, format string, payload []byte) modelstore.Artifact {
		t.Helper()
		art := modelstore.Artifact{
			Name:   name,
			Digest: sha256HexBytes(payload),
			Format: format,
			Bytes:  int64(len(payload)),
		}
		if err := store.Stage(bytes.NewReader(payload), art); err != nil {
			t.Fatalf("Stage(%s): %v", name, err)
		}
		return art
	}

	weightsRef := stageBlob("model.safetensors", "safetensors", weightsBytes)
	configRef := stageBlob("config.json", "config", configBytes)
	tokRef := stageBlob("tokenizer.json", "tokenizer", tokenizerBytes)

	manifest := modelstore.Manifest{
		Model:    DefaultModelID,
		Revision: "rev-1",
		Device:   "cpu",
		Artifact: weightsRef,
		Files:    []modelstore.Artifact{configRef, tokRef},
	}
	if err := store.Activate(modelstore.SignedManifest{Manifest: manifest, Signature: "sig"}); err != nil {
		t.Fatalf("store.Activate: %v", err)
	}

	// Construct Provider without ModelDir or TokenDecoder so both resolve from the activated store revision.
	p := New("nativegen", Options{
		BinaryPath: bin,
		ModelStore: store,
		Device:     "cpu",
	})
	defer p.Close()

	if gotDir := p.ModelDir(); gotDir == "" {
		t.Fatal("expected Provider.ModelDir() resolved from activated modelstore revision")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	comp, err := p.Complete(ctx, modelcall.CompletionRequest{
		Messages:  []api.Message{{Role: api.MessageRoleUser, Content: "hello world"}},
		MaxTokens: 2,
	})
	if err != nil {
		t.Fatalf("Complete against Rust service: %v", err)
	}
	if comp.Content != "Hello Hello" {
		t.Fatalf("expected tokenizer-decoded output %q, got %q", "Hello Hello", comp.Content)
	}
	if !comp.Usage.Present || comp.Usage.CompletionTokens != 2 {
		t.Fatalf("unexpected usage: %+v", comp.Usage)
	}

	// Verify malformed requests return an error correlated to the caller without hanging until timeout.
	callCtx, callCancel := context.WithTimeout(ctx, 2*time.Second)
	defer callCancel()
	if _, err := p.call(callCtx, frame{Method: "unsupported_method"}); err == nil {
		t.Fatal("expected error from unsupported method frame")
	} else if callCtx.Err() != nil {
		t.Fatalf("waiter timed out instead of receiving correlated error: %v", err)
	}
}
