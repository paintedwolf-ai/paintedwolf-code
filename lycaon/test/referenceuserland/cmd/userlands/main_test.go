package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

func TestRunRefusesMisuseBeforeReachingDocker(t *testing.T) {
	opened := false
	open := func(context.Context) (*referenceuserland.Runner, error) {
		opened = true
		return nil, errors.New("no daemon")
	}
	for _, tc := range []struct {
		args []string
		want int
	}{
		{nil, exitUsage},
		{[]string{"publish"}, exitUsage},
		{[]string{"build", "toybox"}, exitUsage},
		{[]string{"build", "-arch", "amd64"}, exitUsage},
		{[]string{"record", "-unknown"}, exitUsage},
		{[]string{"inventory"}, exitUsage},
		{[]string{"inventory", "gnu", "bsd"}, exitUsage},
	} {
		var stderr bytes.Buffer
		if got := run(t.Context(), tc.args, strings.NewReader(""), &bytes.Buffer{}, &stderr, open); got != tc.want || stderr.Len() == 0 {
			t.Errorf("run(%q) = %d with %q, want %d and a usage message", tc.args, got, stderr.String(), tc.want)
		}
	}
	if opened {
		t.Fatal("a refused invocation reached the container runtime")
	}
}

func TestRunReportsAnUnreachableDaemonAsUnverified(t *testing.T) {
	var stderr bytes.Buffer
	open := func(context.Context) (*referenceuserland.Runner, error) { return nil, errors.New("no daemon") }
	if got := run(t.Context(), []string{"build", "busybox"}, strings.NewReader(""), &bytes.Buffer{}, &stderr, open); got != exitUnverified ||
		!strings.Contains(stderr.String(), "unverified: no daemon") {
		t.Fatalf("run = %d with %q, want unverified", got, stderr.String())
	}
}

func TestDriverFailsOnAMalformedRequest(t *testing.T) {
	var stderr bytes.Buffer
	if got := run(t.Context(), []string{"driver"}, strings.NewReader("{"), &bytes.Buffer{}, &stderr, nil); got != exitFailed ||
		!strings.Contains(stderr.String(), "decode request") {
		t.Fatalf("driver = %d with %q, want a decode failure", got, stderr.String())
	}
}
