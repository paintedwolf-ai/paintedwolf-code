package nativeproxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/systemproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

func exchange(t *testing.T, input string, resolve func(*url.URL) (*url.URL, error)) response {
	t.Helper()
	var output bytes.Buffer
	testutil.FailErr(t, "serve native proxy request", serve(strings.NewReader(input), &output, resolve))
	var result response
	testutil.FailErr(t, "decode private response", json.Unmarshal(output.Bytes(), &result))
	return result
}

func TestPrivateResolutionBindsTargetAndKeepsProxyAuthentication(t *testing.T) {
	const target = "https://downloads.paintedwolf.ai/latest.json"
	for _, value := range []string{"", "http://user:password@proxy.example:3128"} {
		result := exchange(t, `{"format_version":1,"target":"`+target+`"}`, func(requested *url.URL) (*url.URL, error) {
			if requested.String() != target {
				t.Fatalf("target = %q", requested)
			}
			if value == "" {
				return nil, nil
			}
			return url.Parse(value)
		})
		if result.FormatVersion != 1 || result.Target != target || result.ErrorCode != "" {
			t.Fatalf("response = %+v", result)
		}
		if value == "" && result.ProxyURL != nil {
			t.Fatal("intentional direct policy was not null")
		}
		if value != "" && (result.ProxyURL == nil || *result.ProxyURL != value) {
			t.Fatal("private proxy authentication was lost")
		}
	}
}

func TestInvalidRequestsNeverResolveAndErrorsNeverExposeConfiguration(t *testing.T) {
	for _, raw := range []string{
		`{`, `{"format_version":2,"target":"https://example.com"}`,
		`{"format_version":1,"target":"https://example.com","extra":true}`,
		`{"format_version":1,"target":"https://example.com"} {}`,
		strings.Repeat("x", maxRequestBytes+1),
		`{"format_version":1,"target":"file:///etc/passwd"}`,
		`{"format_version":1,"target":"https://user:password@example.com"}`,
		`{"format_version":1,"target":"https://example.com/#fragment"}`,
	} {
		result := exchange(t, raw, func(*url.URL) (*url.URL, error) { t.Fatal("invalid request resolved"); return nil, nil })
		if result.ErrorCode == "" || result.ProxyURL != nil {
			t.Fatalf("invalid response = %+v", result)
		}
	}
	for _, err := range []error{errors.New("private password value"), &systemproxy.Unsupported{Source: systemproxy.SourceKIO, Mechanism: "proxy auto-configuration"}} {
		result := exchange(t, `{"format_version":1,"target":"https://example.com"}`, func(*url.URL) (*url.URL, error) { return nil, err })
		if result.ErrorCode == "" || result.ProxyURL != nil || strings.Contains(result.ErrorCode, "password") {
			t.Fatalf("failure = %+v", result)
		}
	}
}
