// Package nativegen implements the provider that drives the native local
// generation service over stdio.
package nativegen

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// BinaryPath is the nativegen executable the provider supervises.
type Options struct {
	BinaryPath string
	ModelDir   string
	Device     string
}

type Provider struct {
	id      string
	opts    Options
	profile providerprofile.Profile
	modelcall.Catalog

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	nextID  uint64
	pending map[uint64]chan frame
}

func New(id string, opts Options) *Provider {
	return &Provider{
		id:      id,
		opts:    opts,
		Catalog: modelcall.NewCatalog("nativegen", nil),
		profile: providerprofile.LocalInference(providerprofile.OpenAI()),
		pending: make(map[uint64]chan frame),
	}
}

func (p *Provider) ID() string                       { return p.id }
func (p *Provider) Profile() providerprofile.Profile { return p.profile }

// frame is one length-prefixed JSON message in either direction.
type frame struct {
	ID     uint64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// start launches the service and begins reading replies.
func (p *Provider) start(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return nil
	}
	cmd := exec.CommandContext(ctx, p.opts.BinaryPath, "--model", p.opts.ModelDir, "--device", p.opts.Device)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("nativegen stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("nativegen stdout: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nativegen start: %w", err)
	}
	p.cmd = cmd
	p.stdin = stdin
	p.stdout = bufio.NewReader(stdout)
	go p.readLoop()
	return nil
}

// readLoop demultiplexes replies to waiting callers.
func (p *Provider) readLoop() {
	for {
		f, err := readFrame(p.stdout)
		if err != nil {
			p.failAll(err)
			return
		}
		p.mu.Lock()
		ch, ok := p.pending[f.ID]
		delete(p.pending, f.ID)
		p.mu.Unlock()
		if ok {
			ch <- *f
		}
	}
}

func (p *Provider) failAll(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, ch := range p.pending {
		ch <- frame{ID: id, Error: err.Error()}
		delete(p.pending, id)
	}
}

func readFrame(r *bufio.Reader) (*frame, error) {
	length, err := readU32(r)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	var f frame
	if err := json.Unmarshal(buf, &f); err != nil {
		return nil, fmt.Errorf("nativegen frame: %w", err)
	}
	return &f, nil
}

func readU32(r *bufio.Reader) (uint32, error) {
	// u32 little-endian length prefix.
	b := make([]byte, 4)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, err
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24, nil
}

// call sends one request frame and waits for its reply.
func (p *Provider) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := p.start(ctx); err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if params != nil {
		var err error
		raw, err = json.Marshal(params)
		if err != nil {
			return nil, err
		}
	}
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	ch := make(chan frame, 1)
	p.pending[id] = ch
	p.mu.Unlock()

	payload, _ := json.Marshal(frame{ID: id, Method: method, Params: raw})
	if err := writeFrame(p.stdin, payload); err != nil {
		return nil, fmt.Errorf("nativegen write: %w", err)
	}
	select {
	case f := <-ch:
		if f.Error != "" {
			return nil, fmt.Errorf("nativegen: %s", f.Error)
		}
		return f.Result, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return nil, ctx.Err()
	}
}

func writeFrame(w io.Writer, payload []byte) error {
	n := uint32(len(payload))
	head := []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}
	if _, err := w.Write(head); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

// Complete runs one non-streaming completion against the local service.
func (p *Provider) Complete(ctx context.Context, req modelcall.CompletionRequest) (*modelcall.Completion, error) {
	return nil, fmt.Errorf("nativegen: Complete requires a tokenized prompt; use the session's chat-template projection (pending L05 integration)")
}

// Stream runs one streaming completion against the local service.
func (p *Provider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	return nil, fmt.Errorf("nativegen: Stream requires a tokenized prompt; use the session's chat-template projection (pending L05 integration)")
}
