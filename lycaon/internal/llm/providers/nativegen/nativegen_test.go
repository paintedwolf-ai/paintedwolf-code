package nativegen

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRenderPromptGemma4Format(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "File path",
			},
		},
		"required": []any{"path"},
	}
	req := modelcall.CompletionRequest{
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "You are a coding assistant."},
			{Role: api.MessageRoleUser, Content: "Read main.go"},
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{
					{ID: "call_1", Name: "read_file", Args: map[string]any{"path": "main.go"}},
				},
			},
			{
				Role:       api.MessageRoleTool,
				ToolResult: &api.ToolResult{Tool: "read_file", Content: "package main"},
				Content:    "package main",
			},
		},
		Tools: []tools.ToolMeta{
			{
				Name:        "read_file",
				Description: "Read a file from disk",
				ArgsSchema:  schema,
			},
		},
	}

	rendered, err := RenderPrompt(req, false)
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if strings.Contains(rendered, "<|think|>") {
		t.Fatalf("expected thinking tag omitted when enableThinking=false, got:\n%s", rendered)
	}
	wantFragments := []string{
		"<|turn>system\nYou are a coding assistant.\n<|tool>declaration:read_file{description:<|\"|>Read a file from disk<|\"|>,parameters:{properties:{path:{description:<|\"|>File path<|\"|>,type:STRING}},required:[<|\"|>path<|\"|>],type:OBJECT}}<tool|><turn|>\n",
		"<|turn>user\nRead main.go<turn|>\n",
		"<|turn>model\n<|tool_call>call:read_file{path:<|\"|>main.go<|\"|>}<tool_call|><turn|>\n",
		"<|turn>user\n<|tool_response>response:read_file{result:<|\"|>package main<|\"|>}<tool_response|><turn|>\n",
		"<|turn>model\n",
	}
	for _, frag := range wantFragments {
		if !strings.Contains(rendered, frag) {
			t.Errorf("missing fragment %q in rendered prompt:\n%s", frag, rendered)
		}
	}

	// ToolUseForbidden omits tool declarations in the closeout turn.
	reqForbidden := req
	reqForbidden.ToolUse = modelcall.ToolUseForbidden
	renderedForbidden, err := RenderPrompt(reqForbidden, false)
	if err != nil {
		t.Fatalf("RenderPrompt forbidden: %v", err)
	}
	if strings.Contains(renderedForbidden, "<|tool>declaration:") {
		t.Fatalf("expected tool declarations omitted when ToolUseForbidden, got:\n%s", renderedForbidden)
	}
}

func TestParseModelOutputAndGemmaToolCallRoundTrip(t *testing.T) {
	args := map[string]any{
		"command": "echo \"hello\" && ls",
		"env": map[string]any{
			"DEBUG": true,
			"COUNT": float64(3),
		},
		"tags": []any{"unit", `quote<|"|>test`},
	}
	encoded := RenderToolCall("bash", args)

	rawModel := "<|channel>thought\nChecking files first.<channel|>Running command.\n<|tool_call>" + encoded + "<tool_call|><turn|>"
	content, reasoning, calls, err := ParseModelOutput(rawModel)
	if err != nil {
		t.Fatalf("ParseModelOutput: %v", err)
	}
	if reasoning != "Checking files first." {
		t.Fatalf("unexpected reasoning %q", reasoning)
	}
	if content != "Running command." {
		t.Fatalf("unexpected content %q", content)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	if calls[0].Name != "bash" {
		t.Fatalf("unexpected tool name %q", calls[0].Name)
	}
	decoded := calls[0].Args
	if decoded["command"] != "echo \"hello\" && ls" {
		t.Fatalf("unexpected command %v", decoded["command"])
	}
	tags, ok := decoded["tags"].([]any)
	if !ok || len(tags) != 2 || tags[1] != `quote<|"|>test` {
		t.Fatalf("unexpected tags %v", decoded["tags"])
	}
}

func TestParseModelOutputDiscardsIncompleteToolCallAndPreservesProse(t *testing.T) {
	// Ordinary prose mentioning call: or JSON is never parsed as a tool call without <|tool_call>.
	prose := `Use call:read_file{path:"a.go"} or {"name":"read_file"} in examples.`
	content, _, calls, err := ParseModelOutput(prose)
	if err != nil {
		t.Fatalf("ParseModelOutput prose: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected 0 tool calls from prose, got %d", len(calls))
	}
	if content != prose {
		t.Fatalf("expected prose preserved, got %q", content)
	}

	// Truncated tool call at max_tokens is discarded without error or fabrication.
	truncated := "Partial answer before limit<|tool_call>call:write_file{path:<|\"|>main.go"
	content, _, calls, err = ParseModelOutput(truncated)
	if err != nil {
		t.Fatalf("ParseModelOutput truncated: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected truncated tool call discarded, got %d", len(calls))
	}
	if content != "Partial answer before limit" {
		t.Fatalf("unexpected content %q", content)
	}
}

// attachFakeService wires an in-process frame handler to the Provider without spawning an external binary.
func attachFakeService(t *testing.T, p *Provider, handler func(req frame, send func(frame))) func() {
	t.Helper()
	clientInR, clientInW := io.Pipe()
	clientOutR, clientOutW := io.Pipe()

	p.mu.Lock()
	p.cmd = &exec.Cmd{}
	p.stdin = clientInW
	p.stdout = bufio.NewReader(clientOutR)
	p.mu.Unlock()

	go p.readLoop(p.stdout)

	var writeMu sync.Mutex
	send := func(f frame) {
		raw, err := json.Marshal(f)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = writeFrame(clientOutW, raw)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(clientInR)
		for {
			_, f, err := readFrame(r)
			if err != nil {
				return
			}
			handler(*f, send)
		}
	}()

	return func() {
		_ = clientInW.Close()
		_ = clientOutW.Close()
		<-done
	}
}

func TestProviderCompletePrefixReuseAndContextLimit(t *testing.T) {
	vocab := map[uint32]string{
		10: "Hello",
		11: " world",
		12: "<|tool_call>call:read_file{path:<|\"|>a.go<|\"|>}<tool_call|>",
	}
	p := New("nativegen", Options{
		BinaryPath: "fake-nativegen",
		ModelDir:   "/tmp/fake-model",
		TokenDecoder: func(tok uint32) string {
			return vocab[tok]
		},
	})

	var mu sync.Mutex
	var seenCacheHandles []string
	tokenCountForNext := 128

	cleanup := attachFakeService(t, p, func(req frame, send func(frame)) {
		switch req.Method {
		case "hello":
			send(frame{ID: req.ID, Service: "nativegen", Protocol: 1})
		case "load":
			tr := true
			send(frame{ID: req.ID, Loaded: &tr, Generation: 1})
		case "tokenize":
			mu.Lock()
			n := tokenCountForNext
			mu.Unlock()
			toks := make([]uint32, n)
			for i := range toks {
				toks[i] = 1
			}
			send(frame{ID: req.ID, Tokens: toks, Count: len(toks)})
		case "generate":
			mu.Lock()
			if req.CacheHandle != nil {
				seenCacheHandles = append(seenCacheHandles, *req.CacheHandle)
			} else {
				seenCacheHandles = append(seenCacheHandles, "")
			}
			mu.Unlock()

			send(frame{Kind: "accepted", RequestID: req.RequestID, Generation: 1})
			send(frame{Kind: "token", RequestID: req.RequestID, Seq: 0, Token: 10})
			send(frame{Kind: "token", RequestID: req.RequestID, Seq: 1, Token: 11})
			send(frame{Kind: "token", RequestID: req.RequestID, Seq: 2, Token: 12})

			// Simulate exact-prefix reuse on the second turn.
			reused := uint64(0)
			if req.CacheHandle != nil && *req.CacheHandle != "" {
				reused = 96
			}
			// Send usage with prompt_tokens matching the wire protocol.
			send(frame{
				Kind:            "usage",
				RequestID:       req.RequestID,
				PromptCount:     uint64(len(req.PromptTokens)),
				GeneratedTokens: 3,
				ReusedPrefix:    reused,
			})
			send(frame{Kind: "done", RequestID: req.RequestID, StopReason: "eos"})
		}
	})
	defer cleanup()

	ctx := context.Background()
	comp1, err := p.Complete(ctx, modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Turn 1"}},
	})
	if err != nil {
		t.Fatalf("Turn 1 Complete: %v", err)
	}
	if comp1.Content != "Hello world" {
		t.Fatalf("unexpected content %q", comp1.Content)
	}
	if len(comp1.ToolCalls) != 1 || comp1.ToolCalls[0].Name != "read_file" {
		t.Fatalf("expected parsed read_file tool call, got %+v", comp1.ToolCalls)
	}

	comp2, err := p.Complete(ctx, modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Turn 2"}},
	})
	if err != nil {
		t.Fatalf("Turn 2 Complete: %v", err)
	}
	if comp2.Usage.CacheReadInputTokens != 96 {
		t.Fatalf("expected CacheReadInputTokens=96 on turn 2, got %d", comp2.Usage.CacheReadInputTokens)
	}

	mu.Lock()
	handles := append([]string(nil), seenCacheHandles...)
	mu.Unlock()
	if len(handles) != 2 || handles[0] != "" || !strings.HasSuffix(handles[1], "/1") {
		t.Fatalf("unexpected cache handles across turns: %v", handles)
	}

	// Verify 64K context window enforcement rejects oversized prompts before generation.
	mu.Lock()
	tokenCountForNext = DefaultContextWindowTokens - 512
	mu.Unlock()
	_, err = p.Complete(ctx, modelcall.CompletionRequest{
		Messages:  []api.Message{{Role: api.MessageRoleUser, Content: "Too large"}},
		MaxTokens: 1024,
	})
	if !errors.Is(err, ErrContextCapacityExceeded) {
		t.Fatalf("expected ErrContextCapacityExceeded, got %v", err)
	}
}

func TestProviderStreamCancellationWaitsForPhysicalSettlement(t *testing.T) {
	p := New("nativegen", Options{
		BinaryPath: "fake-nativegen",
		ModelDir:   "/tmp/fake-model",
	})

	cancelReceived := make(chan string, 1)
	allowDone := make(chan struct{})

	cleanup := attachFakeService(t, p, func(req frame, send func(frame)) {
		switch req.Method {
		case "hello":
			send(frame{ID: req.ID, Service: "nativegen", Protocol: 1})
		case "load":
			tr := true
			send(frame{ID: req.ID, Loaded: &tr, Generation: 1})
		case "tokenize":
			send(frame{ID: req.ID, Tokens: []uint32{1, 2, 3}, Count: 3})
		case "generate":
			send(frame{Kind: "accepted", RequestID: req.RequestID, Generation: 1})
		case "cancel":
			cancelReceived <- req.RequestID
			go func(reqID string) {
				<-allowDone
				send(frame{Kind: "done", RequestID: reqID, StopReason: "canceled"})
			}(req.RequestID)
		}
	})
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.Stream(ctx, modelcall.CompletionRequest{
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Cancel me"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	cancel()
	select {
	case <-cancelReceived:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancel frame")
	}

	// Stream channel must not close before the service sends physical settlement ("done").
	select {
	case chunk := <-ch:
		t.Fatalf("stream emitted before physical settlement: %+v", chunk)
	case <-time.After(50 * time.Millisecond):
	}

	close(allowDone)
	var finalChunk modelcall.StreamChunk
	for c := range ch {
		finalChunk = c
	}
	if !errors.Is(finalChunk.Err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", finalChunk.Err)
	}
}

func TestFrameMarshalJSONRetainsRustRequiredFields(t *testing.T) {
	cases := []struct {
		name     string
		f        frame
		required []string
	}{
		{
			name:     "load",
			f:        frame{Method: "load", ID: 7, Manifest: "", Weights: "/fixture", Device: "cpu"},
			required: []string{"manifest", "weights", "device"},
		},
		{
			name: "generate",
			f: frame{
				Method:       "generate",
				ID:           8,
				RequestID:    "r1",
				PromptTokens: []uint32{1},
				MaxNewTokens: 1,
				Seed:         0,
				Temperature:  0,
				TopP:         1,
				StopTokens:   []uint32{},
			},
			required: []string{"request_id", "prompt_tokens", "max_new_tokens", "seed", "temperature", "top_p", "stop_tokens"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.f)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				t.Fatalf("unmarshal request: %v", err)
			}
			for _, field := range tc.required {
				if _, ok := obj[field]; !ok {
					t.Errorf("required field %q missing in %s", field, raw)
				}
			}
		})
	}
}

func TestTokenizerAndStreamDecoderIncrementalDecoding(t *testing.T) {
	pEmpty := New("nativegen", Options{})
	if got := pEmpty.decodeToken(1000); got != "" {
		t.Fatalf("expected empty decode without loaded tokenizer, got %q", got)
	}

	rawTokenizer := []byte(`{
		"added_tokens": [
			{"id": 100, "content": "<|tool_call>", "special": true},
			{"id": 101, "content": "<tool_call|>", "special": true}
		],
		"model": {
			"type": "BPE",
			"vocab": {
				"▁Hello": 10,
				"<0xF0>": 20,
				"<0x9F>": 21,
				"<0x90>": 22,
				"<0xBA>": 23,
				"call:read_file{path:<|\"|>main.go<|\"|>}": 30
			}
		}
	}`)
	tok, err := ParseTokenizerJSON(rawTokenizer)
	if err != nil {
		t.Fatalf("ParseTokenizerJSON: %v", err)
	}

	dec := tok.NewStreamDecoder()
	var b strings.Builder
	for _, id := range []uint32{10, 20, 21, 22, 23, 100, 30, 101} {
		piece, ok := dec.Step(id)
		if !ok {
			t.Fatalf("Step(%d) failed", id)
		}
		b.WriteString(piece)
	}
	b.WriteString(dec.Flush())

	content, _, calls, err := ParseModelOutput(b.String())
	if err != nil {
		t.Fatalf("ParseModelOutput: %v", err)
	}
	if content != "Hello🐺" {
		t.Fatalf("expected incremental UTF-8 decode %q, got %q", "Hello🐺", content)
	}
	if len(calls) != 1 || calls[0].Name != "read_file" || calls[0].Args["path"] != "main.go" {
		t.Fatalf("expected tool call preserved through tokenizer decode, got %+v", calls)
	}
}

