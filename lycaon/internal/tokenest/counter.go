package tokenest

import (
	"errors"
	"fmt"
	"sync"
	"unicode"

	"github.com/tiktoken-go/tokenizer"
)

// Counter measures ordinary text with a declared encoding. It excludes provider
// framing and image tokens. An empty encoding explicitly selects the size proxy.
type Counter struct {
	codec tokenizer.Codec
}

var codecs sync.Map

// ErrTextLimit keeps local tokenization bounded on pathological tool payloads.
var ErrTextLimit = errors.New("text exceeds local tokenization limits")

const maxTokenizedTextBytes = 1 << 20
const maxTokenizedRunBytes = 4096

// Text fields and uninterrupted whitespace/non-whitespace runs have explicit
// limits because the embedded codec's merge pass is quadratic within a piece.
func withinTokenizationLimits(text string) bool {
	if len(text) > maxTokenizedTextBytes {
		return false
	}
	start := 0
	previousSpace := false
	for offset, r := range text {
		space := unicode.IsSpace(r)
		if space != previousSpace {
			start = offset
			previousSpace = space
		}
		if offset-start > maxTokenizedRunBytes {
			return false
		}
	}
	return len(text)-start <= maxTokenizedRunBytes
}

// NewCounter uses embedded vocabularies; constructing a counter never uses the network.
func NewCounter(encoding string) (Counter, error) {
	if encoding == "" {
		return Counter{}, nil
	}
	if encoding != "o200k_base" && encoding != "cl100k_base" {
		return Counter{}, fmt.Errorf("unsupported text encoding %q", encoding)
	}
	if cached, ok := codecs.Load(encoding); ok {
		return Counter{codec: cached.(tokenizer.Codec)}, nil
	}
	codec, err := tokenizer.Get(tokenizer.Encoding(encoding))
	if err != nil {
		return Counter{}, err
	}
	actual, _ := codecs.LoadOrStore(encoding, codec)
	return Counter{codec: actual.(tokenizer.Codec)}, nil
}

// Method names the measurement rather than implying a provider billing count.
func (c Counter) Method() string {
	if c.codec == nil {
		return "estimated"
	}
	return c.codec.GetName()
}

// Count returns an error rather than silently changing measurement units.
func (c Counter) Count(text string) (int, error) {
	if c.codec == nil {
		return EstimateDefault(text), nil
	}
	if !withinTokenizationLimits(text) {
		return 0, ErrTextLimit
	}
	return c.codec.Count(text)
}
