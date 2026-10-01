package approvaloutcome

import "strings"

// Catalog renders approval outcomes.
type Catalog struct {
	cfg *Config
}

// NewCatalog wraps a validated config for runtime rendering.
func NewCatalog(cfg *Config) *Catalog {
	if cfg == nil {
		return nil
	}
	return &Catalog{cfg: cfg}
}

// Message renders copy or returns its stable code.
func (c *Catalog) Message(code string, ctx map[string]any) string {
	code = strings.TrimSpace(code)
	if c == nil || c.cfg == nil || code == "" {
		return code
	}
	entry, ok := c.cfg.Outcomes[code]
	if !ok {
		return code
	}
	if msg := RenderMessage(entry, ctx); msg != "" {
		return msg
	}
	return code
}
