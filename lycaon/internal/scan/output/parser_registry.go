package output

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/advisory/severity"
)

// DefaultParserRegistry returns a registry with bundled output parsers.
func DefaultParserRegistry() *ParserRegistryImpl {
	r := &ParserRegistryImpl{parsers: make(map[string]OutputParser)}
	for _, p := range []OutputParser{
		newSARIFParser(),
		newOpengrepJSONParser(),
		newFindingsJSONParser(),
		// map/json is bound to its mapper during scanner construction.
		mapJSONParser{},
	} {
		_ = r.Register(p)
	}
	return r
}

// ParserRegistryImpl resolves parser implementations by ID.
type ParserRegistryImpl struct {
	mu      sync.RWMutex
	parsers map[string]OutputParser
}

// Register adds a parser to the registry.
func (r *ParserRegistryImpl) Register(p OutputParser) error {
	if p == nil || strings.TrimSpace(p.ID()) == "" {
		return fmt.Errorf("parser id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.parsers == nil {
		r.parsers = make(map[string]OutputParser)
	}
	if _, exists := r.parsers[p.ID()]; exists {
		return fmt.Errorf("parser %q already registered", p.ID())
	}
	r.parsers[p.ID()] = p
	return nil
}

// Get returns a parser by ID.
func (r *ParserRegistryImpl) Get(id string) (OutputParser, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.parsers[id]
	if !ok {
		return nil, fmt.Errorf("unknown parser %q", id)
	}
	return p, nil
}

// Parse dispatches raw output to a registered parser.
func (r *ParserRegistryImpl) Parse(_ context.Context, parserID string, raw []byte) (*Result, error) {
	p, err := r.Get(parserID)
	if err != nil {
		return nil, err
	}
	// Parsers rate unrated findings from the catalog; a broken catalog fails the parse.
	if _, err := severity.Default(); err != nil {
		return nil, err
	}
	return p.Parse(raw)
}

var defaultParserRegistry = DefaultParserRegistry()

// ParseOutput dispatches raw tool output to a registered parser by ID.
func ParseOutput(parserID string, raw []byte) (*Result, error) {
	return defaultParserRegistry.Parse(context.Background(), parserID, raw)
}

// IsRegisteredOutputParser reports whether parserID is registered.
func IsRegisteredOutputParser(parserID string) bool {
	_, err := defaultParserRegistry.Get(parserID)
	return err == nil
}
