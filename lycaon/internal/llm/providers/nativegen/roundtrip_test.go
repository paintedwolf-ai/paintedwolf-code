package nativegen

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// The adapter round-trip drives the compiled nativegen binary through the
// Provider's own frame codec, so the test needs NATIVEGEN_BIN and
// NATIVEGEN_MODEL pointing at a checkpoint directory.
func TestAdapterRoundTrip(t *testing.T) {
	bin := os.Getenv("NATIVEGEN_BIN")
	if bin == "" {
		t.Skip("NATIVEGEN_BIN not set; build the nativegen release binary to run this check")
	}
	modelDir := os.Getenv("NATIVEGEN_MODEL")
	if modelDir == "" {
		t.Skip("NATIVEGEN_MODEL not set; point it at a checkpoint directory")
	}

	p := New("nativegen-test", Options{BinaryPath: bin, ModelDir: modelDir, Device: "cpu"})
	// Replace the default start with a capture of the child, so the test can
	// drive the codec directly against the real binary.
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

// stubProcess swaps the provider's child for one the test controls.
func (p *Provider) startForTest(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.cmd = cmd
	p.stdin = stdin
	p.stdout = bufio.NewReader(stdout)
}

func (p *Provider) stop() {
	if p.cmd != nil && p.cmd.Process != nil {
		p.stdin.Close()
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
}

var _ = io.EOF
var _ = binary.LittleEndian
var _ = json.Marshal
