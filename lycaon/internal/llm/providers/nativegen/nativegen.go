// Package nativegen implements the provider adapter that supervises and drives
// the native local generation service over length-prefixed stdio frames.
package nativegen

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

const (
	// DefaultModelID identifies the bundled custom Gemma 4 E4B model.
	DefaultModelID = "gemma4-e4b-paintedwolf"
	// DefaultContextWindowTokens is the 64K total token window shared by coordinator and summarizer roles.
	DefaultContextWindowTokens = 65536
	// DefaultOutputReserveTokens is the default output token reserve when MaxTokens is unset.
	DefaultOutputReserveTokens = 2048
	// MaxOutputReserveTokens bounds requested completion tokens on the local model.
	MaxOutputReserveTokens = 4096
)

// ErrContextCapacityExceeded indicates that prompt tokens plus output reserve exceed the 64K window.
var ErrContextCapacityExceeded = errors.New("nativegen: request exceeds 64K context capacity")

// Options configures the supervised nativegen binary and model artifact.
type Options struct {
	BinaryPath   string
	ModelDir     string
	Device       string
	ContextLimit int
	Models       []modelinfo.Entry
	TokenDecoder func(uint32) string
}

// DefaultModelEntry returns the catalog descriptor for the bundled Gemma 4 E4B model.
func DefaultModelEntry() modelinfo.Entry {
	return modelinfo.Entry{
		ID:            DefaultModelID,
		ContextLength: DefaultContextWindowTokens,
		MaxTokens:     MaxOutputReserveTokens,
		Callable:      modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
		Capabilities: modelinfo.ModelCapabilities{
			Chat:             modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			Streaming:        modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			Tools:            modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			StructuredOutput: modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			PromptCaching:    modelinfo.Evidence(modelinfo.CapabilitySupported, "nativegen"),
			Vision:           modelinfo.Evidence(modelinfo.CapabilityUnsupported, "nativegen"),
		},
	}
}

type Provider struct {
	id      string
	opts    Options
	profile providerprofile.Profile
	modelcall.Catalog

	mu          sync.Mutex
	writeMu     sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      *bufio.Reader
	loaded      bool
	generation  uint64
	cacheHandle string
	nextID      uint64
	pending     map[uint64]chan frame
	streams     map[string]chan frame
}

func New(id string, opts Options) *Provider {
	if opts.Device == "" {
		opts.Device = "cpu"
	}
	if opts.ContextLimit <= 0 {
		opts.ContextLimit = DefaultContextWindowTokens
	}
	models := opts.Models
	if len(models) == 0 {
		models = []modelinfo.Entry{DefaultModelEntry()}
	}
	return &Provider{
		id:      id,
		opts:    opts,
		Catalog: modelcall.NewCatalog("nativegen", models),
		profile: providerprofile.LocalInference(providerprofile.OpenAI()),
		pending: make(map[uint64]chan frame),
		streams: make(map[string]chan frame),
	}
}

func (p *Provider) ID() string                       { return p.id }
func (p *Provider) Profile() providerprofile.Profile { return p.profile }

type frame struct {
	Method   string `json:"method,omitempty"`
	ID       uint64 `json:"id,omitempty"`
	Manifest string `json:"manifest,omitempty"`
	Weights  string `json:"weights,omitempty"`
	Device   string `json:"device,omitempty"`
	Text     string `json:"text,omitempty"`

	RequestID    string   `json:"request_id,omitempty"`
	PromptTokens []uint32 `json:"prompt_tokens,omitempty"`
	MaxNewTokens uint32   `json:"max_new_tokens,omitempty"`
	Seed         uint64   `json:"seed,omitempty"`
	Temperature  float32  `json:"temperature,omitempty"`
	TopP         float32  `json:"top_p,omitempty"`
	StopTokens   []uint32 `json:"stop_tokens,omitempty"`
	CacheHandle  *string  `json:"cache_handle,omitempty"`

	Kind            string `json:"kind,omitempty"`
	Seq             uint64 `json:"seq,omitempty"`
	Token           uint32 `json:"token,omitempty"`
	StopReason      string `json:"stop_reason,omitempty"`
	Err             string `json:"error,omitempty"`
	Generation      uint64 `json:"generation,omitempty"`
	PromptCount     uint64 `json:"prompt_tokens_count,omitempty"`
	GeneratedTokens uint64 `json:"generated_tokens,omitempty"`
	ReusedPrefix    uint64 `json:"reused_prefix,omitempty"`

	Service  string   `json:"service,omitempty"`
	Protocol int      `json:"protocol,omitempty"`
	Loaded   *bool    `json:"loaded,omitempty"`
	Tokens   []uint32 `json:"tokens,omitempty"`
	Count    int      `json:"count,omitempty"`
}

// start launches the persistent nativegen child process if not already running.
func (p *Provider) start(_ context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return nil
	}
	if strings.TrimSpace(p.opts.BinaryPath) == "" {
		return errors.New("nativegen: binary path is not configured")
	}
	cmd := exec.Command(p.opts.BinaryPath, "--model", p.opts.ModelDir, "--device", p.opts.Device) // #nosec G204 -- supervised engine binary path
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
	p.loaded = false
	p.cacheHandle = ""
	go p.readLoop(p.stdout)
	return nil
}

// Close shuts down the supervised nativegen child process.
func (p *Provider) Close() error {
	p.stop()
	return nil
}

func (p *Provider) stop() {
	p.mu.Lock()
	cmd := p.cmd
	stdin := p.stdin
	p.cmd = nil
	p.stdin = nil
	p.stdout = nil
	p.loaded = false
	p.cacheHandle = ""
	p.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
}

func (p *Provider) readLoop(r *bufio.Reader) {
	for {
		raw, f, err := readFrame(r)
		if err != nil {
			p.failAll(err)
			return
		}
		if f.Kind == "usage" {
			var u struct {
				PromptTokens uint64 `json:"prompt_tokens"`
			}
			if json.Unmarshal(raw, &u) == nil && u.PromptTokens > 0 {
				f.PromptCount = u.PromptTokens
			}
		}
		p.mu.Lock()
		if f.Kind != "" && f.RequestID != "" {
			ch, ok := p.streams[f.RequestID]
			p.mu.Unlock()
			if ok {
				ch <- *f
			}
			continue
		}
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
		ch <- frame{ID: id, Err: err.Error()}
		delete(p.pending, id)
	}
	for reqID, ch := range p.streams {
		ch <- frame{Kind: "failed", RequestID: reqID, Err: err.Error()}
		close(ch)
		delete(p.streams, reqID)
	}
	p.cmd = nil
	p.loaded = false
	p.cacheHandle = ""
}

func readFrame(r *bufio.Reader) ([]byte, *frame, error) {
	length, err := readU32(r)
	if err != nil {
		return nil, nil, err
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, nil, err
	}
	var f frame
	if err := json.Unmarshal(buf, &f); err != nil {
		return nil, nil, fmt.Errorf("nativegen frame: %w", err)
	}
	return buf, &f, nil
}

func readU32(r *bufio.Reader) (uint32, error) {
	b := make([]byte, 4)
	if _, err := io.ReadFull(r, b); err != nil {
		return 0, err
	}
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24, nil
}

func (p *Provider) sendPayload(payload []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.mu.Lock()
	w := p.stdin
	p.mu.Unlock()
	if w == nil {
		return errors.New("nativegen: stdin closed")
	}
	return writeFrame(w, payload)
}

func (p *Provider) call(ctx context.Context, f frame) (frame, error) {
	if err := p.start(ctx); err != nil {
		return frame{}, err
	}
	p.mu.Lock()
	p.nextID++
	id := p.nextID
	f.ID = id
	ch := make(chan frame, 1)
	p.pending[id] = ch
	p.mu.Unlock()

	payload, err := json.Marshal(f)
	if err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return frame{}, err
	}
	if err := p.sendPayload(payload); err != nil {
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return frame{}, fmt.Errorf("nativegen write: %w", err)
	}
	select {
	case r := <-ch:
		if r.Err != "" {
			return r, fmt.Errorf("nativegen: %s", r.Err)
		}
		return r, nil
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, id)
		p.mu.Unlock()
		return frame{}, ctx.Err()
	}
}

func (p *Provider) hello(ctx context.Context) error {
	f, err := p.call(ctx, frame{Method: "hello"})
	if err != nil {
		return err
	}
	if f.Service != "nativegen" {
		return fmt.Errorf("nativegen: unexpected service %q", f.Service)
	}
	if f.Protocol != 1 {
		return fmt.Errorf("nativegen: unsupported protocol version %d", f.Protocol)
	}
	return nil
}

func (p *Provider) load(ctx context.Context) error {
	f, err := p.call(ctx, frame{Method: "load", Manifest: "", Weights: p.opts.ModelDir, Device: p.opts.Device})
	if err != nil {
		return err
	}
	if f.Loaded == nil || !*f.Loaded {
		return fmt.Errorf("nativegen: load did not confirm")
	}
	p.mu.Lock()
	p.loaded = true
	if f.Generation > 0 && f.Generation != p.generation {
		p.generation = f.Generation
		p.cacheHandle = ""
	}
	p.mu.Unlock()
	return nil
}

func (p *Provider) ensureReady(ctx context.Context) error {
	if err := p.start(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	loaded := p.loaded
	p.mu.Unlock()
	if loaded {
		return nil
	}
	if err := p.hello(ctx); err != nil {
		return err
	}
	return p.load(ctx)
}

// Tokenize serializes and tokenizes text through the loaded native service.
func (p *Provider) Tokenize(ctx context.Context, text string) ([]uint32, error) {
	if err := p.ensureReady(ctx); err != nil {
		return nil, err
	}
	f, err := p.call(ctx, frame{Method: "tokenize", Text: text})
	if err != nil {
		return nil, err
	}
	return f.Tokens, nil
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
	ch, err := p.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	comp, _, err := modelcall.CollectStream(ch)
	return comp, err
}

// Stream runs one streaming completion against the local service.
func (p *Provider) Stream(ctx context.Context, req modelcall.CompletionRequest) (<-chan modelcall.StreamChunk, error) {
	if err := p.ensureReady(ctx); err != nil {
		return nil, err
	}
	rendered, err := RenderPrompt(req, false)
	if err != nil {
		return nil, err
	}
	promptTokens, err := p.Tokenize(ctx, rendered)
	if err != nil {
		return nil, err
	}
	reserve := req.MaxTokens
	if reserve <= 0 {
		reserve = DefaultOutputReserveTokens
	}
	if reserve > MaxOutputReserveTokens {
		reserve = MaxOutputReserveTokens
	}
	if len(promptTokens)+reserve > p.opts.ContextLimit {
		return nil, fmt.Errorf("%w: prompt (%d) + reserve (%d) > %d", ErrContextCapacityExceeded, len(promptTokens), reserve, p.opts.ContextLimit)
	}

	p.mu.Lock()
	p.nextID++
	id := p.nextID
	reqID := fmt.Sprintf("req-%d", id)
	var handlePtr *string
	if p.cacheHandle != "" {
		h := p.cacheHandle
		handlePtr = &h
	}
	genMark := p.generation
	eventCh := make(chan frame, 64)
	p.streams[reqID] = eventCh
	p.mu.Unlock()

	genFrame := frame{
		Method:       "generate",
		ID:           id,
		RequestID:    reqID,
		PromptTokens: promptTokens,
		MaxNewTokens: uint32(reserve),
		Seed:         1,
		Temperature:  0.05,
		TopP:         1.0,
		StopTokens:   []uint32{},
		CacheHandle:  handlePtr,
	}
	payload, err := json.Marshal(genFrame)
	if err != nil {
		p.mu.Lock()
		delete(p.streams, reqID)
		p.mu.Unlock()
		return nil, err
	}
	if err := p.sendPayload(payload); err != nil {
		p.mu.Lock()
		delete(p.streams, reqID)
		p.mu.Unlock()
		return nil, fmt.Errorf("nativegen write: %w", err)
	}

	modelID := p.ResolveModel(req)
	out := make(chan modelcall.StreamChunk, 32)
	go func() {
		defer close(out)
		defer func() {
			p.mu.Lock()
			delete(p.streams, reqID)
			p.mu.Unlock()
		}()

		var rawOutput strings.Builder
		var usage modelcall.TokenUsage
		canceled := false

		for {
			select {
			case <-ctx.Done():
				if !canceled {
					canceled = true
					cancelPayload, _ := json.Marshal(frame{
						Method:    "cancel",
						RequestID: reqID,
					})
					_ = p.sendPayload(cancelPayload)
				}
				// Wait for physical settlement (done/failed) from the native service before returning.
				for ev := range eventCh {
					if ev.Kind == "done" || ev.Kind == "failed" {
						break
					}
				}
				out <- modelcall.StreamChunk{
					ProviderID: p.id,
					Model:      modelID,
					Usage:      usage,
					Err:        ctx.Err(),
				}
				return

			case ev, ok := <-eventCh:
				if !ok {
					return
				}
				switch ev.Kind {
				case "accepted":
					if ev.Generation > 0 {
						genMark = ev.Generation
					}
				case "token":
					tokText := p.decodeToken(ev.Token)
					rawOutput.WriteString(tokText)
				case "usage":
					usage = modelcall.TokenUsage{
						Present:              true,
						PromptTokens:         int(ev.PromptCount),
						CompletionTokens:     int(ev.GeneratedTokens),
						CacheReadInputTokens: int(ev.ReusedPrefix),
					}
				case "failed":
					out <- modelcall.StreamChunk{
						ProviderID: p.id,
						Model:      modelID,
						Usage:      usage,
						Err:        fmt.Errorf("nativegen: %s", ev.Err),
					}
					return
				case "done":
					if ev.StopReason != "canceled" {
						p.mu.Lock()
						p.cacheHandle = fmt.Sprintf("%s/%d", reqID, genMark)
						p.mu.Unlock()
					}
					content, reasoning, calls, parseErr := ParseModelOutput(rawOutput.String())
					if parseErr != nil {
						out <- modelcall.StreamChunk{
							ProviderID: p.id,
							Model:      modelID,
							Usage:      usage,
							Err:        parseErr,
						}
						return
					}
					out <- modelcall.StreamChunk{
						ProviderID: p.id,
						Model:      modelID,
						Content:    content,
						Reasoning:  reasoning,
						ToolCalls:  calls,
						Usage:      usage,
						Done:       true,
					}
					return
				}
			}
		}
	}()
	return out, nil
}

func (p *Provider) decodeToken(tok uint32) string {
	if p.opts.TokenDecoder != nil {
		return p.opts.TokenDecoder(tok)
	}
	if tok < 128 && tok >= 32 {
		return string(rune(tok))
	}
	return fmt.Sprintf("<t:%d>", tok)
}
