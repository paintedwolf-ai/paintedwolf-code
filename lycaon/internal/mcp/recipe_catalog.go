package mcp

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/internal/catalogruntime"
)

// RecipeAuth is the credential chrome a bundled MCP recipe declares.
type RecipeAuth string

const (
	RecipeAuthStaticToken RecipeAuth = "static_token"
	RecipeAuthOAuth       RecipeAuth = "oauth"
	RecipeAuthNone        RecipeAuth = "none"
)

// RecipeEnvKey names a stdio env key the operator fills after add.
type RecipeEnvKey struct {
	Key   string `yaml:"key"`
	Label string `yaml:"label"`
}

// Recipe is one bundled MCP provider recipe.
type Recipe struct {
	ID               string         `yaml:"id"`
	Label            string         `yaml:"label"`
	Hint             string         `yaml:"hint"`
	DocsURL          string         `yaml:"docs_url"`
	URL              string         `yaml:"url,omitempty"`
	Command          string         `yaml:"command,omitempty"`
	Args             []string       `yaml:"args,omitempty"`
	Auth             RecipeAuth     `yaml:"auth"`
	CredentialLabel  string         `yaml:"credential_label,omitempty"`
	CredentialHint   string         `yaml:"credential_hint,omitempty"`
	CredentialWire   CredentialWire `yaml:"credential_wire,omitempty"`
	CredentialHeader string         `yaml:"credential_header,omitempty"`
	EnvKeys          []RecipeEnvKey `yaml:"env_keys,omitempty"`
}

// RecipeCatalog is the bundled known-provider list.
type RecipeCatalog struct {
	entries *catalogruntime.Catalog[Recipe]
}

type recipeCatalogFile struct {
	Recipes []Recipe `yaml:"recipes"`
}

// LoadRecipeCatalog reads and validates mcp-recipes.yaml.
func LoadRecipeCatalog() (*RecipeCatalog, error) {
	data, err := config.Read(config.MCPRecipes)
	if err != nil {
		return nil, fmt.Errorf("read mcp recipe catalog: %w", err)
	}
	var raw recipeCatalogFile
	if err := config.DecodeYAML(data, &raw); err != nil {
		return nil, fmt.Errorf("parse mcp recipe catalog: %w", err)
	}
	if len(raw.Recipes) == 0 {
		return nil, fmt.Errorf("mcp recipe catalog: no recipes")
	}
	items := make([]catalogruntime.Item[Recipe], 0, len(raw.Recipes))
	for _, entry := range raw.Recipes {
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			return nil, fmt.Errorf("mcp recipe catalog: entry missing id")
		}
		if err := validateRecipe(id, entry); err != nil {
			return nil, err
		}
		entry.ID = id
		items = append(items, catalogruntime.Item[Recipe]{ID: id, Spec: entry})
	}
	entries, err := catalogruntime.Assemble([]catalogruntime.Layer[Recipe]{
		{Name: "bundled mcp recipe catalog", Items: items},
	}, func(existing catalogruntime.Item[Recipe], exists bool, incoming catalogruntime.Item[Recipe]) (catalogruntime.Item[Recipe], error) {
		if !exists {
			return incoming, nil
		}
		return existing, fmt.Errorf("duplicate id %q", incoming.ID)
	})
	if err != nil {
		return nil, err
	}
	return &RecipeCatalog{entries: entries}, nil
}

func validateRecipe(id string, entry Recipe) error {
	if strings.TrimSpace(entry.Label) == "" {
		return fmt.Errorf("mcp recipe catalog: %q missing label", id)
	}
	if strings.TrimSpace(entry.Hint) == "" {
		return fmt.Errorf("mcp recipe catalog: %q missing hint", id)
	}
	url := strings.TrimSpace(entry.URL)
	cmd := strings.TrimSpace(entry.Command)
	switch {
	case url != "" && cmd != "":
		return fmt.Errorf("mcp recipe catalog: %q names both url and command", id)
	case url == "" && cmd == "":
		return fmt.Errorf("mcp recipe catalog: %q missing url or command", id)
	}
	switch entry.Auth {
	case RecipeAuthStaticToken, RecipeAuthOAuth:
		if url == "" {
			return fmt.Errorf("mcp recipe catalog: %q auth %s requires url", id, entry.Auth)
		}
	case RecipeAuthNone:
	default:
		return fmt.Errorf("mcp recipe catalog: %q invalid auth %q", id, entry.Auth)
	}
	if entry.Auth == RecipeAuthStaticToken && strings.TrimSpace(entry.CredentialLabel) == "" {
		return fmt.Errorf("mcp recipe catalog: %q static_token requires credential_label", id)
	}
	kind, ok := NormalizeCredentialWire(string(entry.CredentialWire))
	if !ok {
		return fmt.Errorf("mcp recipe catalog: %q invalid credential_wire %q", id, entry.CredentialWire)
	}
	header := strings.TrimSpace(entry.CredentialHeader)
	switch kind {
	case CredentialWireHeader:
		if !validCredentialHeader(header) {
			return fmt.Errorf("mcp recipe catalog: %q credential_wire header requires credential_header", id)
		}
	default:
		if header != "" {
			return fmt.Errorf("mcp recipe catalog: %q credential_header requires credential_wire header", id)
		}
	}
	for _, ek := range entry.EnvKeys {
		if strings.TrimSpace(ek.Key) == "" || strings.TrimSpace(ek.Label) == "" {
			return fmt.Errorf("mcp recipe catalog: %q env_keys require key and label", id)
		}
		if cmd == "" {
			return fmt.Errorf("mcp recipe catalog: %q env_keys require command", id)
		}
	}
	if url != "" {
		canonical, _, reject := ClassifyHTTPURL(url)
		if reject != "" {
			return fmt.Errorf("mcp recipe catalog: %q: %s", id, reject)
		}
		if canonical != url {
			return fmt.Errorf("mcp recipe catalog: %q url must be canonical %q", id, canonical)
		}
	}
	return nil
}

// Class derives local vs web from the recipe transport.
func (r Recipe) Class() ProviderClass {
	return providerClass(r.Command, r.URL)
}

// Entry returns one catalog row by id.
func (c *RecipeCatalog) Entry(id string) (Recipe, bool) {
	if c == nil {
		return Recipe{}, false
	}
	item, ok := c.entries.Get(strings.TrimSpace(id))
	if !ok {
		return Recipe{}, false
	}
	return cloneRecipe(item.Spec), true
}

// Entries returns isolated catalog rows in file order.
func (c *RecipeCatalog) Entries() []Recipe {
	if c == nil {
		return nil
	}
	items := c.entries.Items()
	out := make([]Recipe, len(items))
	for i, item := range items {
		out[i] = cloneRecipe(item.Spec)
	}
	return out
}

func cloneRecipe(entry Recipe) Recipe {
	entry.Args = append([]string(nil), entry.Args...)
	if len(entry.EnvKeys) > 0 {
		entry.EnvKeys = append([]RecipeEnvKey(nil), entry.EnvKeys...)
	}
	return entry
}

// OverlayFor converts a recipe into a disabled user-layer overlay row.
func (c *RecipeCatalog) OverlayFor(id string) (MCPProviderOverlay, bool) {
	entry, ok := c.Entry(id)
	if !ok {
		return MCPProviderOverlay{}, false
	}
	off := false
	recipeID := entry.ID
	ov := MCPProviderOverlay{ID: entry.ID, Enabled: &off, Recipe: &recipeID}
	if url := strings.TrimSpace(entry.URL); url != "" {
		ov.URL = &url
		return ov, true
	}
	cmd := strings.TrimSpace(entry.Command)
	ov.Command = &cmd
	if len(entry.Args) > 0 {
		args := append([]string(nil), entry.Args...)
		ov.Args = &args
	}
	return ov, true
}

// ProjectOK reports whether a project overlay could declare this recipe.
func (r Recipe) ProjectOK() bool {
	if r.Auth != RecipeAuthNone {
		return false
	}
	if strings.TrimSpace(r.Command) != "" {
		return false
	}
	_, loopback, reject := ClassifyHTTPURL(r.URL)
	return reject == "" && loopback
}
