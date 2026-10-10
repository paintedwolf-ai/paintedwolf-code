package researchwarm

import (
	"context"
	"github.com/lycaon/lycaon/pkg/api"
	"strings"
	"testing"
)

type warmingStore struct{}

func (warmingStore) Get(context.Context, string) (*api.Session, error) {
	return &api.Session{ProjectID: "project"}, nil
}

type warmingAdmission struct{ t *testing.T }

func (a warmingAdmission) WithSessionTreeAdmission(ctx context.Context, id string, f func() error) error {
	if id != "session" || ctx.Err() != nil {
		a.t.Fatalf("warming admission %q %v", id, ctx.Err())
	}
	return f()
}

type warmingRecorder struct {
	Warmer
	t        *testing.T
	callback func(api.IndexWarmingMeta)
	kind     string
}

func (w *warmingRecorder) WarmSearchAsync(query, project, dir, session, call string, hits, residual []string, strong, max int, direct bool, done func(api.IndexWarmingMeta)) {
	if query != "query" || project != "project" || session != "session" || call != "call" || len(hits) != 1 || !direct {
		w.t.Fatalf("unattributed search: %q %q %q %q %v", query, project, session, call, hits)
	}
	w.kind = "search"
	w.callback = done
}
func (w *warmingRecorder) WarmFetchAsync(url, title, project, dir, session, call string, done func(api.IndexWarmingMeta)) {
	if url != "https://example.test/page" || project != "project" || session != "session" || call != "call" {
		w.t.Fatalf("unattributed fetch: %q %q %q %q", url, project, session, call)
	}
	w.kind = "fetch"
	w.callback = done
}

func TestWarmCompletionRetainsAttributionAfterRequestCancellation(t *testing.T) {
	for _, kind := range []string{"search", "fetch"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var row api.Message
			var recordedSession string
			service := New(warmingStore{}, warmingAdmission{t}, func(ctx context.Context, id string, messages ...api.Message) error {
				if ctx.Err() != nil {
					t.Fatalf("completed warm reused canceled request: %v", ctx.Err())
				}
				recordedSession = id
				row = messages[0]
				return nil
			})
			recorder := &warmingRecorder{t: t}
			service.SetWarmer(recorder)
			if kind == "search" {
				service.Search(ctx, "session", "call", "query", "/project", []string{"https://example.test/page"}, nil, 1, 4, true)
			} else {
				service.Fetch(ctx, "session", "call", "https://example.test/page", "Page", "/project")
			}
			if recorder.kind != kind || recorder.callback == nil {
				t.Fatalf("warming not admitted: %+v", recorder)
			}
			cancel()
			recorder.callback(api.IndexWarmingMeta{Pages: 1, Hosts: []string{"example.test"}})
			if recordedSession != "session" || row.Kind != api.MessageKindIndexWarming || row.IndexWarming == nil || row.IndexWarming.Pages != 1 {
				t.Fatalf("completion lost attribution: %q %+v", recordedSession, row)
			}
		})
	}
}

func TestIndexWarmingSummaryLine(t *testing.T) {
	got := indexWarmingSummary(api.IndexWarmingMeta{
		Topic: "steam machine", Hosts: []string{"a", "b"}, Pages: 1, DurationMs: 1200,
	})
	if got != "Warmed web index — 2 hosts, 1 page · steam machine" {
		t.Fatalf("summary = %q", got)
	}
	skip := indexWarmingSummary(api.IndexWarmingMeta{Topic: "x", SkipReason: "hourly seed cap"})
	if !strings.Contains(skip, "hourly seed cap") {
		t.Fatalf("summary = %q", skip)
	}
	partial := indexWarmingSummary(api.IndexWarmingMeta{
		Topic: "widget", Hosts: []string{"a.example"}, Pages: 2, SkipReason: "hourly seed cap",
	})
	if strings.Contains(partial, "hourly seed cap") {
		t.Fatalf("summary = %q want no skip when crawl succeeded", partial)
	}
}
