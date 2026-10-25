package startupprotocol

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func tokenFrame(token string) []byte {
	frame := binary.BigEndian.AppendUint32(nil, uint32(len(token)))
	return append(frame, token...)
}

func TestReadAPITokenFrameReadsOneFrame(t *testing.T) {
	t.Parallel()
	stream := bytes.NewReader(append(tokenFrame("abc-DEF_123"), "shutdown\n"...))
	token, err := ReadAPITokenFrame(stream)
	if err != nil || token != "abc-DEF_123" {
		t.Fatalf("token = %q, %v", token, err)
	}
	if err := ReadControlShutdown(stream); err != nil {
		t.Fatalf("frame after the token: %v", err)
	}
}

func TestReadAPITokenFrameRefusesMalformedFrames(t *testing.T) {
	t.Parallel()
	for name, frame := range map[string][]byte{
		"empty stream":   nil,
		"short length":   {0, 0},
		"zero length":    tokenFrame(""),
		"oversized":      binary.BigEndian.AppendUint32(nil, maxAPITokenBytes+1),
		"truncated":      tokenFrame("abcdef")[:7],
		"space":          tokenFrame("abc def"),
		"newline":        tokenFrame("abc\n"),
		"non-ascii byte": tokenFrame("abc\xff"),
	} {
		if token, err := ReadAPITokenFrame(bytes.NewReader(frame)); err == nil {
			t.Fatalf("%s accepted as %q", name, token)
		}
	}
	if _, err := ReadAPITokenFrame(bytes.NewReader(tokenFrame(strings.Repeat("x", maxAPITokenBytes)))); err != nil {
		t.Fatalf("token at the bound: %v", err)
	}
}
