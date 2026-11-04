package session

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryDockerContainersClosesItsConnection(t *testing.T) {
	dir, err := os.MkdirTemp("", "docker")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	listener, err := net.Listen("unix", filepath.Join(dir, "d.sock"))
	if err != nil {
		t.Fatal(err)
	}
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
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 1 || containers[0].Name != "web" {
		t.Fatalf("containers = %+v", containers)
	}
	select {
	case <-closed:
	case <-time.After(30 * time.Second):
		t.Fatal("docker socket connection stayed open after the query returned")
	}
}
