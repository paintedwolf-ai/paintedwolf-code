// Command fake_stdio_server is a minimal MCP stdio fixture for confined-spawn /
// roots / caps / error-code bridge tests.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	// Protocol 2026-07-28 forbids server-initiated requests, so list_roots
	// needs a session negotiated at an earlier version.
	protocol := flag.String("protocol", "", "restrict the server to one MCP protocol version")
	flag.Parse()
	var opts *mcp.ServerOptions
	if *protocol != "" {
		opts = &mcp.ServerOptions{SupportedProtocolVersions: []string{*protocol}}
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "fake_stdio", Version: "0.0.1"}, opts)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_roots",
		Description: "Return client-advertised roots as text",
	}, listRoots)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "echo",
		Description: "Echo a message",
	}, echo)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fail_coded",
		Description: "Return IsError with a declared code",
	}, failCoded)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fail_plain",
		Description: "Return IsError without a declared code",
	}, failPlain)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "huge",
		Description: "Return an oversized JSON payload",
	}, huge)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func listRoots(ctx context.Context, req *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
	res, err := req.Session.ListRoots(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	uris := make([]string, 0, len(res.Roots))
	for _, r := range res.Roots {
		uris = append(uris, r.URI)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: strings.Join(uris, "\n")}},
	}, nil, nil
}

type echoArgs struct {
	Message string `json:"message"`
}

func echo(_ context.Context, _ *mcp.CallToolRequest, in echoArgs) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: in.Message}},
	}, nil, nil
}

func failCoded(_ context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
	payload := map[string]any{"code": "FIXTURE_MCP_DENIED", "message": "fixture denied"}
	raw, _ := json.Marshal(payload)
	return &mcp.CallToolResult{
		IsError:           true,
		StructuredContent: payload,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(raw)}},
	}, nil, nil
}

func failPlain(_ context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: "something went wrong without a code"}},
	}, nil, nil
}

func huge(_ context.Context, _ *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
	body := map[string]any{"blob": strings.Repeat("x", 400_000)}
	raw, _ := json.Marshal(body)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}},
	}, nil, nil
}
