package commandprogram

import (
	"strconv"
	"strings"
	"sync"
	"time"
)

// ChildProcess is what the program asks of an external child launched for
// one request: the limits ulimit set earlier in the program, SIGHUP ignored
// for nohup, the relay timeout signals through, and the processor time
// account an enclosing time reads. Prefix commands pass it to their inner
// command unchanged; native owners never launch with it.
type ChildProcess struct {
	Limits       []ResourceLimit
	IgnoreHangup bool
	Signals      *SignalRelay
	Times        *ProcessTimes
}

// Signal is a portable signal: the same number on every supported platform.
type Signal struct {
	Name   string
	Number int
}

// Kill is the signal no process can ignore.
var Kill = Signal{Name: "KILL", Number: 9}

var portableSignals = []Signal{{"HUP", 1}, {"INT", 2}, {"QUIT", 3}, Kill, {"ALRM", 14}, {"TERM", 15}}

// ParseSignal accepts a signal name with or without SIG, in any case, or its
// number.
func ParseSignal(word string) (Signal, bool) {
	name := strings.TrimPrefix(strings.ToUpper(word), "SIG")
	number, numeric := strconv.Atoi(word)
	for _, sig := range portableSignals {
		if (numeric == nil && number == sig.Number) || (numeric != nil && name == sig.Name) {
			return sig, true
		}
	}
	return Signal{}, false
}

// SignalRelay carries signals from a supervising prefix to the external
// process its inner command launched. A native inner command attaches
// nothing, so the prefix ends it through its context instead.
type SignalRelay struct {
	mu      sync.Mutex
	deliver func(Signal) bool
}

// Attach routes delivered signals to a running process until detach.
func (r *SignalRelay) Attach(deliver func(Signal) bool) (detach func()) {
	if r == nil {
		return func() {}
	}
	r.mu.Lock()
	r.deliver = deliver
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		r.deliver = nil
		r.mu.Unlock()
	}
}

// Deliver reports whether an attached process received sig.
func (r *SignalRelay) Deliver(sig Signal) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	deliver := r.deliver
	r.mu.Unlock()
	return deliver != nil && deliver(sig)
}

// ProcessTimes accumulates the processor time external children report when
// they are reaped, and whether a native operation ran whose time the host
// cannot attribute. An enclosing account receives every addition too.
type ProcessTimes struct {
	mu         sync.Mutex
	user, sys  time.Duration
	unmeasured bool
	enclosing  *ProcessTimes
}

// Add records one reaped external child.
func (t *ProcessTimes) Add(user, sys time.Duration) {
	for ; t != nil; t = t.enclosing {
		t.mu.Lock()
		t.user += user
		t.sys += sys
		t.mu.Unlock()
	}
}

// Unmeasured records a native operation whose processor time is not its own.
func (t *ProcessTimes) Unmeasured() {
	for ; t != nil; t = t.enclosing {
		t.mu.Lock()
		t.unmeasured = true
		t.mu.Unlock()
	}
}

func (t *ProcessTimes) totals() (user, sys time.Duration, unmeasured bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.user, t.sys, t.unmeasured
}
