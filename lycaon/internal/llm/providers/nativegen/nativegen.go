// Package nativegen supervises and drives the native local generation service
// over length-prefixed stdio frames.
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
	"github.com/lycaon/lycaon/internal/modelstore"
)

const (
	// DefaultModelID identifies the bundled Gemma 4 E4B model.
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
	ModelStore   *modelstore.Store
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

// ResolveModelDir resolves an active checkpoint directory from store for the given models and device.
func ResolveModelDir(store *modelstore.Store, models []modelinfo.Entry, device string) (string, error) {
	lease, err := acquireStoreLease(store, models, device)
	if err != nil {
		return "", err
	}
	dir := lease.CheckpointDir()
	lease.Release()
	return dir, nil
}

func acquireStoreLease(store *modelstore.Store, models []modelinfo.Entry, device string) (*modelstore.Lease, error) {
	if store == nil {
		return nil, errors.New("nativegen: model store is nil")
	}
	modelIDs := make([]string, 0, len(models)+1)
	for _, m := range models {
		if id := strings.TrimSpace(m.ID); id != "" {
			modelIDs = append(modelIDs, id)
		}
	}
	modelIDs = append(modelIDs, DefaultModelID)

	devices := []string{device, "cpu", "mlx"}
	var lastErr error
	for _, id := range modelIDs {
		for _, dev := range devices {
			if strings.TrimSpace(dev) == "" {
				continue
			}
			lease, err := store.AcquireLease(id, dev, "nativegen", 1)
			if err == nil {
				return lease, nil
			}
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("nativegen: no active model revision")
	}
	return nil, lastErr
}

type Provider struct {
	id      string
	opts    Options
	profile providerprofile.Profile
	modelcall.Catalog

	mu            sync.Mutex
	writeMu       sync.Mutex
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdout        *bufio.Reader
	loaded        bool
	generation    uint64
	cacheHandle   string
	nextID        uint64
	pending       map[uint64]chan frame
	streams       map[string]chan frame
	lease         *modelstore.Lease
	leaseOwnedDir bool
	manifestRev   string
	tokenizer     *Tokenizer
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
	opts.Models = models
	if strings.TrimSpace(opts.ModelDir) == "" {
		if envDir := strings.TrimSpace(os.Getenv("LYCAON_NATIVEGEN_MODEL_DIR")); envDir != "" {
			opts.ModelDir = envDir
		}
	}
	if opts.ModelStore == nil {
		if root := modelstore.DefaultRoot(); root != "" {
			opts.ModelStore = modelstore.New(root, nil)
		}
	}
	p := &Provider{
		id:      id,
		opts:    opts,
		Catalog: modelcall.NewCatalog("nativegen", models),
		profile: providerprofile.LocalInference(providerprofile.OpenAI()),
		pending: make(map[uint64]chan frame),
		streams: make(map[string]chan frame),
	}
	_, _ = p.resolveModelDirLocked()
	if p.opts.ModelDir != "" {
		if tok, err := LoadTokenizer(p.opts.ModelDir); err == nil {
			p.tokenizer = tok
		}
	}
	return p
}

func (p *Provider) ID() string                       { return p.id }
func (p *Provider) Profile() providerprofile.Profile { return p.profile }

// ModelDir returns the configured or store-resolved checkpoint directory.
func (p *Provider) ModelDir() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	dir, _ := p.resolveModelDirLocked()
	return dir
}

func (p *Provider) resolveModelDirLocked() (string, error) {
	if p.lease == nil && p.opts.ModelStore != nil {
		if lease, err := acquireStoreLease(p.opts.ModelStore, p.opts.Models, p.opts.Device); err == nil {
			p.lease = lease
			p.manifestRev = lease.Manifest.Revision
			if strings.TrimSpace(p.opts.ModelDir) == "" {
				p.opts.ModelDir = lease.CheckpointDir()
				p.leaseOwnedDir = true
			}
		}
	}
	if strings.TrimSpace(p.opts.ModelDir) == "" {
		if envDir := strings.TrimSpace(os.Getenv("LYCAON_NATIVEGEN_MODEL_DIR")); envDir != "" {
			p.opts.ModelDir = envDir
		}
	}
	if strings.TrimSpace(p.opts.ModelDir) == "" {
		return "", errors.New("nativegen: model directory is not configured and no active modelstore revision is available")
	}
	return p.opts.ModelDir, nil
}

type frame struct {
	Method   string `json:"method,omitempty"`
	ID       uint64 `json:"id,omitempty"`
	Manifest string `json:"manifest,omitempty"`
	Weights  string `json:"weights,omitempty"`
	Device   string `json:"device,omitempty"`
	Text     string `json:"text,omitempty"`

	RequestID    string   `json:"request_id,omitempty"`
	PromptTokens []uint32 `json:"-"`
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

// MarshalJSON preserves protocol fields required by the Rust service even when empty or zero.
func (f frame) MarshalJSON() ([]byte, error) {
	type rawFrame frame
	raw, err := json.Marshal(rawFrame(f))
	if err != nil {
		return nil, err
	}
	if f.Method == "" && len(f.PromptTokens) == 0 && f.Kind != "usage" {
		return raw, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if len(f.PromptTokens) > 0 {
		obj["prompt_tokens"] = f.PromptTokens
	}
	if f.Kind == "usage" && f.PromptCount > 0 {
		obj["prompt_tokens"] = f.PromptCount
	}
	switch f.Method {
	case "load":
		obj["manifest"] = f.Manifest
		obj["weights"] = f.Weights
		obj["device"] = f.Device
	case "generate":
		obj["request_id"] = f.RequestID
		if f.PromptTokens == nil {
			obj["prompt_tokens"] = []uint32{}
		} else {
			obj["prompt_tokens"] = f.PromptTokens
		}
		obj["max_new_tokens"] = f.MaxNewTokens
		obj["seed"] = f.Seed
		obj["temperature"] = f.Temperature
		obj["top_p"] = f.TopP
		if f.StopTokens == nil {
			obj["stop_tokens"] = []uint32{}
		} else {
			obj["stop_tokens"] = f.StopTokens
		}
	case "tokenize":
		obj["text"] = f.Text
	case "detokenize":
		if f.Tokens == nil {
			obj["tokens"] = []uint32{}
		} else {
			obj["tokens"] = f.Tokens
		}
	case "cancel":
		obj["request_id"] = f.RequestID
	}
	return json.Marshal(obj)
}

// UnmarshalJSON decodes prompt_tokens as either a token array (generate request) or a token count (usage event).
func (f *frame) UnmarshalJSON(data []byte) error {
	type rawFrame frame
	var rf rawFrame
	if err := json.Unmarshal(data, &rf); err != nil {
		return err
	}
	*f = frame(rf)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if rawPrompt, ok := fields["prompt_tokens"]; ok && len(rawPrompt) > 0 {
		var count uint64
		if err := json.Unmarshal(rawPrompt, &count); err == nil {
			f.PromptCount = count
			return nil
		}
		var tokens []uint32
		if err := json.Unmarshal(rawPrompt, &tokens); err != nil {
			return err
		}
		f.PromptTokens = tokens
	}
	return nil
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
	modelDir, err := p.resolveModelDirLocked()
	if err != nil {
		return err
	}
	cmd := exec.Command(p.opts.BinaryPath, "--model", modelDir, "--device", p.opts.Device) // #nosec G204 -- supervised engine binary path
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

// Close shuts down the supervised nativegen child process and releases any active model lease.
func (p *Provider) Close() error {
	p.stop()
	return nil
}

func (p *Provider) stop() {
	p.mu.Lock()
	cmd := p.cmd
	stdin := p.stdin
	lease := p.lease
	p.cmd = nil
	p.stdin = nil
	p.stdout = nil
	p.loaded = false
	p.cacheHandle = ""
	p.lease = nil
	if p.leaseOwnedDir {
		p.opts.ModelDir = ""
		p.leaseOwnedDir = false
	}
	p.mu.Unlock()
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}
	if lease != nil {
		lease.Release()
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
		if f.RequestID != "" && (f.Kind != "" || f.Err != "") {
			if f.Kind == "" && f.Err != "" {
				f.Kind = "failed"
			}
			if ch, ok := p.streams[f.RequestID]; ok {
				p.mu.Unlock()
				ch <- *f
				continue
			}
		}
		if f.ID != 0 {
			if ch, ok := p.pending[f.ID]; ok {
				delete(p.pending, f.ID)
				p.mu.Unlock()
				ch <- *f
				continue
			}
		}
		if f.Err != "" {
			for id, ch := range p.pending {
				delete(p.pending, id)
				ch <- frame{ID: id, Err: f.Err}
			}
			for reqID, ch := range p.streams {
				ch <- frame{Kind: "failed", RequestID: reqID, Err: f.Err}
			}
		}
		p.mu.Unlock()
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
	p.mu.Lock()
	modelDir, err := p.resolveModelDirLocked()
	manifestRev := p.manifestRev
	device := p.opts.Device
	p.mu.Unlock()
	if err != nil {
		return err
	}
	f, err := p.call(ctx, frame{Method: "load", Manifest: manifestRev, Weights: modelDir, Device: device})
	if err != nil {
		return err
	}
	if f.Loaded == nil || !*f.Loaded {
		return fmt.Errorf("nativegen: load did not confirm")
	}
	tok, tokErr := LoadTokenizer(modelDir)
	p.mu.Lock()
	if tokErr == nil {
		p.tokenizer = tok
	}
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

// Detokenize decodes token IDs into text through the loaded native service.
func (p *Provider) Detokenize(ctx context.Context, tokens []uint32) (string, error) {
	if err := p.ensureReady(ctx); err != nil {
		return "", err
	}
	f, err := p.call(ctx, frame{Method: "detokenize", Tokens: tokens})
	if err != nil {
		return "", err
	}
	return f.Text, nil
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
	streamDec := p.tokenizer.NewStreamDecoder()
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
					rawOutput.WriteString(p.decodeStreamToken(ev, streamDec))
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
					if p.opts.TokenDecoder == nil && streamDec != nil {
						rawOutput.WriteString(streamDec.Flush())
					}
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

func (p *Provider) decodeStreamToken(ev frame, streamDec *StreamDecoder) string {
	if p.opts.TokenDecoder != nil {
		return p.opts.TokenDecoder(ev.Token)
	}
	if streamDec != nil {
		if piece, ok := streamDec.Step(ev.Token); ok {
			return piece
		}
	}
	if ev.Text != "" {
		return ev.Text
	}
	return p.decodeToken(ev.Token)
}

func (p *Provider) decodeToken(tok uint32) string {
	if p.opts.TokenDecoder != nil {
		return p.opts.TokenDecoder(tok)
	}
	p.mu.Lock()
	t := p.tokenizer
	if t == nil && p.opts.ModelDir != "" {
		if loaded, err := LoadTokenizer(p.opts.ModelDir); err == nil {
			p.tokenizer = loaded
			t = loaded
		}
	}
	p.mu.Unlock()
	if t != nil {
		if piece, ok := t.DecodeToken(tok); ok {
			return piece
		}
	}
	return ""
}
