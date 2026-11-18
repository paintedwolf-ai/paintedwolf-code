package mcptest

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/mcp"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Connector returns predefined tools without spawning subprocesses.
type Connector struct {
	Tools       map[string][]*sdkmcp.Tool
	Err         map[string]error
	CallErr     map[string]error
	CallIsError map[string]string
	Calls       map[string]map[string]int
	// CallArgs records the most recent CallTool args per provider/tool when non-nil.
	CallArgs map[string]map[string]map[string]any
	// ConnectCount records connections per provider.
	ConnectCount map[string]int
	// HTTPStatusOnce reports one transport failure on the next call.
	HTTPStatusOnce map[string]int
}

func (m *Connector) Connect(_ context.Context, entry mcp.MCPProviderEntry, opts mcp.ConnectOpts) (mcp.ProviderSession, error) {
	if m.Err != nil {
		if err := m.Err[entry.ID]; err != nil {
			return nil, err
		}
	}
	if m.ConnectCount != nil {
		m.ConnectCount[entry.ID]++
	}
	return &session{providerID: entry.ID, calls: m.Calls, parent: m, onHTTPStatus: opts.OnHTTPStatus}, nil
}

type session struct {
	providerID   string
	calls        map[string]map[string]int
	parent       *Connector
	onHTTPStatus func(int)
}

// ListTools observes updates made after connection.
func (m *session) ListTools(_ context.Context) ([]*sdkmcp.Tool, error) {
	if m.parent == nil {
		return nil, nil
	}
	return m.parent.Tools[m.providerID], nil
}

func (m *session) CallTool(_ context.Context, name string, args map[string]any) (*sdkmcp.CallToolResult, error) {
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

func (m *session) Close() error { return nil }
