package httpio

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lycaon/lycaon/internal/pagecursor"
	wire "github.com/lycaon/lycaon/pkg/api"
)

var testPageLimit = MustPageLimit(50, 1, 200)

func TestReadPageQueryRejectsOutOfRangeLimitsWithoutClamping(t *testing.T) {
	cases := []struct {
		query     string
		want      PageQuery
		wantParam string
	}{
		{query: "", want: PageQuery{Limit: 50}},
		{query: "?cursor=abc&limit=200", want: PageQuery{Cursor: "abc", Limit: 200}},
		{query: "?limit=1", want: PageQuery{Limit: 1}},
		{query: "?limit=0", wantParam: "limit"},
		{query: "?limit=201", wantParam: "limit"},
		{query: "?limit=-3", wantParam: "limit"},
		{query: "?limit=ten", wantParam: "limit"},
		{query: "?limit=", wantParam: "limit"},
		{query: "?limit=5&limit=6", wantParam: "limit"},
		{query: "?cursor=", wantParam: "cursor"},
		{query: "?cursor=a&cursor=b", wantParam: "cursor"},
	}
	for _, tc := range cases {
		got, err := ReadPageQuery(httptest.NewRequest(http.MethodGet, "/"+tc.query, nil), testPageLimit)
		if tc.wantParam == "" {
			if err != nil || got != tc.want {
				t.Errorf("%q: got %+v err %v, want %+v", tc.query, got, err, tc.want)
			}
			continue
		}
		var queryErr *QueryParameterError
		if !errors.As(err, &queryErr) || queryErr.Parameter != tc.wantParam {
			t.Errorf("%q: error %v, want invalid %s", tc.query, err, tc.wantParam)
		}
	}
}

func TestReadWindowQueryAcceptsOneDirection(t *testing.T) {
	got, err := ReadWindowQuery(httptest.NewRequest(http.MethodGet, "/?before=b&limit=20", nil), testPageLimit)
	if err != nil || got != (WindowQuery{Before: "b", Limit: 20}) {
		t.Fatalf("before window = %+v err %v", got, err)
	}
	_, err = ReadWindowQuery(httptest.NewRequest(http.MethodGet, "/?before=b&after=a", nil), testPageLimit)
	var queryErr *QueryParameterError
	if !errors.As(err, &queryErr) || queryErr.Parameter != "after" {
		t.Fatalf("both directions error = %v, want invalid after", err)
	}
}

func TestMustPageLimitPanicsOnInconsistentBounds(t *testing.T) {
	for _, bounds := range [][3]int{{0, 0, 10}, {5, 10, 1}, {11, 1, 10}, {0, 1, 10}} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("bounds %v did not panic", bounds)
				}
			}()
			MustPageLimit(bounds[0], bounds[1], bounds[2])
		}()
	}
}

func TestPageCursorErrorMapsDecodeFailures(t *testing.T) {
	cases := []struct {
		err  error
		want wire.ApiErrorCode
	}{
		{err: pagecursor.ErrInvalid, want: wire.ApiErrorCodeInvalidPageCursor},
		{err: fmt.Errorf("wrapped: %w", pagecursor.ErrExpired), want: wire.ApiErrorCodeCursorGenerationExpired},
		{err: errors.New("store closed"), want: wire.ApiErrorCodeInternalError},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		(&Responder{Logger: slog.New(slog.DiscardHandler)}).PageCursorError(w, httptest.NewRequest(http.MethodGet, "/", nil), "cursor", tc.err)
		body := decodeErrorBody(t, w)
		if body.Code != tc.want || w.Code != tc.want.HTTPStatus() {
			t.Errorf("%v: answered %d %s, want %s", tc.err, w.Code, body.Code, tc.want)
		}
		if tc.want != wire.ApiErrorCodeInternalError && body.Details["param"] != "cursor" {
			t.Errorf("%v: details %v do not name the cursor param", tc.err, body.Details)
		}
	}
}
