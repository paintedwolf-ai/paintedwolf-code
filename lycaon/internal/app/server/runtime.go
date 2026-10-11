package server

import (
	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/coordinator"
	"github.com/lycaon/lycaon/internal/historyretention"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/projectliveness"
	"github.com/lycaon/lycaon/internal/usernotice"
)

// Runtime holds the HTTP server, coordinator, orchestrator, MCP, and notification runtimes.
type Runtime struct {
	Server          *api.Server
	MCP             *mcp.Runtime
	Orchestrator    orchestration.Orchestrator
	Coordinator     *coordinator.Runtime
	HistoryStorage  *historyretention.Service
	UserNotices     *usernotice.Catalog
	ProjectLiveness *projectliveness.Tracker
}
