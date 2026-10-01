package workflowdiag

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/pkg/api"
)

// Entry is one author-facing diagnostic definition.
type Entry struct {
	Code        string `yaml:"code"`
	Message     string `yaml:"message"`
	Replacement string `yaml:"replacement"`
}

// Catalog is the loaded author-facing registry (message/replacement templates).
type Catalog struct {
	entries map[Code]Entry
}

var (
	defaultMu  sync.RWMutex
	defaultGen uint64
	defaultCat *Catalog
	defaultErr error
)

// Load reads the bundled workflow-diagnostics catalog.
func Load() (*Catalog, error) {
	entries, err := config.List(config.SharedWorkflowDiag)
	if err != nil {
		return nil, err
	}
	out := map[Code]Entry{}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".yaml") {
			continue
		}
		rel := config.SharedWorkflowDiag.Join(ent.Name())
		data, err := config.Read(rel)
		if err != nil {
			return nil, err
		}
		var e Entry
		if err := config.DecodeYAML(data, &e); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		code := Code(strings.TrimSpace(e.Code))
		if code == "" {
			return nil, fmt.Errorf("%s: code required", rel)
		}
		wantName := string(code) + ".yaml"
		if ent.Name() != wantName {
			return nil, fmt.Errorf("%s: filename must be %s", rel, wantName)
		}
		if strings.TrimSpace(e.Message) == "" {
			return nil, fmt.Errorf("%s: message required", rel)
		}
		e.Code = string(code)
		e.Message = strings.TrimSpace(e.Message)
		e.Replacement = strings.TrimSpace(e.Replacement)
		if _, dup := out[code]; dup {
			return nil, fmt.Errorf("duplicate diagnostic code %q", code)
		}
		out[code] = e
	}
	return &Catalog{entries: out}, nil
}

// Default returns the process-wide catalog from bundled config.
func Default() (*Catalog, error) {
	gen := config.SourceGeneration()
	defaultMu.RLock()
	if defaultCat != nil && defaultGen == gen && defaultErr == nil {
		cat := defaultCat
		defaultMu.RUnlock()
		return cat, nil
	}
	defaultMu.RUnlock()

	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultCat != nil && defaultGen == gen && defaultErr == nil {
		return defaultCat, nil
	}
	defaultCat, defaultErr = Load()
	defaultGen = gen
	return defaultCat, defaultErr
}

// Has reports whether code is in the catalog.
func (c *Catalog) Has(code Code) bool {
	if c == nil {
		return false
	}
	_, ok := c.entries[code]
	return ok
}

// Codes returns catalog keys sorted by string value.
func (c *Catalog) Codes() []Code {
	if c == nil {
		return nil
	}
	out := make([]Code, 0, len(c.entries))
	for code := range c.entries {
		out = append(out, code)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Emit renders one diagnostic. It panics if code ∉ Catalog — emission is only
// possible through a registered Code.
func (c *Catalog) Emit(code Code, field string, data map[string]any) api.ComposeValidationError {
	if c == nil {
		panic("workflowdiag: nil catalog")
	}
	e, ok := c.entries[code]
	if !ok {
		panic(fmt.Sprintf("workflowdiag: unregistered code %q", code))
	}
	return api.ComposeValidationError{
		Field:       strings.TrimSpace(field),
		Code:        string(code),
		Message:     renderTemplate(e.Message, data),
		Replacement: renderTemplate(e.Replacement, data),
	}
}

// EmitDefault renders one diagnostic from the default catalog.
func EmitDefault(code Code, field string, data map[string]any) api.ComposeValidationError {
	c, err := Default()
	if err != nil {
		panic(fmt.Sprintf("workflowdiag: default catalog: %v", err))
	}
	return c.Emit(code, field, data)
}

// Summarize joins diagnostic messages for boot fail-closed errors.
func Summarize(diags []api.ComposeValidationError) string {
	if len(diags) == 0 {
		return ""
	}
	parts := make([]string, 0, len(diags))
	for _, d := range diags {
		s := fmt.Sprintf("%s: %s", d.Code, d.Message)
		if r := strings.TrimSpace(d.Replacement); r != "" {
			s += " Instead: " + r
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

func renderTemplate(tmpl string, data map[string]any) string {
	if tmpl == "" || len(data) == 0 {
		return tmpl
	}
	out := tmpl
	for k, v := range data {
		out = strings.ReplaceAll(out, "{{"+k+"}}", fmt.Sprint(v))
	}
	return out
}
