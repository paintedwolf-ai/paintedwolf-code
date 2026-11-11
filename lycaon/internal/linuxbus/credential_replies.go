package linuxbus

import (
	"context"
	"sync"

	"github.com/godbus/dbus/v5"
)

// credentialReplies binds each reply to the destination actually called. A
// different bus peer cannot turn a guessed serial into service ownership facts.
type credentialReplies struct {
	mu       sync.Mutex
	expected map[uint32]string
	cancel   context.CancelFunc
}

func (r *credentialReplies) outgoing(message *dbus.Message) {
	if message.Type != dbus.TypeMethodCall || message.Flags&dbus.FlagNoReplyExpected != 0 {
		return
	}
	destination, _ := message.Headers[dbus.FieldDestination].Value().(string)
	r.mu.Lock()
	admitted := destination != "" && len(r.expected) < 128
	if admitted {
		r.expected[message.Serial()] = destination
	}
	r.mu.Unlock()
	if !admitted {
		message.Type = 0
		r.cancel()
	}
}

func (r *credentialReplies) incoming(message *dbus.Message) {
	if message.Type != dbus.TypeMethodReply && message.Type != dbus.TypeError {
		return
	}
	serial, _ := message.Headers[dbus.FieldReplySerial].Value().(uint32)
	sender, _ := message.Headers[dbus.FieldSender].Value().(string)
	r.mu.Lock()
	expected := r.expected[serial]
	delete(r.expected, serial)
	r.mu.Unlock()
	trustedBusError := message.Type == dbus.TypeError && sender == "org.freedesktop.DBus"
	if expected == "" || sender != expected && !trustedBusError {
		// The interceptor runs before godbus dispatches the reply. Invalidating
		// this message prevents it from completing even a concurrently closed call.
		message.Type = 0
		r.cancel()
	}
}
