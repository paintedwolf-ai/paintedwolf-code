package main

import (
	"errors"
	"strings"
	"testing"
)

func TestAuthzRejectsUnexpectedArguments(t *testing.T) {
	for _, args := range [][]string{
		{"verify", "--db", "store.db", "extra"},
		{"events", "--db", "store.db", "--session", "session", "extra"},
		{"export", "--db", "store.db", "--session", "session", "extra"},
	} {
		err := runAuthz(args)
		var coded exitCodeError
		if !errors.As(err, &coded) || coded.code != 2 || !strings.Contains(err.Error(), "unexpected arguments: extra") {
			t.Fatalf("runAuthz(%q) = %v", args, err)
		}
	}
}
