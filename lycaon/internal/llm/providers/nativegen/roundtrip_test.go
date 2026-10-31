package nativegen

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestAdapterRoundTrip drives the compiled nativegen binary through Provider's frame codec.
func TestAdapterRoundTrip(t *testing.T) {
	bin := os.Getenv("NATIVEGEN_BIN")
	if bin == "" {
		t.Skip("NATIVEGEN_BIN not set")
	}
	modelDir := os.Getenv("NATIVEGEN_MODEL")
	if modelDir == "" {
		t.Skip("NATIVEGEN_MODEL not set")
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
