package httpio

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONRejectsUnknownAndTrailingValues(t *testing.T) {
	type request struct {
		Known string `json:"known"`
	}

	cases := []struct {
		name      string
		body      string
		wantError bool
	}{
		{name: "single value", body: `{"known":"value"}`},
		{name: "unknown field", body: `{"known":"value","unexpected":true}`, wantError: true},
		{name: "second JSON value", body: `{"known":"value"} {"known":"other"}`, wantError: true},
		{name: "trailing invalid bytes", body: `{"known":"value"}not-json`, wantError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", MediaTypeJSON)
			var dst request
			err := DecodeJSON(httptest.NewRecorder(), req, &dst)
			if (err != nil) != tc.wantError {
				t.Fatalf("decodeJSON() error = %v, want error = %v", err, tc.wantError)
			}
		})
	}
}

func TestDecodeJSONWithRawRejectsUnknownAndTrailingValues(t *testing.T) {
	type nested struct {
		Known string `json:"known"`
	}
	type request struct {
		Known  string `json:"known"`
		Nested nested `json:"nested"`
	}

	cases := []struct {
		name      string
		body      string
		wantError bool
	}{
		{name: "single value", body: `{"known":"value","nested":{"known":"nested"}}`},
		{name: "unknown top-level field", body: `{"known":"value","nested":{"known":"nested"},"unexpected":true}`, wantError: true},
		{name: "unknown nested field", body: `{"known":"value","nested":{"known":"nested","unexpected":true}}`, wantError: true},
		{name: "second JSON value", body: `{"known":"value","nested":{"known":"nested"}} {}`, wantError: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", MediaTypeJSON)
			var dst request
			var raw map[string]any
			err := DecodeJSONWithRaw(httptest.NewRecorder(), req, &dst, &raw)
			if (err != nil) != tc.wantError {
				t.Fatalf("decodeJSONWithRaw() error = %v, want error = %v", err, tc.wantError)
			}
		})
	}
}

func TestDecodeJSONRequiresJSONMediaTypeWhenBodyIsPresent(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"known":"value"}`))
	req.Header.Set("Content-Type", "text/plain")
	var dst struct {
		Known string `json:"known"`
	}
	if err := DecodeJSON(httptest.NewRecorder(), req, &dst); !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("decodeJSON() error = %v, want errUnsupportedMediaType", err)
	}
}

func FuzzDecodeStrictJSON(f *testing.F) {
	for _, body := range []string{
		`{"known":"value"}`,
		`{"known":"value","unexpected":true}`,
		`{"known":"value"} {}`,
		`{`,
	} {
		f.Add(body)
	}

	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > MaxJSONBody+1 {
			t.Skip()
		}
		var dst struct {
			Known string `json:"known"`
		}
		_ = DecodeStrictJSON(strings.NewReader(body), &dst)
	})
}
