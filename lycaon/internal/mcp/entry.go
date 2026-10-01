package mcp

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http"
)

// ProviderClass is host-derived local vs web.
type ProviderClass string

const (
	ProviderClassLocal ProviderClass = "local"
	ProviderClassWeb   ProviderClass = "web"
)

// MCPProviderEntry is one MCP provider row from distro, user, or merged catalog.
type MCPProviderEntry struct {
	ID          string             `yaml:"id"`
	URL         string             `yaml:"url,omitempty"`
	Command     string             `yaml:"command,omitempty"`
	Args        []string           `yaml:"args,omitempty"`
	Env         map[string]string  `yaml:"env,omitempty"`
	Headers     map[string]string  `yaml:"headers,omitempty"`
	Token       string             `yaml:"token,omitempty"`
	Version     string             `yaml:"version,omitempty"`
	Enabled     bool               `yaml:"enabled"`
	ToolLoading api.McpToolLoading `yaml:"tool_loading,omitempty"`
	// Recipe is the bundled catalog id. Empty for Custom.
	Recipe string `yaml:"recipe,omitempty"`
	// Empty CredentialWire means bearer.
	CredentialWire   string `yaml:"credential_wire,omitempty"`
	CredentialHeader string `yaml:"credential_header,omitempty"`
}

// NormalizedToolLoading returns the effective schema loading mode.
func (e MCPProviderEntry) NormalizedToolLoading() api.McpToolLoading {
	if e.ToolLoading == api.McpToolLoadingAlways {
		return api.McpToolLoadingAlways
	}
	return api.McpToolLoadingAuto
}

// AlwaysLoadsTools reports whether every eligible call receives this provider's schemas.
func (e MCPProviderEntry) AlwaysLoadsTools() bool {
	return e.NormalizedToolLoading() == api.McpToolLoadingAlways
}

// HasStaticHTTPAuth reports whether static HTTP credentials are configured.
func (e MCPProviderEntry) HasStaticHTTPAuth() bool {
	if strings.TrimSpace(e.Token) != "" {
		return true
	}
	return len(e.Headers) > 0
}

// Transport reports stdio vs streamable HTTP from the entry shape.
func (e MCPProviderEntry) Transport() (Transport, error) {
	url := strings.TrimSpace(e.URL)
	cmd := strings.TrimSpace(e.Command)
	switch {
	case url != "" && cmd != "":
		return "", fmt.Errorf("mcp provider %q: specify url or command, not both", e.ID)
	case url != "":
		return TransportHTTP, nil
	case cmd != "":
		return TransportStdio, nil
	default:
		return "", fmt.Errorf("mcp provider %q: missing url or command", e.ID)
	}
}

// Class derives local vs web from transport and URL facts.
func (e MCPProviderEntry) Class() ProviderClass {
	return providerClass(e.Command, e.URL)
}

func providerClass(command, rawURL string) ProviderClass {
	if strings.TrimSpace(command) != "" {
		return ProviderClassLocal
	}
	_, loopback, reject := ClassifyHTTPURL(rawURL)
	if reject == "" && loopback {
		return ProviderClassLocal
	}
	return ProviderClassWeb
}

// Validate checks a catalog row before connect.
func (e MCPProviderEntry) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("mcp provider: missing id")
	}
	if _, err := e.Transport(); err != nil {
		return err
	}
	if !validOptionalToolLoading(e.ToolLoading) {
		return fmt.Errorf("mcp provider %q: invalid tool_loading %q", e.ID, e.ToolLoading)
	}
	return nil
}

func validToolLoading(mode api.McpToolLoading) bool {
	return mode == api.McpToolLoadingAuto || mode == api.McpToolLoadingAlways
}

func validOptionalToolLoading(mode api.McpToolLoading) bool {
	return mode == "" || validToolLoading(mode)
}
