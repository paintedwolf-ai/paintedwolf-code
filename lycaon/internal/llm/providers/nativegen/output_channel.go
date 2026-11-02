package nativegen

import (
	"strings"
)

func (d *outputDecoder) channelStep(token *uint32, text string) (bool, string, error) {
	if token != nil && *token == 100 {
		prefix, ok := strings.CutSuffix(text, "<|channel>")
		if !ok || d.channel || d.inCall || d.schema {
			return true, "", d.protocolError("channel_boundary")
		}
		d.channel, d.header = true, "thought\n"
		return true, prefix, nil
	}
	if !d.channel {
		return false, "", nil
	}
	if token != nil && *token == 101 {
		prefix, ok := strings.CutSuffix(text, "<channel|>")
		if !ok {
			return true, "", d.protocolError("decoding_mismatch")
		}
		if err := d.thoughtText(prefix); err != nil {
			return true, "", err
		}
		if d.header != "" {
			return true, "", d.protocolError("channel_header")
		}
		d.channel = false
		return true, "", nil
	}
	if token != nil {
		switch *token {
		case 0, 1, 2, 3, 4, 46, 47, 48, 49, 50, 51, 98, 105, 106:
			return true, "", d.protocolError("channel_control")
		}
	}
	return true, "", d.thoughtText(text)
}

func (d *outputDecoder) thoughtText(text string) error {
	if d.header != "" {
		count := min(len(text), len(d.header))
		if text[:count] != d.header[:count] {
			return d.protocolError("unsupported_channel")
		}
		d.header = d.header[count:]
		text = text[count:]
	}
	d.reasoning += text
	return nil
}
