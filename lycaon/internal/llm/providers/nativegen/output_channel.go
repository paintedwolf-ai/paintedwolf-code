package nativegen

import (
	"errors"
	"strings"
)

func (d *outputDecoder) channelStep(token *uint32, text string) (bool, string, error) {
	if token != nil && *token == 100 {
		prefix, ok := strings.CutSuffix(text, "<|channel>")
		if !ok || d.channel || d.inCall {
			return true, "", errors.New("native generation: invalid thought channel boundary")
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
			return true, "", errors.New("native generation: thought channel decoding mismatch")
		}
		if err := d.thoughtText(prefix); err != nil {
			return true, "", err
		}
		if d.header != "" {
			return true, "", errors.New("native generation: incomplete thought channel header")
		}
		d.channel = false
		return true, "", nil
	}
	if token != nil {
		switch *token {
		case 0, 1, 2, 3, 4, 46, 47, 48, 49, 50, 51, 98, 105, 106:
			return true, "", errors.New("native generation: invalid control token in thought channel")
		}
	}
	return true, "", d.thoughtText(text)
}

func (d *outputDecoder) thoughtText(text string) error {
	if d.header != "" {
		count := min(len(text), len(d.header))
		if text[:count] != d.header[:count] {
			return errors.New("native generation: unsupported output channel")
		}
		d.header = d.header[count:]
		text = text[count:]
	}
	d.reasoning += text
	return nil
}
