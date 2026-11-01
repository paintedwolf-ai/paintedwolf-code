package linuxbus

import (
	"strings"

	"github.com/godbus/dbus/v5"
)

func (p *credentialProxy) busCall(request *dbus.Message, path dbus.ObjectPath, iface, member string) ([]any, error) {
	if path != "/org/freedesktop/DBus" || iface != "org.freedesktop.DBus" {
		return nil, errCredentialOperation
	}
	signature, _ := request.Headers[dbus.FieldSignature].Value().(dbus.Signature)
	if member == "Hello" && signature.String() == "" && !p.hello {
		for _, service := range p.policy.Services {
			if err := p.bus.AddMatchSignalContext(p.ctx, dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, service.Name)); err != nil {
				return nil, err
			}
		}
		p.hello = true
		return []any{p.unique}, nil
	}
	if !p.hello {
		return nil, errCredentialOperation
	}
	switch member + ":" + signature.String() {
	case "GetId:":
		return []any{p.guid}, nil
	case "AddMatch:s", "RemoveMatch:s":
		raw := request.Body[0].(string)
		match, err := credentialMatch(raw)
		if err != nil {
			return nil, err
		}
		if member == "RemoveMatch" {
			delete(p.matches, raw)
			return nil, nil
		}
		if len(p.matches) >= 64 {
			return nil, errCredentialWireLimit
		}
		// Matches apply only to this virtual bus's admitted signals. They are
		// never installed as arbitrary upstream subscriptions.
		p.matches[raw] = match
		return nil, nil
	case "NameHasOwner:s", "GetNameOwner:s", "StartServiceByName:su":
		name := request.Body[0].(string)
		if _, admitted := p.policy.service(name); !admitted {
			return nil, errCredentialOperation
		}
		if member == "StartServiceByName" && request.Body[1].(uint32) != 0 {
			return nil, errCredentialOperation
		}
		call := p.bus.BusObject().CallWithContext(p.ctx, "org.freedesktop.DBus."+member, 0, request.Body...)
		return call.Body, call.Err
	default:
		return nil, errCredentialOperation
	}
}

// credentialMatch accepts a bounded subset of the D-Bus match grammar used by
// credential clients. Unsupported syntax fails closed; no upstream match uses it.
func credentialMatch(raw string) (map[string]string, error) {
	if raw == "" || len(raw) > 4096 {
		return nil, errCredentialWireLimit
	}
	fields := make(map[string]string)
	for raw != "" {
		key, rest, ok := strings.Cut(strings.TrimSpace(raw), "=")
		key, rest = strings.TrimSpace(key), strings.TrimSpace(rest)
		if !ok || len(rest) < 2 || rest[0] != '\'' || len(fields) >= 16 {
			return nil, errCredentialOperation
		}
		end := strings.IndexByte(rest[1:], '\'')
		if end < 0 {
			return nil, errCredentialOperation
		}
		value := rest[1 : end+1]
		if _, duplicate := fields[key]; duplicate {
			return nil, errCredentialOperation
		}
		switch key {
		case "type":
			if value != "signal" {
				return nil, errCredentialOperation
			}
		case "sender", "interface", "member", "path", "path_namespace", "destination", "arg0", "arg0namespace":
		case "eavesdrop":
			if value != "false" {
				return nil, errCredentialOperation
			}
		default:
			return nil, errCredentialOperation
		}
		fields[key] = value
		raw = strings.TrimSpace(rest[end+2:])
		if raw == "" {
			break
		}
		if raw[0] != ',' || len(raw) == 1 {
			return nil, errCredentialOperation
		}
		raw = raw[1:]
	}
	return fields, nil
}

func (p *credentialProxy) DeliverSignal(_, _ string, signal *dbus.Signal) {
	select {
	case p.signals <- signal:
	default:
		p.cancel()
	}
}

func (p *credentialProxy) forwardSignals() {
	for {
		select {
		case <-p.ctx.Done():
			return
		case signal := <-p.signals:
			p.stateMu.Lock()
			err := p.forwardSignal(signal)
			p.stateMu.Unlock()
			if err != nil {
				p.cancel()
				return
			}
		}
	}
}

func (p *credentialProxy) forwardSignal(signal *dbus.Signal) error {
	separator := strings.LastIndexByte(signal.Name, '.')
	if separator < 0 {
		return nil
	}
	iface, member := signal.Name[:separator], signal.Name[separator+1:]
	if signal.Sender == "org.freedesktop.DBus" && signal.Path == "/org/freedesktop/DBus" && iface == "org.freedesktop.DBus" && member == "NameOwnerChanged" {
		if len(signal.Body) != 3 {
			return errCredentialOperation
		}
		name, ok := signal.Body[0].(string)
		if !ok {
			return errCredentialOperation
		}
		if _, admitted := p.policy.service(name); !admitted {
			return nil
		}
		return p.sendMatchedSignal(signal, "org.freedesktop.DBus", iface, member)
	}
	service, owner, err := p.resolveService(signal.Sender, false)
	if err != nil || !service.includesPath(signal.Path) {
		return nil
	}
	admitted := false
	for _, name := range service.Interfaces[iface].Signals {
		admitted = admitted || name == member
	}
	if !admitted {
		return nil
	}
	if iface == "org.freedesktop.Secret.Prompt" {
		object := credentialObject{owner, signal.Path}
		if p.objects[object] != "prompt" {
			return nil
		}
		if member == "Completed" {
			delete(p.objects, object)
		}
	}
	return p.sendMatchedSignal(signal, service.Name, iface, member)
}

func (p *credentialProxy) sendMatchedSignal(signal *dbus.Signal, service, iface, member string) error {
	matched := false
	for _, match := range p.matches {
		matched = matched || p.matchesSignal(match, signal, service, iface, member)
	}
	if !matched {
		return nil
	}
	headers := map[dbus.HeaderField]dbus.Variant{
		dbus.FieldSender: dbus.MakeVariant(signal.Sender), dbus.FieldDestination: dbus.MakeVariant(p.unique),
		dbus.FieldPath: dbus.MakeVariant(signal.Path), dbus.FieldInterface: dbus.MakeVariant(iface),
		dbus.FieldMember: dbus.MakeVariant(member),
	}
	if len(signal.Body) > 0 {
		headers[dbus.FieldSignature] = dbus.MakeVariant(dbus.SignatureOf(signal.Body...))
	}
	return p.send(&dbus.Message{Type: dbus.TypeSignal, Headers: headers, Body: signal.Body})
}

func (p *credentialProxy) matchesSignal(match map[string]string, signal *dbus.Signal, service, iface, member string) bool {
	for key, expected := range match {
		actual := ""
		switch key {
		case "type", "eavesdrop":
			continue
		case "sender":
			if expected == service {
				continue
			}
			actual = signal.Sender
		case "interface":
			actual = iface
		case "member":
			actual = member
		case "destination":
			actual = p.unique
		case "path":
			actual = string(signal.Path)
		case "path_namespace":
			actual = string(signal.Path)
			if strings.HasPrefix(actual, strings.TrimRight(expected, "/")+"/") {
				continue
			}
		case "arg0", "arg0namespace":
			if len(signal.Body) > 0 {
				actual, _ = signal.Body[0].(string)
			}
			if key == "arg0namespace" && strings.HasPrefix(actual, expected+".") {
				continue
			}
		}
		if actual != expected {
			return false
		}
	}
	return true
}
