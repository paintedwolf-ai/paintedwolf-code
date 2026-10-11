package native

import (
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
