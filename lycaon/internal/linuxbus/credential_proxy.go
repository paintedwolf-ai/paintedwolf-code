package linuxbus

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"github.com/godbus/dbus/v5"
)

const maxCredentialConnections = 16

// CredentialReadPool retains each mediated connection until it has physically ended.
type CredentialReadPool struct {
	policy CredentialReadPolicy
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	active map[*credentialProxy]bool
	wg     sync.WaitGroup
}

func NewCredentialReadPool(policy CredentialReadPolicy) (*CredentialReadPool, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(policy)
	if err != nil || len(raw) > 64<<10 {
		return nil, fmt.Errorf("credential read policy exceeds its frozen bound")
	}
	var frozen CredentialReadPolicy
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &CredentialReadPool{policy: frozen, ctx: ctx, cancel: cancel, active: make(map[*credentialProxy]bool)}, nil
}

// Start takes both connections even when admission fails. Neither is a raw relay.
func (p *CredentialReadPool) Start(client, upstream net.Conn) error {
	p.mu.Lock()
	if p.closed || len(p.active) >= maxCredentialConnections {
		p.mu.Unlock()
		_ = client.Close()
		_ = upstream.Close()
		return fmt.Errorf("credential read connection admission is closed or full")
	}
	ctx, cancel := context.WithCancel(p.ctx)
	proxy := &credentialProxy{client: client, upstream: upstream, policy: p.policy,
		ctx: ctx, cancel: cancel, signals: make(chan *dbus.Signal, 16),
		owners: make(map[string]string), objects: make(map[credentialObject]string),
		matches: make(map[string]map[string]string)}
	p.active[proxy] = true
	p.wg.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wg.Done()
		_ = proxy.serve()
		p.mu.Lock()
		delete(p.active, proxy)
		p.mu.Unlock()
	}()
	return nil
}

func (p *CredentialReadPool) Close() error {
	p.mu.Lock()
	p.closed = true
	p.cancel()
	active := make([]*credentialProxy, 0, len(p.active))
	for proxy := range p.active {
		active = append(active, proxy)
	}
	p.mu.Unlock()
	for _, proxy := range active {
		_ = proxy.client.Close()
		_ = proxy.upstream.Close()
	}
	p.wg.Wait()
	return nil
}

type credentialObject struct {
	service string
	path    dbus.ObjectPath
}

type credentialProxy struct {
	client, upstream net.Conn
	policy           CredentialReadPolicy
	ctx              context.Context
	cancel           context.CancelFunc
	bus              *dbus.Conn
	guid, unique     string
	hello            bool
	signals          chan *dbus.Signal
	// A completed method's reply reaches the client before its signals. This
	// also publishes newly opened session and prompt facts.
	stateMu sync.Mutex
	writeMu sync.Mutex
	serial  uint32
	owners  map[string]string
	objects map[credentialObject]string
	matches map[string]map[string]string
}

func (p *credentialProxy) serve() error {
	defer p.cancel()
	defer func() { _ = p.client.Close(); _ = p.upstream.Close() }()
	stop := context.AfterFunc(p.ctx, func() { _ = p.client.Close(); _ = p.upstream.Close() })
	defer stop()
	replies := &credentialReplies{expected: make(map[uint32]string), cancel: p.cancel}
	bus, guid, err := connectCredentialUpstream(p.ctx, p.upstream, p, replies)
	if err != nil {
		return err
	}
	p.bus, p.guid = bus, guid
	defer func() { _ = bus.Close() }()
	if names := bus.Names(); len(names) > 0 {
		p.unique = names[0]
	}
	if p.unique == "" {
		return fmt.Errorf("credential bus did not establish a private identity")
	}
	reader := bufio.NewReaderSize(p.client, 1024)
	if err := credentialServerAuth(reader, p.client, os.Getuid(), guid); err != nil {
		return err
	}
	signalDone := make(chan struct{})
	go func() { defer close(signalDone); p.forwardSignals() }()
	requests := make(chan *dbus.Message, 2)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer p.cancel()
		for {
			message, err := readCredentialMessage(reader)
			if err != nil || message.Type != dbus.TypeMethodCall {
				return
			}
			select {
			case requests <- message:
			default:
				return
			}
		}
	}()
	defer func() { p.cancel(); _ = p.client.Close(); <-readDone; <-signalDone }()
	for {
		select {
		case <-p.ctx.Done():
			return p.ctx.Err()
		case message := <-requests:
			p.stateMu.Lock()
			err = p.forwardCall(message)
			p.stateMu.Unlock()
			if err != nil {
				return err
			}
		}
	}
}

func (p *credentialProxy) send(message *dbus.Message) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	p.serial++
	if p.serial == 0 {
		return errCredentialWireLimit
	}
	return writeCredentialMessage(p.client, message, p.serial)
}

func (p *credentialProxy) reply(request *dbus.Message, sender string, body []any, failure error) error {
	if request.Flags&dbus.FlagNoReplyExpected != 0 {
		return nil
	}
	headers := map[dbus.HeaderField]dbus.Variant{
		dbus.FieldReplySerial: dbus.MakeVariant(request.Serial()),
		dbus.FieldSender:      dbus.MakeVariant(sender),
		dbus.FieldDestination: dbus.MakeVariant(p.unique),
	}
	typ := dbus.TypeMethodReply
	if failure != nil {
		typ = dbus.TypeError
		name := "org.freedesktop.DBus.Error.AccessDenied"
		body = []any{"The OS credential-store read does not admit this operation."}
		var remote dbus.Error
		if errors.As(failure, &remote) {
			name, body = remote.Name, remote.Body
		}
		headers[dbus.FieldErrorName] = dbus.MakeVariant(name)
	}
	if len(body) > 0 {
		headers[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(body...))
	}
	return p.send(&dbus.Message{Type: typ, Headers: headers, Body: body})
}
