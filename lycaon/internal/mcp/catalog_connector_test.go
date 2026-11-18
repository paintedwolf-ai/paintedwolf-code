package mcp

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Catalog fixtures discover providers without exposing callable tools.
type catalogConnector struct{}

func (catalogConnector) Connect(context.Context, MCPProviderEntry, ConnectOpts) (ProviderSession, error) {
	return catalogSession{}, nil
}

type catalogSession struct{}

func (catalogSession) ListTools(context.Context) ([]*sdkmcp.Tool, error) { return nil, nil }
func (catalogSession) CallTool(_ context.Context, name string, _ map[string]any) (*sdkmcp.CallToolResult, error) {
	return nil, fmt.Errorf("unexpected catalog fixture tool call %q", name)
}
func (catalogSession) Close() error { return nil }
