package mcp_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/mcp"
)

func TestMCPProviderEntryTransport(t *testing.T) {
	t.Run("http", func(t *testing.T) {
		e := mcp.MCPProviderEntry{ID: "loopback", URL: "http://127.0.0.1:8765/mcp"}
		got, err := e.Transport()
		if err != nil {
			t.Fatalf("Transport: %v", err)
		}
		if got != mcp.TransportHTTP {
			t.Fatalf("transport = %q want http", got)
		}
	})

	t.Run("stdio", func(t *testing.T) {
		e := mcp.MCPProviderEntry{ID: "stdio", Command: mcp.DistroCommandSelf}
		got, err := e.Transport()
		if err != nil {
			t.Fatalf("Transport: %v", err)
		}
		if got != mcp.TransportStdio {
			t.Fatalf("transport = %q want stdio", got)
		}
	})

	t.Run("reject both", func(t *testing.T) {
		e := mcp.MCPProviderEntry{ID: "x", URL: "http://127.0.0.1/mcp", Command: "true"}
		_, err := e.Transport()
		if err == nil || !strings.Contains(err.Error(), "not both") {
			t.Fatalf("Transport = %v want both-specified error", err)
		}
	})

	t.Run("reject neither", func(t *testing.T) {
		e := mcp.MCPProviderEntry{ID: "x"}
		_, err := e.Transport()
		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("Transport = %v want missing error", err)
		}
	})
}
