package native

import (
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/tooloutput"
)

// CapOpaqueTail caps opaque tool stdout before JSON marshal. Returns capped text,
// whether truncation occurred, and the original byte length.
func CapOpaqueTail(tail string, maxBytes int) (capped string, truncated bool, originalBytes int) {
	originalBytes = len(tail)
	cap := tooloutput.EffectiveMaxSpillFileBytes(maxBytes)
	if originalBytes <= cap {
		return tail, false, originalBytes
	}
	return tooloutput.CapSpillBytes(tail, maxBytes), true, originalBytes
}

// capBackgroundOutput caps aggregate command_output chunk text at the spill-file ceiling.
func capBackgroundOutput(out *commandOutput, maxBytes int) {
	if out == nil || len(out.Chunks) == 0 {
		return
	}
	var b strings.Builder
	for _, c := range out.Chunks {
		b.WriteString(c.Text)
	}
	joined := b.String()
	capped, truncated, _ := CapOpaqueTail(joined, maxBytes)
	if !truncated {
		return
	}
	out.Truncated = true
	stream := out.Chunks[0].Stream
	if stream == "" {
		stream = "stdout"
	}
	out.Chunks = []bgprocess.OutputChunk{
		{Cursor: out.Chunks[0].Cursor, Stream: stream, Text: capped},
	}
}
