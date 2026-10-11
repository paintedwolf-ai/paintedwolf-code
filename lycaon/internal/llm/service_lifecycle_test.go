package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/configdir"
	providercredentials "github.com/lycaon/lycaon/internal/llm/credentials"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestServiceCloseWaitsForModelFeedRefresh(t *testing.T) {
	refreshCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	exited := make(chan struct{})
	go func() {
		defer close(done)
		<-refreshCtx.Done()
		close(exited)
	}()

	service := &Service{
		modelFeedRefreshCancel: cancel,
		modelFeedRefreshDone:   done,
	}
	testutil.FailErr(t, "close service", service.Close(t.Context()))
	select {
	case <-exited:
	default:
		t.Fatal("model feed refresh still running")
	}
}

func TestCanceledServiceAllocationDoesNotCreateDeviceState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, filepath.Join(home, "device"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	service, err := NewService(ctx, nil)
	if service != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled allocation = %v, %v", service, err)
	}
	entries, err := os.ReadDir(home)
	testutil.FailErr(t, "read untouched device directory", err)
	if len(entries) != 0 {
		t.Fatalf("canceled allocation created device state: %v", entries)
	}
}

func TestCanceledAllocationDrainsInitialProviderDiscovery(t *testing.T) {
	for _, kind := range []string{"registry", "service"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv(configdir.EnvConfigDir, t.TempDir())
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/tags" {
					http.NotFound(w, r)
					return
				}
				close(started)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-release:
				}
			}))
			t.Cleanup(server.Close)
			t.Cleanup(func() { close(release) })
			yaml := "providers:\n  - id: ollama\n    kind: ollama\n    base_url: " + server.URL + "/v1\n    api_key_env: \"\"\n    models: []\n" + MinimalShipHTTPRetryYAML
			stageShipProviders(t, yaml)
			local, err := userProvidersLocalPath()
			testutil.FailErr(t, "provider path", err)
			writeProvidersLocal(t, local, []byte(yaml))
			catalog, err := NewProviderCatalog()
			testutil.FailErr(t, "provider catalog", err)
			ctx, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			type result struct {
				allocated bool
				err       error
			}
			done := make(chan result, 1)
			go func() {
				if kind == "service" {
					service, err := NewService(ctx, nil)
					done <- result{service != nil, err}
					return
				}
				registry, err := NewRegistry(ctx, catalog, providercredentials.NewAt(filepath.Join(t.TempDir(), "credential-vault.age")))
				done <- result{registry != nil, err}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("initial discovery did not reach the provider")
			}
			cancel()
			select {
			case got := <-done:
				if got.allocated || !errors.Is(got.err, context.Canceled) {
					t.Fatalf("canceled allocation = %+v", got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("constructor did not join canceled discovery")
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("constructor left its provider request running")
			}
		})
	}
}

func TestServiceCloseReportsCanceledJoinAndCanFinishAfterRefreshExits(t *testing.T) {
	done := make(chan struct{})
	canceled := false
	service := &Service{
		modelFeedRefreshCancel: func() { canceled = true },
		modelFeedRefreshDone:   done,
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := service.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("unfinished refresh join error=%v, want cancellation", err)
	}
	if !canceled {
		t.Fatal("close did not request refresh cancellation before refusing the join")
	}
	select {
	case <-done:
		t.Fatal("canceled join invented refresh completion")
	default:
	}
	close(done)
	testutil.FailErr(t, "finish service shutdown after refresh exits", service.Close(t.Context()))
}
