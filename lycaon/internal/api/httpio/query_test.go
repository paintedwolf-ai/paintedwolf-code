package httpio

import (
	"net/http/httptest"
	"testing"
)

func TestOptionalBoolQueryIsStrictAndSingleValued(t *testing.T) {
	tests := []struct {
		query       string
		want        bool
		wantPresent bool
		wantError   bool
	}{
		{query: "", wantPresent: false},
		{query: "?flag=true", want: true, wantPresent: true},
		{query: "?flag=false", wantPresent: true},
		{query: "?flag=1", wantPresent: true, wantError: true},
		{query: "?flag=True", wantPresent: true, wantError: true},
		{query: "?flag=", wantPresent: true, wantError: true},
		{query: "?flag=true&flag=false", wantPresent: true, wantError: true},
	}
	for _, tc := range tests {
		req := httptest.NewRequest("GET", "/"+tc.query, nil)
		got, present, err := OptionalBoolQuery(req, "flag")
		if got != tc.want || present != tc.wantPresent || (err != nil) != tc.wantError {
			t.Fatalf("query=%q got=%v present=%v err=%v", tc.query, got, present, err)
		}
	}
}
