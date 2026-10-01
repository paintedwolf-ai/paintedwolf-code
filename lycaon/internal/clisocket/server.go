package clisocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

// SocketName is the socket file inside the config dir.
const SocketName = "cli.sock"

const (
	socketMode = 0o600
	// probeTimeout bounds the liveness dial against an existing socket path.
	probeTimeout = 250 * time.Millisecond
	// connDeadline bounds one CLI exchange. The protocol is a single request and
	// a single reply, so a connection outliving this is a stuck peer, not slow work.
	connDeadline = 5 * time.Second
)

// Ops the CLI can ask for.
const (
	OpList = "ls"
	OpOpen = "open"
)

// Response statuses.
const (
	StatusOK        = "ok"
	StatusError     = "error"
	StatusAmbiguous = "ambiguous"
)

// Request is one CLI invocation.
type Request struct {
	Op     string `json:"op"`
	Target string `json:"target,omitempty"`
	New    bool   `json:"new,omitempty"`
}

// ProjectRow is one project as the CLI renders it.
type ProjectRow struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	LastOpenedAt time.Time `json:"last_opened_at"`
}

// Response is the single reply to a Request.
type Response struct {
	Status   string            `json:"status"`
	Message  string            `json:"message,omitempty"`
	Action   api.CLIOpenAction `json:"action,omitempty"`
	Projects []ProjectRow      `json:"projects,omitempty"`
}

// Projects is the slice of the project registry this server needs.
type Projects interface {
	List(ctx context.Context) ([]project.Project, error)
}

// OpenPublisher hands a resolved open to Den.
type OpenPublisher interface {
	PublishCLIOpen(ctx context.Context, ev api.CLIOpenEvent)
}

// Server answers CLI requests on a Unix socket.
type Server struct {
	projects Projects
	publish  OpenPublisher
	listener net.Listener
	path     string
}

// SocketPath is where the CLI socket lives for this config dir.
func SocketPath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SocketName), nil
}

// Listen binds the CLI socket and serves until Close.
//
// A Unix socket path is capped near 104 bytes on macOS, so a deep config-dir
// override fails to bind. That loses the CLI, not the engine.
func Listen(ctx context.Context, projects Projects, publish OpenPublisher) (*Server, error) {
	path, err := SocketPath()
	if err != nil {
		return nil, err
	}
	if err := clearStaleSocket(ctx, path); err != nil {
		return nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	// The 0700 parent contains the socket until Chmod sets owner-only access.
	if err := os.Chmod(path, socketMode); err != nil {
		_ = listener.Close()
		return nil, err
	}
	s := &Server{projects: projects, publish: publish, listener: listener, path: path}
	go s.serve(ctx)
	return s, nil
}

// Path is the bound socket path.
func (s *Server) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// Close stops serving and unlinks the socket.
func (s *Server) Close() error {
	if s == nil || s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

// clearStaleSocket removes a socket file if no live engine answers connections on it.
func clearStaleSocket(ctx context.Context, path string) error {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dialer := net.Dialer{Timeout: probeTimeout}
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("another engine is already serving %s", path)
	}
	return os.Remove(path)
}

func (s *Server) serve(ctx context.Context) {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(connDeadline))
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = writeResponse(conn, Response{Status: StatusError, Message: "malformed request"})
		return
	}
	_ = writeResponse(conn, s.Handle(ctx, req))
}

func writeResponse(conn net.Conn, resp Response) error {
	return json.NewEncoder(conn).Encode(resp)
}

// Handle answers one request. Exported so tests exercise the decisions without a
// socket in the loop.
func (s *Server) Handle(ctx context.Context, req Request) Response {
	switch req.Op {
	case OpList:
		return s.handleList(ctx)
	case OpOpen:
		return s.handleOpen(ctx, req)
	default:
		return Response{Status: StatusError, Message: fmt.Sprintf("unknown op %q", req.Op)}
	}
}

func (s *Server) handleList(ctx context.Context) Response {
	projects, err := s.projects.List(ctx)
	if err != nil {
		return Response{Status: StatusError, Message: err.Error()}
	}
	rows := make([]ProjectRow, 0, len(projects))
	for i := range projects {
		rows = append(rows, rowFor(projects[i]))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].LastOpenedAt.After(rows[j].LastOpenedAt)
	})
	return Response{Status: StatusOK, Projects: rows}
}

func (s *Server) handleOpen(ctx context.Context, req Request) Response {
	target := strings.TrimSpace(req.Target)
	if target == "" {
		return Response{Status: StatusError, Message: "open needs a folder or a project name"}
	}
	all, err := s.projects.List(ctx)
	if err != nil {
		return Response{Status: StatusError, Message: err.Error()}
	}
	if LooksLikePath(target) {
		return s.openPath(ctx, all, target, req.New)
	}
	return s.openName(ctx, all, target)
}

func (s *Server) openPath(ctx context.Context, all []project.Project, target string, forceNew bool) Response {
	info, err := os.Stat(target)
	if err != nil {
		return Response{Status: StatusError, Message: fmt.Sprintf("no such folder: %s", target)}
	}
	if !info.IsDir() {
		return Response{
			Status:  StatusError,
			Message: fmt.Sprintf("%s is a file — open the folder that contains it", target),
		}
	}
	ev := ResolvePath(all, target)
	// --new creates a second project on a folder that already has one. Without
	// it, opening the same folder twice resolves to the existing project.
	if forceNew {
		ev = api.CLIOpenEvent{Action: api.CLIOpenActionCreate, Path: canonical(target)}
	}
	s.publish.PublishCLIOpen(ctx, ev)
	return Response{Status: StatusOK, Action: ev.Action, Projects: rowsForEvent(all, ev)}
}

// rowsForEvent names the project an open resolved to, so the CLI can echo which
// one it reached rather than leaving the user to check the window.
func rowsForEvent(all []project.Project, ev api.CLIOpenEvent) []ProjectRow {
	if ev.ProjectID == nil {
		return nil
	}
	for i := range all {
		if all[i].ID == *ev.ProjectID {
			return []ProjectRow{rowFor(all[i])}
		}
	}
	return nil
}

func (s *Server) openName(ctx context.Context, all []project.Project, name string) Response {
	matches := ResolveName(all, name)
	switch len(matches) {
	case 0:
		return Response{
			Status:  StatusError,
			Message: fmt.Sprintf("No project named %q — `pw ls` lists them", name),
		}
	case 1:
		id := matches[0].ID
		ev := api.CLIOpenEvent{
			Action:    api.CLIOpenActionOpen,
			ProjectID: &id,
			Path:      project.PrimaryRootPath(&matches[0]),
		}
		s.publish.PublishCLIOpen(ctx, ev)
		return Response{Status: StatusOK, Action: ev.Action, Projects: []ProjectRow{rowFor(matches[0])}}
	default:
		rows := make([]ProjectRow, 0, len(matches))
		for i := range matches {
			rows = append(rows, rowFor(matches[i]))
		}
		return Response{
			Status:   StatusAmbiguous,
			Message:  fmt.Sprintf("%d projects are named %q — open one by its folder instead", len(matches), name),
			Projects: rows,
		}
	}
}

func rowFor(p project.Project) ProjectRow {
	return ProjectRow{
		ID:           p.ID,
		Name:         DisplayName(p),
		Path:         project.PrimaryRootPath(&p),
		LastOpenedAt: p.LastOpenedAt,
	}
}

// CompletionCacheName is the project-name cache the shell completion reads.
const CompletionCacheName = "cli-projects.cache"

// CompletionCachePath returns the shell completion cache path.
func CompletionCachePath() (string, error) {
	dir, err := configdir.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, CompletionCacheName), nil
}
