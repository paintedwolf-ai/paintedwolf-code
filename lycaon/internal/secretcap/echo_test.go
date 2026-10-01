package secretcap

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestReferenceEchoesRewritesEveryEncodedEcho(t *testing.T) {
	t.Parallel()
	const id = "9451ac87-2ef0-4647-b55d-92fda6921ec9"
	const value = "echo-S3cr3t/+=&?#value ~"
	r := &Resolution{values: map[string]resolvedValue{id: {id: id, value: value}}}
	quoted, err := json.Marshal(value)
	testutil.FailErr(t, "json.Marshal failed", err)
	escaped := strings.Trim(string(quoted), `"`)
	nested, err := json.Marshal("token=" + escaped)
	testutil.FailErr(t, "json.Marshal failed", err)
	for name, echo := range map[string]string{
		"plain":            value,
		"query":            url.QueryEscape(value),
		"path":             url.PathEscape(value),
		"json":             escaped,
		"json in json":     string(nested),
		"base64":           "b64 " + base64.StdEncoding.EncodeToString([]byte(value)),
		"base64 raw url":   base64.RawURLEncoding.EncodeToString([]byte(value)),
		"basic credential": "Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+value)),
		"escaped newline":  `line\n` + base64.StdEncoding.EncodeToString([]byte(value)),
	} {
		got := r.ReferenceEchoes(echo)
		if strings.Contains(got, value) || strings.Contains(got, escaped) ||
			strings.Contains(got, base64.StdEncoding.EncodeToString([]byte(value))) ||
			strings.Contains(got, base64.RawURLEncoding.EncodeToString([]byte(value))) ||
			strings.Contains(got, url.QueryEscape(value)) || strings.Contains(got, url.PathEscape(value)) ||
			strings.Contains(got, base64.StdEncoding.EncodeToString([]byte("user:"+value))) {
			t.Errorf("%s: echo survived: %q", name, got)
		}
		if name != "basic credential" && !strings.Contains(got, secretmatch.ReferenceToken(id)) {
			t.Errorf("%s: echo was not written as its reference: %q", name, got)
		}
	}
	const unrelated = "build ok: Y2FjaGUtaGl0cw== plain text"
	if got := r.ReferenceEchoes(unrelated); got != unrelated {
		t.Errorf("unrelated output changed: %q", got)
	}
	if got := (&Resolution{}).ReferenceEchoes(value); got != value {
		t.Errorf("an invocation that resolved nothing rewrote output: %q", got)
	}
}

func TestReferenceEchoesInRewritesStructuredStrings(t *testing.T) {
	t.Parallel()
	const id = "9451ac87-2ef0-4647-b55d-92fda6921ec9"
	const value = "echo-S3cr3t/+=&?#value ~"
	r := &Resolution{values: map[string]resolvedValue{id: {id: id, value: value}}}
	got := r.ReferenceEchoesIn(map[string]any{
		"detail": "server said " + value,
		"nested": []any{map[string]any{"b64": base64.StdEncoding.EncodeToString([]byte(value))}},
		"lines":  []string{url.QueryEscape(value)},
		"count":  3,
	})
	encoded, err := json.Marshal(got)
	testutil.FailErr(t, "json.Marshal failed", err)
	for _, leaked := range []string{value, base64.StdEncoding.EncodeToString([]byte(value)), url.QueryEscape(value)} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("structured echo survived: %s", encoded)
		}
	}
	if strings.Count(string(encoded), secretmatch.ReferenceToken(id)) != 3 {
		t.Errorf("structured echoes were not written as references: %s", encoded)
	}
}
