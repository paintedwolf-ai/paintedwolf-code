package linuxbus

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/godbus/dbus/v5"
)

// credentialTransport intentionally uses godbus's generic transport: neither
// authentication nor the message channel can negotiate descriptor passing.
type credentialTransport struct {
	net.Conn
	binary        atomic.Bool
	authRemaining int
	frame         *bytes.Reader
}

func (t *credentialTransport) Write(raw []byte) (int, error) {
	n, err := t.Conn.Write(raw)
	if err == nil && n == len(raw) && !t.binary.Load() && bytes.Equal(raw, []byte("BEGIN\r\n")) {
		t.binary.Store(true)
	}
	return n, err
}

func (t *credentialTransport) Read(raw []byte) (int, error) {
	if len(raw) == 0 {
		return 0, nil
	}
	if !t.binary.Load() {
		if t.authRemaining <= 0 {
			return 0, errCredentialWireLimit
		}
		n, err := t.Conn.Read(raw[:min(len(raw), t.authRemaining)])
		t.authRemaining -= n
		return n, err
	}
	if t.frame == nil || t.frame.Len() == 0 {
		frame, err := readCredentialFrame(t.Conn)
		if err != nil {
			return 0, err
		}
		message, err := decodeCredentialFrame(frame)
		if err != nil {
			return 0, err
		}
		if message.Type != dbus.TypeMethodReply && message.Type != dbus.TypeError && message.Type != dbus.TypeSignal {
			return 0, fmt.Errorf("credential client cannot receive service method calls")
		}
		t.frame = bytes.NewReader(frame)
	}
	return t.frame.Read(raw)
}

func connectCredentialUpstream(ctx context.Context, socket net.Conn, signals dbus.SignalHandler, replies *credentialReplies) (*dbus.Conn, string, error) {
	setup, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	stop := context.AfterFunc(setup, func() { _ = socket.Close() })
	defer stop()
	deadline, _ := setup.Deadline()
	if err := socket.SetDeadline(deadline); err != nil {
		_ = socket.Close()
		return nil, "", err
	}
	transport := &credentialTransport{Conn: socket, authRemaining: 16 << 10}
	bus, err := dbus.NewConn(transport, dbus.WithContext(ctx), dbus.WithSignalHandler(signals),
		dbus.WithOutgoingInterceptor(replies.outgoing), dbus.WithIncomingInterceptor(replies.incoming))
	if err != nil {
		_ = socket.Close()
		return nil, "", err
	}
	fail := func(err error) (*dbus.Conn, string, error) {
		_ = bus.Close()
		return nil, "", err
	}
	if err := bus.Auth([]dbus.Auth{dbus.AuthExternal(fmt.Sprint(os.Getuid()))}); err != nil {
		return fail(err)
	}
	if err := bus.Hello(); err != nil {
		return fail(err)
	}
	var guid string
	if err := bus.BusObject().CallWithContext(setup, "org.freedesktop.DBus.GetId", 0).Store(&guid); err != nil {
		return fail(err)
	}
	stop()
	if err := setup.Err(); err != nil {
		return fail(err)
	}
	if err := socket.SetDeadline(time.Time{}); err != nil {
		return fail(err)
	}
	return bus, guid, nil
}
