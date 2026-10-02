// Package documentcore runs text CRDT operations in a separate native process
// that holds no files, network, or credentials, under a fixed memory ceiling.
package documentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const (
	maxRequestBytes = 64 << 20
	// memoryLimitBytes bounds the core's heap; a document past it ends the
	// process rather than the engine.
	memoryLimitBytes = 256 << 20
	callTimeout      = 30 * time.Second
)

// Engine serializes entry into one core process. Callers serialize each
// document's admission with its durable commit before exposing the result.
type Engine struct {
	mu   sync.Mutex
	proc *process
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

// New starts a core process for this host.
func New(ctx context.Context) (*Engine, error) {
	binary, err := Binary()
	if err != nil {
		return nil, err
	}
	proc, err := start(ctx, binary)
	if err != nil {
		return nil, err
	}
	return &Engine{proc: proc}, nil
}

// Closed reports whether the process has ended, after a failed call or on its own.
func (e *Engine) Closed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.proc.exited()
}

// Close stops the process and releases every document it held.
func (e *Engine) Close(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.proc.stop(stopGrace)
	return nil
}

// Call runs one request. Caller cancellation does not interrupt it: an
// abandoned call would leave the shared process mid-request. Any transport
// failure ends the process, so its documents reload from accepted state.
func (e *Engine) Call(ctx context.Context, request Request) (Snapshot, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
	defer cancel()
	var response Snapshot
	input, err := json.Marshal(request)
	if err != nil {
		return response, fmt.Errorf("encode document request: %w", err)
	}
	if len(input) > maxRequestBytes {
		return response, &Rejected{Code: "update_too_large"}
	}
	output, err := e.proc.exchange(ctx, input)
	if err != nil {
		e.proc.stop(0)
		// Names the request a crash or hang interrupted, so an input that
		// ends the core every time can be found.
		log.Warn("document core request did not complete", "action", request.Action,
			"handle", request.Handle, "request_bytes", len(input), "error", err)
		return response, err
	}
	if err = json.Unmarshal(output, &response); err != nil {
		e.proc.stop(0)
		return response, fmt.Errorf("decode document response: %w", err)
	}
	if response.Error != "" {
		return response, &Rejected{Code: response.Error}
	}
	return response, nil
}
