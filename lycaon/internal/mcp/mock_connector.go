package mcp

import (
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MockConnector returns predefined tools without spawning subprocesses.
type MockConnector struct {
	Tools       map[string][]*sdkmcp.Tool
	Err         map[string]error
	CallErr     map[string]error
	CallIsError map[string]string
	Calls       map[string]map[string]int
	// CallArgs records the most recent CallTool args per provider/tool when non-nil.
	CallArgs map[string]map[string]map[string]any
	// ConnectCount records how many times Connect ran per provider, so a test can
	// prove a stale pooled session was evicted rather than reused.
	ConnectCount map[string]int
	// HTTPStatusOnce fires opts.OnHTTPStatus with the given code on the provider's
	// next CallTool, then clears itself; the call fails as on a real 401/403.
	HTTPStatusOnce map[string]int
}

func (m *MockConnector) Connect(_ context.Context, entry MCPProviderEntry, opts ConnectOpts) (ProviderSession, error) {
	if m.Err != nil {
		if err := m.Err[entry.ID]; err != nil {
			return nil, err
		}
	}
	if m.ConnectCount != nil {
		m.ConnectCount[entry.ID]++
	}
	return &mockSession{providerID: entry.ID, calls: m.Calls, parent: m, onHTTPStatus: opts.OnHTTPStatus}, nil
}

type mockSession struct {
	providerID   string
	calls        map[string]map[string]int
	parent       *MockConnector
	onHTTPStatus func(int)
}

func (m *mockSession) Close() error { return nil }
