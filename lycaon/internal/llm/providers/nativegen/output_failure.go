package nativegen

import (
	"fmt"
	"github.com/lycaon/lycaon/internal/llm/failure"
)

// OutputProtocolError contains only protocol state, never generated text.
type OutputProtocolError struct {
	ProviderID string
	Model      string
	Reason     string
	Phase      string
	Position   int
	Controls   []OutputControl
}

type OutputControl struct {
	ID       uint32
	Position int
}

func (e *OutputProtocolError) Error() string {
	return fmt.Sprintf("native generation: output protocol violation (%s; phase=%s; position=%d; controls=%v)", e.Reason, e.Phase, e.Position, e.Controls)
}

func (d *outputDecoder) protocolError(reason string) error {
	phase := "text"
	if d.channel {
		phase = "thought"
		if d.header != "" {
			phase = "channel_header"
		}
	} else if d.inCall {
		phase = "tool_call"
		if d.quoted {
			phase = "tool_string"
		}
	}
	diagnostic := &OutputProtocolError{ProviderID: d.provider, Model: d.model, Reason: reason, Phase: phase, Position: d.position, Controls: append([]OutputControl(nil), d.controls...)}
	repairable := len(d.allowed) > 0 && !d.schema
	switch reason {
	case "decoding_mismatch", "output_bound":
		repairable = false
	}
	return &failure.ProviderOutputProtocolError{ProviderID: d.provider, Model: d.model, Reason: reason, Repairable: repairable, Cause: diagnostic}
}

func (d *outputDecoder) observe(token *uint32) {
	if token == nil {
		return
	}
	d.position++
	switch *token {
	case 0, 1, 2, 3, 4, 46, 47, 48, 49, 50, 51, 52, 98, 100, 101, 105, 106:
		if len(d.controls) == 16 {
			d.controls = d.controls[1:]
		}
		d.controls = append(d.controls, OutputControl{ID: *token, Position: d.position})
	}
}
