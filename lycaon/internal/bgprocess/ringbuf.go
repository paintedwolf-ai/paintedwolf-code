package bgprocess

import (
	"strings"
	"sync"
	"unicode/utf8"
)

const DefaultRingBufferBytes = 256 << 10

// DefaultTailBytes is the tail a settled command result carries inline.
const DefaultTailBytes = 8192

// OutputChunk is one stdout/stderr slice with a monotonic cursor.
type OutputChunk struct {
	Cursor int64  `json:"cursor"`
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// RingBuffer stores recent process output with a byte cap.
type RingBuffer struct {
	mu         sync.Mutex
	chunks     []OutputChunk
	nextCursor int64
	totalBytes int
	maxBytes   int
	truncated  bool
}

func NewRingBuffer(maxBytes int) *RingBuffer {
	if maxBytes <= 0 {
		maxBytes = DefaultRingBufferBytes
	}
	return &RingBuffer{maxBytes: maxBytes}
}

// Append records output for stream and returns the chunk start cursor.
func (b *RingBuffer) Append(stream string, p []byte) int64 {
	if len(p) == 0 {
		return b.nextCursor
	}
	text := string(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	// Eviction waits for this write so screening sees boundary-spanning values.
	b.evictLocked()
	start := b.nextCursor
	b.nextCursor += int64(len(text))
	b.chunks = append(b.chunks, OutputChunk{
		Cursor: start,
		Stream: stream,
		Text:   text,
	})
	b.totalBytes += len(text)
	return start
}

// Head returns the cursor of the oldest retained byte.
func (b *RingBuffer) Head() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.chunks) == 0 {
		return b.nextCursor
	}
	return b.chunks[0].Cursor
}

// Overflowing reports whether the next append will evict retained bytes.
func (b *RingBuffer) Overflowing() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalBytes > b.maxBytes
}

func (b *RingBuffer) evictLocked() {
	for b.totalBytes > b.maxBytes && len(b.chunks) > 0 {
		drop := b.chunks[0]
		b.chunks = b.chunks[1:]
		b.totalBytes -= len(drop.Text)
		b.truncated = true
	}
}

// ReadSince returns chunks with cursor >= since and the next cursor for polling.
func (b *RingBuffer) ReadSince(since int64) (chunks []OutputChunk, nextCursor int64, truncated bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range b.chunks {
		if c.Cursor >= since {
			chunks = append(chunks, c)
		}
	}
	return chunks, b.nextCursor, b.truncated
}

// ReadPage returns the next lossless byte page at since. Pages may start and end
// inside buffered chunks. Valid UTF-8 remains rune-aligned.
func (b *RingBuffer) ReadPage(since int64, maxBytes int) (text string, nextCursor, availableThrough, evictedBytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	availableThrough = b.nextCursor
	if maxBytes <= 0 || len(b.chunks) == 0 {
		return "", since, availableThrough, 0
	}
	first := b.chunks[0].Cursor
	if since < first {
		evictedBytes = first - since
		since = first
	}
	if since > availableThrough {
		since = availableThrough
	}
	nextCursor = since
	remaining := maxBytes
	var out strings.Builder
	for _, chunk := range b.chunks {
		chunkEnd := chunk.Cursor + int64(len(chunk.Text))
		if chunkEnd <= nextCursor {
			continue
		}
		start := 0
		if nextCursor > chunk.Cursor {
			start = int(nextCursor - chunk.Cursor)
		}
		part := chunk.Text[start:]
		if len(part) > remaining {
			part = safeUTF8Prefix(part, remaining)
		}
		if part == "" {
			break
		}
		out.WriteString(part)
		nextCursor += int64(len(part))
		remaining -= len(part)
		if remaining == 0 {
			break
		}
	}
	return out.String(), nextCursor, availableThrough, evictedBytes
}

func safeUTF8Prefix(text string, maxBytes int) string {
	if maxBytes <= 0 || text == "" {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	if !utf8.ValidString(text) {
		return text[:maxBytes]
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// NextCursor returns the cursor value for the next append.
func (b *RingBuffer) NextCursor() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextCursor
}

// Render preserves adjacent writes so screening can match across boundaries.
func (b *RingBuffer) Render() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return RenderChunks(b.chunks)
}

// Tail returns the newest complete rendered chunks within maxBytes.
func (b *RingBuffer) Tail(maxBytes int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if maxBytes <= 0 {
		maxBytes = DefaultTailBytes
	}
	start := len(b.chunks)
	used := 0
	for start > 0 {
		chunk := b.chunks[start-1]
		need := len(chunk.Stream) + 2 + len(chunk.Text)
		if start < len(b.chunks) {
			need++
		}
		if used > 0 && used+need > maxBytes {
			break
		}
		used += need
		start--
		if used >= maxBytes {
			break
		}
	}
	var out strings.Builder
	for i := start; i < len(b.chunks); i++ {
		if i > start {
			out.WriteByte('\n')
		}
		out.WriteString(b.chunks[i].Stream)
		out.WriteString(": ")
		out.WriteString(b.chunks[i].Text)
	}
	return CutTail(out.String(), maxBytes)
}

// RenderChunks renders an already-selected window.
func RenderChunks(chunks []OutputChunk) string {
	var out strings.Builder
	stream := ""
	for i, chunk := range chunks {
		if i == 0 || chunk.Stream != stream {
			if i > 0 {
				out.WriteString("\n")
			}
			out.WriteString(chunk.Stream + ": ")
			stream = chunk.Stream
		}
		out.WriteString(chunk.Text)
	}
	return out.String()
}

// CutTail keeps the newest maxBytes of screened output.
func CutTail(text string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = DefaultTailBytes
	}
	if len(text) <= maxBytes {
		return text
	}
	cut := len(text) - maxBytes
	for cut < len(text) && !utf8.RuneStart(text[cut]) {
		cut++
	}
	return text[cut:]
}
