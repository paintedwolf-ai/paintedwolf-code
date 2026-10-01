// Package requestscope resolves what an HTTP request addresses: its caller,
// project, session, and settings scope.
package requestscope

import (
	"context"
	"net/http"

	"github.com/lycaon/lycaon/internal/people"
)

// Caller returns the person bound to an authorized request.
func Caller(r *http.Request) people.Person {
	return ContextCaller(r.Context())
}

// ContextCaller returns the person bound to an authorized request's context.
func ContextCaller(ctx context.Context) people.Person {
	caller, _ := people.Caller(ctx)
	return caller
}
