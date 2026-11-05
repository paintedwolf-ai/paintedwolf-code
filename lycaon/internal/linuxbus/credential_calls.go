package linuxbus

import (
	"errors"
	"fmt"
	"strings"

	"github.com/godbus/dbus/v5"
)

var errCredentialOperation = errors.New("operation is outside the OS credential-store read grant")

func credentialHeader(message *dbus.Message, field dbus.HeaderField) string {
	value, _ := message.Headers[field].Value().(string)
	return value
}

func (p *credentialProxy) forwardCall(request *dbus.Message) error {
	destination := credentialHeader(request, dbus.FieldDestination)
	path, _ := request.Headers[dbus.FieldPath].Value().(dbus.ObjectPath)
	iface, member := credentialHeader(request, dbus.FieldInterface), credentialHeader(request, dbus.FieldMember)
	if destination == "org.freedesktop.DBus" {
		body, err := p.busCall(request, path, iface, member)
		return p.reply(request, destination, body, err)
	}
	if !p.hello {
		return p.reply(request, "org.freedesktop.DBus", nil, errCredentialOperation)
	}
	service, owner, err := p.resolveService(destination, request.Flags&dbus.FlagNoAutoStart == 0)
	if err != nil {
		return p.reply(request, "org.freedesktop.DBus", nil, err)
	}
	signature, _ := request.Headers[dbus.FieldSignature].Value().(dbus.Signature)
	method, admitted := service.method(path, iface, member, signature.String())
	if !admitted || !p.resourceAdmitted(owner, path, method.Role, request.Body, service) {
		return p.reply(request, owner, nil, errCredentialOperation)
	}
	flags := request.Flags & dbus.FlagNoAutoStart
	call := p.bus.Object(owner, path).CallWithContext(p.ctx, iface+"."+member, flags, request.Body...)
	if call.Err == nil {
		if method.Role == "session_read" {
			call.Body, call.Err = credentialSecretReply(member, call.Body)
		}
	}
	if call.Err == nil {
		if err := p.recordResources(owner, path, method.Role, call.Body, service); err != nil {
			return err
		}
	}
	return p.reply(request, owner, call.Body, call.Err)
}

// Every operation uses a current, bus-attested unique owner. Replacing a name
// cannot inherit another daemon's sessions or prompts.
func (p *credentialProxy) resolveService(destination string, activate bool) (CredentialService, string, error) {
	for _, service := range p.policy.Services {
		if destination != service.Name && !strings.HasPrefix(destination, ":") {
			continue
		}
		var owner string
		err := p.bus.BusObject().CallWithContext(p.ctx, "org.freedesktop.DBus.GetNameOwner", 0, service.Name).Store(&owner)
		if err != nil && activate && destination == service.Name {
			var started uint32
			if err = p.bus.BusObject().CallWithContext(p.ctx, "org.freedesktop.DBus.StartServiceByName", 0, service.Name, uint32(0)).Store(&started); err == nil {
				err = p.bus.BusObject().CallWithContext(p.ctx, "org.freedesktop.DBus.GetNameOwner", 0, service.Name).Store(&owner)
			}
		}
		if err != nil || !strings.HasPrefix(owner, ":") || destination != service.Name && destination != owner {
			continue
		}
		if previous := p.owners[service.Name]; previous != owner {
			if previous != "" {
				for _, root := range service.Paths {
					if err := p.bus.RemoveMatchSignalContext(p.ctx, dbus.WithMatchSender(previous), dbus.WithMatchPathNamespace(dbus.ObjectPath(root))); err != nil {
						return CredentialService{}, "", err
					}
				}
			}
			for object := range p.objects {
				if object.service == previous {
					delete(p.objects, object)
				}
			}
			for _, root := range service.Paths {
				if err := p.bus.AddMatchSignalContext(p.ctx, dbus.WithMatchSender(owner), dbus.WithMatchPathNamespace(dbus.ObjectPath(root))); err != nil {
					return CredentialService{}, "", err
				}
			}
			p.owners[service.Name] = owner
		}
		return service, owner, nil
	}
	return CredentialService{}, "", errCredentialOperation
}

func (p *credentialProxy) resourceAdmitted(owner string, path dbus.ObjectPath, role string, body []any, service CredentialService) bool {
	switch role {
	case "read", "open_session":
		return true
	case "property_read":
		iface, ok := body[0].(string)
		_, admitted := service.Interfaces[iface]
		return ok && admitted
	case "session_read":
		session, ok := body[len(body)-1].(dbus.ObjectPath)
		return ok && p.objects[credentialObject{owner, session}] == "session"
	case "session_close":
		return p.objects[credentialObject{owner, path}] == "session"
	case "prompt":
		return p.objects[credentialObject{owner, path}] == "prompt"
	case "unlock":
		paths, ok := body[0].([]dbus.ObjectPath)
		if !ok || len(paths) > 128 {
			return false
		}
		for _, item := range paths {
			if !service.includesPath(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// godbus decodes wire structs into []any. Restore the protocol's concrete
// secret shape before encoding it again, including an empty GetSecrets map.
type credentialSecret struct {
	Session     dbus.ObjectPath
	Parameters  []byte
	Value       []byte
	ContentType string
}

func credentialSecretReply(member string, body []any) ([]any, error) {
	switch member {
	case "GetSecret":
		var secret credentialSecret
		if err := dbus.Store(body, &secret); err != nil {
			return nil, err
		}
		return []any{secret}, nil
	case "GetSecrets":
		var secrets map[dbus.ObjectPath]credentialSecret
		if err := dbus.Store(body, &secrets); err != nil {
			return nil, err
		}
		return []any{secrets}, nil
	default:
		return nil, errCredentialOperation
	}
}

func (p *credentialProxy) recordResources(owner string, path dbus.ObjectPath, role string, body []any, service CredentialService) error {
	var object dbus.ObjectPath
	kind := ""
	switch role {
	case "open_session":
		if len(body) != 2 {
			return fmt.Errorf("invalid credential session reply")
		}
		object, _ = body[1].(dbus.ObjectPath)
		kind = "session"
	case "unlock":
		if len(body) != 2 {
			return fmt.Errorf("invalid credential unlock reply")
		}
		object, _ = body[1].(dbus.ObjectPath)
		if object == "/" {
			return nil
		}
		kind = "prompt"
	case "session_close":
		delete(p.objects, credentialObject{owner, path})
	}
	if kind != "" {
		if !service.includesPath(object) || len(p.objects) >= 128 {
			return fmt.Errorf("credential session or prompt exceeds its object bound")
		}
		p.objects[credentialObject{owner, object}] = kind
	}
	return nil
}
