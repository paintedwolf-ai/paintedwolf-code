package app

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/startupprotocol"
	"github.com/lycaon/lycaon/internal/testutil"
)

type recordingSink struct {
	mu     sync.Mutex
	phases []startupprotocol.Phase
	port   int
	failed string
	ready  chan struct{}
	once   sync.Once
}

func TestRecoveryCompletionFailurePreventsServing(t *testing.T) {
	t.Setenv(profileAddrEnv, "")
	sink := newRecordingSink()
	failure := errors.New("recovery metadata publication failed")
	app := &ServeApp{
		ListenAddr: "127.0.0.1:0",
		APIToken:   "recovery-completion-test-token",
		resources:  &runtimeResources{},
		startup:    sink,
		upgradeRecoveryReady: func() error {
			return failure
		},
	}
	err := app.Run(t.Context())
	if !errors.Is(err, failure) {
		t.Fatalf("Run error=%v; want recovery publication failure", err)
	}
	if app.resources.httpServer != nil || sink.port != 0 || sink.failed != "serve_failed" {
		t.Fatalf("failed completion exposed readiness: server=%v port=%d failure=%q", app.resources.httpServer, sink.port, sink.failed)
	}
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", app.ListenAddr)
	testutil.FailErr(t, "rebind released listener", err)
	testutil.FailErr(t, "close test listener", listener.Close())
}

func newRecordingSink() *recordingSink {
	return &recordingSink{ready: make(chan struct{})}
}

func (s *recordingSink) Phase(p startupprotocol.Phase) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phases = append(s.phases, p)
	return nil
}

func (s *recordingSink) Ready(port int) error {
	s.mu.Lock()
	s.port = port
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
	return nil
}

func (s *recordingSink) Failed(code string) error {
	s.mu.Lock()
	s.failed = code
	s.mu.Unlock()
	s.once.Do(func() { close(s.ready) })
	return nil
}

// Recovery mode reports ready after binding its recovery routes.
func TestRecoveryBootReportsReadyToTheStartupProtocol(t *testing.T) {
	t.Setenv("LYCAON_LLM_MOCK", "1")
	t.Setenv("LYCAON_API_TOKEN", "test-token")
	t.Setenv("LYCAON_TEST", "1")

	cfg := recoveryTestConfig(t)
	cfg.ListenAddr = "127.0.0.1:0"
	sink := newRecordingSink()
	cfg.Startup = sink

	sqlDB, err := db.Open(cfg.DBPath)
	testutil.FailErr(t, "seed Open", err)
	_, err = sqlDB.ExecContext(t.Context(), `PRAGMA user_version = 2`)
	testutil.FailErr(t, "change baseline marker", err)
	testutil.FailErr(t, "close seed store", sqlDB.Close())

	app, err := Build(t.Context(), cfg)
	testutil.FailErr(t, "build recovery app", err)
	t.Cleanup(func() { _ = app.Close() })

	if body := getHealth(t, app.Server); body["status"] != "recovery" {
		t.Fatalf("status = %v; want recovery", body["status"])
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	select {
	case <-sink.ready:
	case err := <-done:
		t.Fatalf("recovery Run returned before reporting ready: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("recovery boot never reported ready — the startup stage would wait forever")
	}

	sink.mu.Lock()
	port, failed := sink.port, sink.failed
	sink.mu.Unlock()
	if failed != "" {
		t.Fatalf("recovery boot reported failure %q; want ready", failed)
	}
	if port <= 0 {
		t.Fatalf("ready port = %d; want the bound recovery port", port)
	}

	cancel()
	<-done
}
