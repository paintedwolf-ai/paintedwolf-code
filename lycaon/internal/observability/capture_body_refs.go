package observability

import (
	"crypto/sha256"
	"encoding/base64"
	"sync"

	"github.com/lycaon/lycaon/pkg/api"
)

// bodyRefRunes is the shortest body worth replacing with a reference. Below it
// the reference costs more than the text it saves.
const bodyRefRunes = 512

// maxBodyRefs bounds the digests one process remembers.
const maxBodyRefs = 4096

// bodyRefs tracks long bodies already written to the current capture file.
type bodyRefs struct {
	mu   sync.Mutex
	seen map[string]struct{}
	// order tracks insertion so the oldest digest is evicted first.
	order []string
}

var captureBodies = &bodyRefs{seen: map[string]struct{}{}}

// claim records a digest and reports whether this is its first sighting.
func (b *bodyRefs) claim(digest string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[digest]; ok {
		return false
	}
	if len(b.order) >= maxBodyRefs {
		delete(b.seen, b.order[0])
		b.order = b.order[1:]
	}
	b.seen[digest] = struct{}{}
	b.order = append(b.order, digest)
	return true
}

// resetCaptureBodyRefs clears references when the capture file rotates.
func resetCaptureBodyRefs() {
	captureBodies.mu.Lock()
	defer captureBodies.mu.Unlock()
	captureBodies.seen = map[string]struct{}{}
	captureBodies.order = nil
}

func bodyDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

// dedupeMessageBodies references repeated, already-redacted bodies by digest.
func dedupeMessageBodies(msgs []api.Message) []api.Message {
	for i := range msgs {
		msgs[i].Content = referenceOrBody(msgs[i].Content)
		if msgs[i].ToolResult != nil {
			clone := *msgs[i].ToolResult
			clone.Content = referenceOrBody(clone.Content)
			msgs[i].ToolResult = &clone
		}
	}
	return msgs
}

func referenceOrBody(content string) string {
	if len([]rune(content)) < bodyRefRunes {
		return content
	}
	digest := bodyDigest(content)
	if captureBodies.claim(digest) {
		return content + "\n[capture-body " + digest + "]"
	}
	return "[capture-body " + digest + " — identical to its first appearance in this file]"
}
