package httpclient

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/netip"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/testutil"
)

func feedTestOptions() FeedOptions {
	return FeedOptions{Class: egressclass.PricingFeedRefresh, Timeout: time.Second, MaxBytes: 16, UserAgent: "feed-test/1.0"}
}

type feedTestTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
	closed    int
}

func (f *feedTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f.roundTrip(r) }
func (f *feedTestTransport) CloseIdleConnections()                             { f.closed++ }

type feedTestBody struct {
	io.Reader
	closed bool
}

func (b *feedTestBody) Close() error { b.closed = true; return nil }

func feedTestNetwork(tr *feedTestTransport) feedNetwork {
	return feedNetwork{
		resolve:   egress.ResolvePublicIPs,
		transport: func(string, []netip.Addr) feedTransport { return tr },
	}
}

func TestFeedRejectsUnsafeDestinations(t *testing.T) {
	for _, rawURL := range []string{
		"http://8.8.8.8/feed", "file:///feed", "https:///feed", "https://8.8.8.8:0/feed",
		"https://8.8.8.8:65536/feed", "https://8.8.8.8:bad/feed", "https://%zz/feed",
		"https://127.0.0.1/feed", "https://169.254.169.254/feed", "https://10.0.0.1/feed",
		"https://[::1]/feed", "https://[::ffff:127.0.0.1]/feed", "https://[fc00::1]/feed",
	} {
		t.Run(rawURL, func(t *testing.T) {
			body, err := GetFeed(t.Context(), rawURL, feedTestOptions())
			if err == nil || body != nil {
				t.Fatalf("unsafe fetch = %q, %v", body, err)
			}
		})
	}
}

func TestFeedRedirectsRevalidateAndCloseEveryHop(t *testing.T) {
	for _, code := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			firstBody := &feedTestBody{Reader: strings.NewReader("redirect body")}
			finalBody := &feedTestBody{Reader: strings.NewReader(`{"ok":true}`)}
			var urls, hosts []string
			tr := &feedTestTransport{roundTrip: func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.Header.Get("Accept") != "application/json" || r.UserAgent() != "feed-test/1.0" {
					t.Fatalf("request method/headers = %s %v", r.Method, r.Header)
				}
				if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
					t.Fatal("feed sent credentials")
				}
				urls = append(urls, r.URL.String())
				if len(urls) == 1 {
					return &http.Response{StatusCode: code, Header: http.Header{"Location": {"https://ignored:credential@1.1.1.1/next#fragment"}}, Body: firstBody}, nil
				}
				return &http.Response{StatusCode: 200, Body: finalBody}, nil
			}}
			network := feedTestNetwork(tr)
			network.transport = func(host string, ips []netip.Addr) feedTransport {
				hosts = append(hosts, host)
				if len(ips) != 1 || ips[0].String() != host {
					t.Fatalf("pin = %v for %s", ips, host)
				}
				if len(hosts) > 1 && (!firstBody.closed || tr.closed != 1) {
					t.Fatal("previous hop not released")
				}
				return tr
			}
			body, err := getFeed(t.Context(), "https://ignored:credential@8.8.8.8/start#fragment", feedTestOptions(), network)
			testutil.FailErr(t, "fetch redirect", err)
			if string(body) != `{"ok":true}` || !reflect.DeepEqual(urls, []string{"https://8.8.8.8/start", "https://1.1.1.1/next"}) || tr.closed != 2 || !finalBody.closed {
				t.Fatalf("body=%s urls=%v closes=%d bodyClosed=%v", body, urls, tr.closed, finalBody.closed)
			}
		})
	}
}

func TestFeedRedirectRefusals(t *testing.T) {
	for _, location := range []string{"", "%zz", "http://8.8.8.8/next", "https://127.0.0.1/next", "https://169.254.169.254/next", "https://[::1]/next"} {
		t.Run(location, func(t *testing.T) {
			b := &feedTestBody{Reader: strings.NewReader("")}
			calls := 0
			tr := &feedTestTransport{roundTrip: func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 302, Header: http.Header{"Location": {location}}, Body: b}, nil
			}}
			body, err := getFeed(t.Context(), "https://8.8.8.8/feed", feedTestOptions(), feedTestNetwork(tr))
			if err == nil || body != nil || calls != 1 || !b.closed || tr.closed != 1 {
				t.Fatalf("body=%q err=%v calls=%d closed=%v/%d", body, err, calls, b.closed, tr.closed)
			}
		})
	}
}

func TestFeedRedirectBudgetAndRelativeLocation(t *testing.T) {
	for _, redirects := range []int{5, 6} {
		t.Run(strconv.Itoa(redirects), func(t *testing.T) {
			calls := 0
			tr := &feedTestTransport{roundTrip: func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 && r.URL.Path != "/next" {
					t.Fatalf("relative redirect resolved to %s", r.URL)
				}
				resp := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}
				if calls <= redirects {
					resp.StatusCode = 302
					resp.Header = http.Header{"Location": {"../next"}}
				}
				return resp, nil
			}}
			body, err := getFeed(t.Context(), "https://8.8.8.8/dir/feed", feedTestOptions(), feedTestNetwork(tr))
			if calls != 6 || tr.closed != 6 || (redirects == 5 && (err != nil || string(body) != "ok")) || (redirects == 6 && (err == nil || body != nil)) {
				t.Fatalf("redirects=%d calls=%d closed=%d body=%q err=%v", redirects, calls, tr.closed, body, err)
			}
		})
	}
}

func TestFeedRejectsInvalidBounds(t *testing.T) {
	for _, opts := range []FeedOptions{
		{Class: egressclass.PricingFeedRefresh, Timeout: 0, MaxBytes: 16},
		{Class: egressclass.PricingFeedRefresh, Timeout: time.Second, MaxBytes: 0},
		{Class: egressclass.PricingFeedRefresh, Timeout: time.Second, MaxBytes: -1},
		{Class: egressclass.PricingFeedRefresh, Timeout: time.Second, MaxBytes: math.MaxInt64},
	} {
		if _, err := getFeed(t.Context(), "https://8.8.8.8/feed", opts, feedNetwork{}); err == nil {
			t.Fatalf("accepted bounds %+v", opts)
		}
	}
}

func TestFeedPreservesTransportFailureAndCloses(t *testing.T) {
	failure := errors.New("connection failed")
	tr := &feedTestTransport{roundTrip: func(*http.Request) (*http.Response, error) { return nil, failure }}
	_, err := getFeed(t.Context(), "https://8.8.8.8/feed", feedTestOptions(), feedTestNetwork(tr))
	if !errors.Is(err, failure) || tr.closed != 1 {
		t.Fatalf("err=%v closed=%d", err, tr.closed)
	}
}

func TestFeedResolvesAgainOnSameHostRedirect(t *testing.T) {
	calls := 0
	denied := &egress.DestinationDeniedError{Reason: "host now resolves to a private address"}
	tr := &feedTestTransport{roundTrip: func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/next"}}, Body: http.NoBody}, nil
	}}
	network := feedTestNetwork(tr)
	network.resolve = func(context.Context, string) ([]netip.Addr, error) {
		calls++
		if calls == 1 {
			return []netip.Addr{netip.MustParseAddr("8.8.8.8")}, nil
		}
		return nil, denied
	}
	_, err := getFeed(t.Context(), "https://feed.example/start", feedTestOptions(), network)
	if !errors.Is(err, denied) || calls != 2 || tr.closed != 1 {
		t.Fatalf("err=%v resolutions=%d transports=%d", err, calls, tr.closed)
	}
}
