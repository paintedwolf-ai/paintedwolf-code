package mcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/fseffect"
	"gopkg.in/yaml.v3"
)

// DefaultToolPinsPath is the device-level tool-definition pin file.
const DefaultToolPinsPath = "mcp-tool-pins.yaml"

// toolPinsFile is the on-disk shape: provider id → tool name → definition fingerprint.
type toolPinsFile struct {
	Providers map[string]map[string]string `yaml:"providers"`
}

// toolPins records fingerprints for remote HTTP MCP tools so definition substitution is detectable.
// Stdio and loopback providers are out of scope.
type toolPins struct {
	mu   sync.RWMutex
	path string
	// loadErr blocks remote definitions when durable pin state is unreadable.
	loadErr error
	// pinned is the last accepted fingerprint per provider/tool.
	pinned map[string]map[string]string
	// current is the fingerprint most recently observed at registration.
	current map[string]map[string]string
	// tracked marks provider ids in scope for pinning (remote HTTP only).
	tracked map[string]bool
}

func newToolPins(stateDir string) *toolPins {
	p := &toolPins{
		pinned:  map[string]map[string]string{},
		current: map[string]map[string]string{},
		tracked: map[string]bool{},
	}
	if dir := strings.TrimSpace(stateDir); dir != "" {
		p.path = filepath.Join(dir, DefaultToolPinsPath)
	}
	p.load()
	return p
}

func (p *toolPins) load() {
	if p == nil || p.path == "" {
		return
	}
	raw, err := os.ReadFile(p.path)
	if err != nil {
		if !os.IsNotExist(err) {
			p.loadErr = fmt.Errorf("read mcp tool pins: %w", err)
		}
		return
	}
	var f toolPinsFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		p.loadErr = fmt.Errorf("parse mcp tool pins: %w", err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for provider, toolMap := range f.Providers {
		if strings.TrimSpace(provider) == "" {
			continue
		}
		copied := make(map[string]string, len(toolMap))
		for tool, fp := range toolMap {
			if strings.TrimSpace(tool) != "" && strings.TrimSpace(fp) != "" {
				copied[tool] = fp
			}
		}
		p.pinned[provider] = copied
	}
}

func (p *toolPins) saveLocked(next map[string]map[string]string) error {
	if p == nil || p.path == "" {
		return fmt.Errorf("mcp tool pin path is not configured")
	}
	out := toolPinsFile{Providers: make(map[string]map[string]string, len(next))}
	for provider, toolMap := range next {
		copied := make(map[string]string, len(toolMap))
		for tool, fp := range toolMap {
			copied[tool] = fp
		}
		out.Providers[provider] = copied
	}

	raw, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal mcp tool pins: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return fmt.Errorf("create mcp tool pin directory: %w", err)
	}
	if _, err := fseffect.Replace(fseffect.ReplaceRequest{
		Location: fseffect.PathLocation(p.path),
		Source:   bytes.NewReader(raw),
		Mode:     0o600,
		DirMode:  0o700,
	}); err != nil {
		return fmt.Errorf("persist mcp tool pins: %w", err)
	}
	return nil
}

// track marks a provider as in scope (remote HTTP) or out of scope.
func (p *toolPins) track(providerID string, remoteHTTP bool) {
	if p == nil || strings.TrimSpace(providerID) == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tracked[providerID] = remoteHTTP
}

// observe records the fingerprint a provider presented for one tool.
// First sight is accepted and persisted; later sightings are compared only.
func (p *toolPins) observe(providerID, toolName, fingerprint string) error {
	if p == nil || !p.inScope(providerID) {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loadErr != nil {
		return p.loadErr
	}
	if p.current[providerID] == nil {
		p.current[providerID] = map[string]string{}
	}
	p.current[providerID][toolName] = fingerprint
	_, hadPin := p.pinned[providerID][toolName]
	if hadPin {
		return nil
	}
	next := cloneToolPinMap(p.pinned)
	if next[providerID] == nil {
		next[providerID] = map[string]string{}
	}
	next[providerID][toolName] = fingerprint
	if err := p.saveLocked(next); err != nil {
		return err
	}
	p.pinned = next
	return nil
}

// changed reports whether the observed definition differs from the pinned one.
// Unknown tools and out-of-scope providers report false.
func (p *toolPins) changed(providerID, toolName string) bool {
	if p == nil || !p.inScope(providerID) {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	pinned, ok := p.pinned[providerID][toolName]
	if !ok || pinned == "" {
		return false
	}
	cur, ok := p.current[providerID][toolName]
	if !ok || cur == "" {
		return false
	}
	return cur != pinned
}

// accept re-pins the currently observed definition after an approved call proceeds.
func (p *toolPins) accept(providerID, toolName string) error {
	if p == nil || !p.inScope(providerID) {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.loadErr != nil {
		return p.loadErr
	}
	cur, ok := p.current[providerID][toolName]
	if !ok || cur == "" {
		return nil
	}
	if p.pinned[providerID][toolName] == cur {
		return nil
	}
	next := cloneToolPinMap(p.pinned)
	if next[providerID] == nil {
		next[providerID] = map[string]string{}
	}
	next[providerID][toolName] = cur
	if err := p.saveLocked(next); err != nil {
		return err
	}
	p.pinned = next
	return nil
}

func cloneToolPinMap(in map[string]map[string]string) map[string]map[string]string {
	out := make(map[string]map[string]string, len(in))
	for provider, tools := range in {
		out[provider] = make(map[string]string, len(tools))
		for tool, fingerprint := range tools {
			out[provider][tool] = fingerprint
		}
	}
	return out
}

func (p *toolPins) inScope(providerID string) bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.tracked[strings.TrimSpace(providerID)]
}

// toolDefinitionFingerprint hashes the sanitized, policy-relevant definition.
func toolDefinitionFingerprint(def sanitizedToolDefinition) (string, error) {
	schema, err := json.Marshal(def.Schema)
	if err != nil {
		return "", fmt.Errorf("encode tool schema: %w", err)
	}
	h := sha256.New()
	fmt.Fprintf(h, "name:%s\x00title:%s\x00desc:%s\x00ro:%t\x00schema:",
		def.Name, def.Title, def.Description, def.ReadOnly)
	h.Write(schema)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// observeToolDefinition records a definition for a remote provider.
func (r *RegistryImpl) observeToolDefinition(providerID string, def sanitizedToolDefinition) error {
	if r == nil || r.pins == nil {
		return nil
	}
	r.pins.track(providerID, r.isRemoteWebProvider(providerID))
	fingerprint, err := toolDefinitionFingerprint(def)
	if err != nil {
		return err
	}
	return r.pins.observe(providerID, def.Name, fingerprint)
}

// observeProviderToolDefinitions validates all pins before publishing a provider's tools.
func (r *RegistryImpl) observeProviderToolDefinitions(providerID string, defs []sanitizedToolDefinition) error {
	for _, def := range defs {
		if err := r.observeToolDefinition(providerID, def); err != nil {
			return fmt.Errorf("pin mcp tool definition %s.%s: %w", providerID, def.Name, err)
		}
	}
	return nil
}

// acceptToolDefinition re-pins the observed definition for a tool whose call is proceeding.
func (r *RegistryImpl) acceptToolDefinition(providerID, toolName string) error {
	if r == nil || r.pins == nil {
		return nil
	}
	return r.pins.accept(providerID, toolName)
}

// ToolDefinitionChanged reports a remote tool-definition mismatch.
func (r *RegistryImpl) ToolDefinitionChanged(qualifiedTool string) bool {
	if r == nil || r.pins == nil {
		return false
	}
	r.mu.RLock()
	ref, ok := r.toolRefs[strings.TrimSpace(qualifiedTool)]
	r.mu.RUnlock()
	if !ok {
		return false
	}
	return r.pins.changed(ref.ProviderID, ref.ToolName)
}

// isRemoteWebProvider reports whether providerID is an HTTP entry whose URL is not loopback.
func (r *RegistryImpl) isRemoteWebProvider(providerID string) bool {
	entry, ok := r.deviceEntry(providerID)
	if !ok {
		return false
	}
	tr, err := entry.Transport()
	if err != nil || tr != TransportHTTP {
		return false
	}
	return !isLoopbackURL(entry.URL)
}
