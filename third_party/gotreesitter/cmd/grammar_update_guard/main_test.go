package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestExternalName(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "string", raw: `"_automatic_semicolon"`, want: "_automatic_semicolon"},
		{name: "name object", raw: `{"type":"SYMBOL","name":"safe_nav"}`, want: "safe_nav"},
		{name: "value object", raw: `{"type":"STRING","value":"else"}`, want: "else"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := externalName(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("externalName: %v", err)
			}
			if got != tc.want {
				t.Fatalf("externalName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShouldCheck(t *testing.T) {
	cases := []struct {
		name string
		in   updateResult
		want bool
	}{
		{name: "applied", in: updateResult{RepoURL: "https://example.test/repo", NewRef: "abc", Applied: true}, want: true},
		{name: "available", in: updateResult{RepoURL: "https://example.test/repo", NewRef: "abc", Status: updateStatusAvailable}, want: true},
		{name: "unchanged", in: updateResult{RepoURL: "https://example.test/repo", NewRef: "abc"}, want: false},
		{name: "missing ref", in: updateResult{RepoURL: "https://example.test/repo", Applied: true}, want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldCheck(tc.in); got != tc.want {
				t.Fatalf("shouldCheck = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSafeDirName(t *testing.T) {
	got := []string{
		safeDirName("Swift"),
		safeDirName("c-sharp"),
		safeDirName(""),
	}
	want := []string{"swift", "c_sharp", "grammar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("safeDirName values = %v, want %v", got, want)
	}
}
