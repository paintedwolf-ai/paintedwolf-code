package testutil

import (
	"testing"

	"go.uber.org/goleak"
)

// leakIgnores lists process-wide goroutines allowed after tests finish.
func leakIgnores() []goleak.Option {
	return []goleak.Option{
		// gobreaker's expiration goroutine is package-global and lives for the
		// life of the process.
		goleak.IgnoreTopFunction("github.com/sony/gobreaker.(*TwoStepCircuitBreaker).Counts"),

		// Client-specific transports keep idle connections for 90 seconds.
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
		goleak.IgnoreAnyFunction("net/http.(*http2ClientConn).readLoop"),
	}
}

// VerifyNoLeaks runs teardown before checking package goroutines.
func VerifyNoLeaks(m *testing.M, beforeVerify func(), opts ...goleak.Option) {
	goleak.VerifyTestMain(leakCheckedMain{m: m, beforeVerify: beforeVerify}, append(leakIgnores(), opts...)...)
}

type leakCheckedMain struct {
	m            *testing.M
	beforeVerify func()
}

func (l leakCheckedMain) Run() int {
	code := l.m.Run()
	if l.beforeVerify != nil {
		l.beforeVerify()
	}
	return code
}
