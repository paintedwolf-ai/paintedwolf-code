// Package heldcall holds tool calls that outlive their foreground budget.
//
// A call that has not settled within its budget returns a handle and keeps
// running under a context the turn does not own. The handle joins the same
// background-handle vocabulary as command jobs: wait can subscribe to it, the
// session lists it, and Den reads its running state from the process topic.
// Handles live in memory, as process handles do; a host restart ends them.
package heldcall
