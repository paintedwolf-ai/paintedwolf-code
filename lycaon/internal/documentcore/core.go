// Package documentcore runs text CRDT operations in isolated WASM memory.
package documentcore

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed core.wasm
var binary []byte

var compilationCache = wazero.NewCompilationCache()

const maxRequestBytes = 64 << 20

// Engine serializes entry into one isolated WASM memory. Callers serialize each
// document's admission with its durable commit before exposing the result.
type Engine struct {
	mu      sync.Mutex
	runtime wazero.Runtime
	module  api.Module
}

type Edit struct {
	Index  uint32 `json:"index"`
	Delete uint32 `json:"delete"`
	Insert string `json:"insert"`
}

type Request struct {
	Authorship   bool           `json:"authorship,omitempty"`
	OmitText     bool           `json:"omit_text,omitempty"`
	RetainedUndo [][]byte       `json:"retained_undo,omitempty"`
	CaptureUndo  bool           `json:"capture_undo,omitempty"`
	TrackChanges bool           `json:"track_changes,omitempty"`
	Undo         []byte         `json:"undo,omitempty"`
	Action       string         `json:"action"`
	Handle       uint32         `json:"handle"`
	Client       uint32         `json:"client,omitempty"`
	Update       []byte         `json:"update,omitempty"`
	Vector       []byte         `json:"vector,omitempty"`
	Edits        []Edit         `json:"edits,omitempty"`
	Checkpoint   bool           `json:"checkpoint,omitempty"`
	Anchors      []AnchoredEdit `json:"anchors,omitempty"`
	Guards       []AnchoredEdit `json:"guards,omitempty"`
}

type AnchoredEdit struct {
	Start    []byte `json:"start"`
	End      []byte `json:"end"`
	Expected string `json:"expected"`
	Insert   string `json:"insert"`
}

type Snapshot struct {
	Inserted   []IdentityRange  `json:"inserted"`
	Deleted    []IdentityRange  `json:"deleted"`
	Authors    []AuthorshipSpan `json:"authors"`
	UndoUnits  uint64           `json:"undo_units"`
	Undo       []byte           `json:"undo"`
	Text       string           `json:"text"`
	Vector     []byte           `json:"vector"`
	Update     []byte           `json:"update"`
	Checkpoint []byte           `json:"checkpoint"`
	Error      string           `json:"error,omitempty"`
	Anchors    []AnchoredEdit   `json:"anchors"`
	Resolved   []Edit           `json:"resolved"`
}

type AuthorshipSpan struct {
	Index  uint32 `json:"index"`
	Length uint32 `json:"length"`
	Client uint32 `json:"client"`
	Clock  uint32 `json:"clock"`
}

type IdentityRange struct {
	Client uint32 `json:"client"`
	Start  uint32 `json:"start"`
	End    uint32 `json:"end"`
}

// Rejected indicates invalid CRDT input. Any failed call requires reloading
// the last accepted state before reusing the document handle.
type Rejected struct{ Code string }

func (e *Rejected) Error() string { return "document core: " + e.Code }

func New(ctx context.Context) (*Engine, error) {
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(4096).WithCloseOnContextDone(true).WithCompilationCache(compilationCache))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("instantiate document computation imports: %w", err)
	}
	module, err := runtime.InstantiateWithConfig(ctx, binary, wazero.NewModuleConfig().WithRandSource(rand.Reader))
	if err != nil {
		_ = runtime.Close(ctx)
		return nil, fmt.Errorf("instantiate document core: %w", err)
	}
	return &Engine{runtime: runtime, module: module}, nil
}

func (e *Engine) Closed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.module.IsClosed()
}

func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.runtime.Close(ctx)
}

func (e *Engine) Call(ctx context.Context, request Request) (Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	var response Snapshot
	input, err := json.Marshal(request)
	if err != nil {
		return response, fmt.Errorf("encode document request: %w", err)
	}
	if len(input) > maxRequestBytes {
		return response, &Rejected{Code: "update_too_large"}
	}
	output, err := e.exchange(ctx, input)
	if err != nil {
		// A trap may leave Rust locks or allocator state unusable.
		_ = e.runtime.Close(context.WithoutCancel(ctx))
		return response, err
	}
	if err = json.Unmarshal(output, &response); err != nil {
		_ = e.runtime.Close(context.WithoutCancel(ctx))
		return response, fmt.Errorf("decode document response: %w", err)
	}
	if response.Error != "" {
		return response, &Rejected{Code: response.Error}
	}
	return response, nil
}

func (e *Engine) exchange(ctx context.Context, input []byte) ([]byte, error) {
	allocated, err := e.module.ExportedFunction("allocate").Call(ctx, uint64(len(input)))
	if err != nil {
		return nil, fmt.Errorf("allocate document request: %w", err)
	}
	pointer := allocated[0]
	if pointer == 0 || pointer > math.MaxUint32 || !e.module.Memory().Write(uint32(pointer), input) {
		return nil, errors.New("document core request memory exhausted")
	}
	defer func() { _, _ = e.module.ExportedFunction("release").Call(ctx, pointer, uint64(len(input))) }()
	values, err := e.module.ExportedFunction("execute_request").Call(ctx, pointer, uint64(len(input)))
	if err != nil {
		return nil, fmt.Errorf("execute document request: %w", err)
	}
	address, size := uint32(values[0]>>32), uint32(values[0]&math.MaxUint32)
	defer func() { _, _ = e.module.ExportedFunction("release").Call(ctx, uint64(address), uint64(size)) }()
	output, ok := e.module.Memory().Read(address, size)
	if !ok || size > maxRequestBytes {
		return nil, errors.New("document core response outside memory bounds")
	}
	return append([]byte(nil), output...), nil
}
