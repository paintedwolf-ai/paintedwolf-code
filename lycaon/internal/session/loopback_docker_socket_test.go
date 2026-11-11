package session

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestQueryDockerContainersClosesItsConnection(t *testing.T) {
	dir, err := os.MkdirTemp("", "docker") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.Listen("unix", filepath.Join(dir, "d.sock"))
	testutil.FailErr(t, "listen on docker socket", err)
	closed := make(chan struct{})
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"Id":"abc","Names":["/web"]}]`))
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				close(closed)
			}
		},
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	containers, err := queryDockerContainersOverSocket(context.Background(), listener.Addr().String())
	testutil.FailErr(t, "query docker containers", err)
	if len(containers) != 1 || containers[0].Name != "web" {
		t.Fatalf("containers = %+v", containers)
	}
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("docker socket connection stayed open after the query returned")
	}
}
