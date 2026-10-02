package documentcore

import (
	"context"
	"fmt"
)

// Verification is what Verify established about the installed core.
type Verification struct {
	Binary   string
	Confined bool
}

// Verify starts the installed core under the confinement editors use and
// round-trips a document through it: an edit with undo capture, a restore
// from its checkpoint into a second document, and the undo.
func Verify(ctx context.Context) (Verification, error) {
	binary, err := Binary()
	if err != nil {
		return Verification{}, err
	}
	proc, err := start(ctx, binary)
	if err != nil {
		return Verification{}, err
	}
	engine := &Engine{proc: proc}
	defer func() { _ = engine.Close(context.WithoutCancel(ctx)) }()

	const text = "a🐺z\n"
	if _, err := engine.Call(ctx, Request{Action: "open", Handle: 1, Client: 1}); err != nil {
		return Verification{}, fmt.Errorf("document core open: %w", err)
	}
	edited, err := engine.Call(ctx, Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: text}}, CaptureUndo: true, Checkpoint: true})
	if err != nil {
		return Verification{}, fmt.Errorf("document core edit: %w", err)
	}
	if edited.Text != text || len(edited.Checkpoint) == 0 || len(edited.Undo) == 0 {
		return Verification{}, fmt.Errorf("document core edit returned text %q without its checkpoint or undo", edited.Text)
	}
	restored, err := engine.Call(ctx, Request{Action: "open", Handle: 2, Client: 2, Update: edited.Checkpoint})
	if err != nil {
		return Verification{}, fmt.Errorf("document core restore: %w", err)
	}
	if restored.Text != text {
		return Verification{}, fmt.Errorf("document core restore returned %q", restored.Text)
	}
	undone, err := engine.Call(ctx, Request{Action: "undo", Handle: 1, Undo: edited.Undo})
	if err != nil {
		return Verification{}, fmt.Errorf("document core undo: %w", err)
	}
	if undone.Text != "" {
		return Verification{}, fmt.Errorf("document core undo left %q", undone.Text)
	}
	return Verification{Binary: binary, Confined: proc.confined}, nil
}
