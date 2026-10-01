package usernotice

import "strings"

// Catalog renders configured user notices.
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

// Config returns the underlying config document.
func (c *Catalog) Config() *Config {
	if c == nil {
		return nil
	}
	return c.cfg
}

// Render resolves user-facing copy for a stable API error code.
func (c *Catalog) Render(code string, ctx map[string]any) (NoticeCopy, bool) {
	if c == nil || c.cfg == nil {
		return NoticeCopy{}, false
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return NoticeCopy{}, false
	}
	entry, ok := c.cfg.UserNotices[code]
	if !ok || !entry.IsUserVisible() {
		return NoticeCopy{}, false
	}
	if entry.UsesDefaults() {
		return c.RenderDefaults(ctx), true
	}
	return RenderCopy(entry, ctx), true
}

// RenderWire returns user-facing copy for code, falling back to defaults.
func (c *Catalog) RenderWire(code string, ctx map[string]any) NoticeCopy {
	if c == nil || c.cfg == nil {
		return NoticeCopy{}
	}
	if copy, ok := c.Render(code, ctx); ok {
		return copy
	}
	return c.RenderDefaults(ctx)
}

// RenderDefaults returns generic fallback copy.
func (c *Catalog) RenderDefaults(ctx map[string]any) NoticeCopy {
	if c == nil || c.cfg == nil {
		return NoticeCopy{}
	}
	return RenderNoticeCopy(c.cfg.Defaults, ctx)
}

// Placement resolves a notice's tier and scope.
func (c *Catalog) Placement(code string, ctx map[string]any) (Resolution, bool) {
	if c == nil || c.cfg == nil {
		return Resolution{}, false
	}
	entry, ok := c.cfg.UserNotices[strings.TrimSpace(code)]
	if !ok || !entry.IsUserVisible() || entry.Notification == nil {
		return Resolution{}, false
	}
	return entry.Notification.Resolve(ctx)
}

// Retryable reads transport policy from the notice entry.
func (c *Catalog) Retryable(code string) bool {
	if c == nil || c.cfg == nil {
		return false
	}
	entry, ok := c.cfg.UserNotices[strings.TrimSpace(code)]
	return ok && entry.IsRetryable()
}
