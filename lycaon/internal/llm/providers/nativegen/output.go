package nativegen

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

type outputDecoder struct {
	allowed   map[string]bool
	calls     []api.ToolCall
	body      strings.Builder
	literal   strings.Builder
	inCall    bool
	quoted    bool
	bytes     int
	channel   bool
	header    string
	reasoning string
}

func newOutputDecoder(req modelcall.CompletionRequest) *outputDecoder {
	d := &outputDecoder{allowed: make(map[string]bool)}
	if req.ToolsCallable() && req.ResponseFormat == nil {
		for _, tool := range req.Tools {
			d.allowed[tool.Name] = true
		}
	}
	return d
}

func (d *outputDecoder) text(text string) string {
	if d.quoted {
		d.literal.WriteString(text)
	} else if d.inCall {
		d.body.WriteString(text)
	} else {
		return text
	}
	return ""
}

func (d *outputDecoder) step(token *uint32, text string) (string, error) {
	d.bytes += len(text)
	if d.bytes > 4<<20 || d.body.Len()+d.literal.Len() > 1<<20 {
		return "", errors.New("native generation: output exceeds its bound")
	}
	if handled, visible, err := d.channelStep(token, text); handled {
		return visible, err
	}
	if token == nil {
		return d.text(text), nil
	}
	marker := ""
	switch *token {
	case 48:
		marker = "<|tool_call>"
	case 49:
		marker = "<tool_call|>"
	case 52:
		if d.inCall {
			marker = `<|"|>`
		}
	case 0, 1, 2, 3, 4, 46, 47, 50, 51, 98, 100, 101, 105, 106:
		return "", fmt.Errorf("native generation: unexpected output control token %d", *token)
	}
	if marker == "" {
		return d.text(text), nil
	}
	prefix, ok := strings.CutSuffix(text, marker)
	if !ok {
		return "", errors.New("native generation: control token decoding mismatch")
	}
	visible := d.text(prefix)
	switch *token {
	case 48:
		if d.inCall || len(d.allowed) == 0 || len(d.calls) >= 64 {
			return "", errors.New("native generation: tool call is not allowed here")
		}
		d.inCall = true
		d.body.Reset()
	case 49:
		if !d.inCall || d.quoted {
			return "", errors.New("native generation: incomplete tool-call boundary")
		}
		if err := d.finishCall(false); err != nil {
			return "", err
		}
	case 52:
		if d.quoted {
			encoded, err := json.Marshal(d.literal.String())
			if err != nil {
				return "", err
			}
			d.body.Write(encoded)
			d.literal.Reset()
		}
		d.quoted = !d.quoted
	}
	return visible, nil
}

func (d *outputDecoder) finishCall(truncated bool) error {
	body := strings.TrimSpace(d.body.String())
	body, ok := strings.CutPrefix(body, "call:")
	brace := strings.IndexByte(body, '{')
	if !ok || brace < 1 {
		return errors.New("native generation: malformed tool-call header")
	}
	name := strings.TrimSpace(body[:brace])
	if !d.allowed[name] {
		return errors.New("native generation: tool was not offered")
	}
	call := api.ToolCall{ID: providerwire.NewToolCallID(), Name: name, ArgsTruncated: truncated}
	if !truncated {
		args, err := parseArguments(body[brace:])
		call.Args, call.ArgsMalformed = args, err != nil
	}
	d.calls = append(d.calls, call)
	d.inCall = false
	d.body.Reset()
	return nil
}

func (d *outputDecoder) finish(lengthCapped bool) ([]api.ToolCall, error) {
	if d.channel && !lengthCapped {
		return nil, errors.New("native generation: unfinished thought channel")
	}
	if d.inCall {
		if !lengthCapped {
			return nil, errors.New("native generation: unfinished tool call")
		}
		if err := d.finishCall(true); err != nil {
			return nil, err
		}
	}
	return d.calls, nil
}
