package mcp

import (
	"context"
	"fmt"

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

// ListTools reads the connector's current tool map, not a connect-time snapshot, so a
// tools/list_changed re-list sees updates as a real server would report them.
func (m *mockSession) ListTools(_ context.Context) ([]*sdkmcp.Tool, error) {
	if m.parent == nil {
		return nil, nil
	}
	return m.parent.Tools[m.providerID], nil
}

func (m *mockSession) CallTool(_ context.Context, name string, args map[string]any) (*sdkmcp.CallToolResult, error) {
	if m.calls != nil {
		if m.calls[m.providerID] == nil {
			m.calls[m.providerID] = map[string]int{}
		}
		m.calls[m.providerID][name]++
	}
	if m.parent != nil && m.parent.HTTPStatusOnce != nil {
		if code, pending := m.parent.HTTPStatusOnce[m.providerID]; pending {
			delete(m.parent.HTTPStatusOnce, m.providerID)
			if m.onHTTPStatus != nil {
				m.onHTTPStatus(code)
			}
			return nil, fmt.Errorf("mock http status %d", code)
		}
	}
	if m.parent != nil && m.parent.CallArgs != nil {
		if m.parent.CallArgs[m.providerID] == nil {
			m.parent.CallArgs[m.providerID] = map[string]map[string]any{}
		}
		m.parent.CallArgs[m.providerID][name] = args
	}
	if m.parent != nil && m.parent.CallErr != nil {
		if err := m.parent.CallErr[m.providerID]; err != nil {
			return nil, err
		}
	}
	if m.parent != nil && m.parent.CallIsError != nil {
		if text := m.parent.CallIsError[m.providerID]; text != "" {
			return &sdkmcp.CallToolResult{
				Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: text}},
				IsError: true,
			}, nil
		}
	}
	switch name {
	case "intel.search":
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"note":"mock dotted tool"}`}},
		}, nil
	case "list_scans", "get_scan_summary", "query_findings":
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"note":"mock mcp server"}`}},
		}, nil
	case "scan":
		raw := `{"results":[{"check_id":"lycaon.test.rule","path":"a.go","start":{"line":1},"extra":{"message":"test","severity":"ERROR"}}]}`
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: raw}},
		}, nil
	case "query":
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"results":[]}`}},
		}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func (m *mockSession) Close() error { return nil }
