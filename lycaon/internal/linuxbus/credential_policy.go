package linuxbus

import (
	"fmt"
	"slices"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/lycaon/lycaon/config"
)

// CredentialReadPolicy is the frozen service protocol admitted by an OS-store read.
// It is not a raw session-bus grant.
type CredentialReadPolicy struct {
	Version  int                 `json:"version" yaml:"version"`
	Services []CredentialService `json:"services" yaml:"services"`
}

type CredentialService struct {
	Name       string                         `json:"name" yaml:"name"`
	Paths      []string                       `json:"paths" yaml:"paths"`
	Interfaces map[string]CredentialInterface `json:"interfaces" yaml:"interfaces"`
}

type CredentialInterface struct {
	Methods []CredentialMethod `json:"methods,omitempty" yaml:"methods,omitempty"`
	Signals []string           `json:"signals,omitempty" yaml:"signals,omitempty"`
}

// Role describes the connection-local resource facts a protocol operation needs.
// Method identity and accepted signatures are catalog data, never name heuristics.
type CredentialMethod struct {
	Name       string   `json:"name" yaml:"name"`
	Signatures []string `json:"signatures" yaml:"signatures"`
	Role       string   `json:"role" yaml:"role"`
}

func LoadCredentialReadPolicy() (CredentialReadPolicy, error) {
	raw, err := config.Read(config.CredentialStoreRead)
	if err != nil {
		return CredentialReadPolicy{}, err
	}
	var policy CredentialReadPolicy
	if err := config.DecodeYAML(raw, &policy); err != nil {
		return CredentialReadPolicy{}, err
	}
	return policy, policy.Validate()
}

func (p CredentialReadPolicy) Validate() error {
	if p.Version != 1 || len(p.Services) == 0 || len(p.Services) > 8 {
		return fmt.Errorf("invalid credential read policy version or service count")
	}
	names := map[string]bool{}
	for _, service := range p.Services {
		if service.Name == "" || strings.HasPrefix(service.Name, ":") || names[service.Name] || len(service.Paths) == 0 || len(service.Paths) > 8 || len(service.Interfaces) == 0 || len(service.Interfaces) > 16 {
			return fmt.Errorf("invalid credential service declaration")
		}
		names[service.Name] = true
		for _, path := range service.Paths {
			if path == "/" || !dbus.ObjectPath(path).IsValid() {
				return fmt.Errorf("credential service requires a bounded object namespace")
			}
		}
		for name, iface := range service.Interfaces {
			if name == "" || len(iface.Methods) > 64 || len(iface.Signals) > 32 {
				return fmt.Errorf("invalid credential interface declaration")
			}
			seen := map[string]bool{}
			for _, method := range iface.Methods {
				if method.Name == "" || seen[method.Name] || len(method.Signatures) == 0 || len(method.Signatures) > 4 {
					return fmt.Errorf("invalid credential method declaration")
				}
				seen[method.Name] = true
				switch method.Role {
				case "read", "property_read", "open_session", "session_read", "session_close", "unlock", "prompt":
				default:
					return fmt.Errorf("unknown credential protocol resource role %q", method.Role)
				}
				for _, signature := range method.Signatures {
					if strings.Contains(signature, "h") {
						return fmt.Errorf("credential IPC cannot carry file descriptors")
					}
					if _, err := dbus.ParseSignature(signature); err != nil {
						return fmt.Errorf("invalid credential method signature: %w", err)
					}
				}
			}
		}
	}
	return nil
}

func (p CredentialReadPolicy) service(name string) (CredentialService, bool) {
	for _, service := range p.Services {
		if service.Name == name {
			return service, true
		}
	}
	return CredentialService{}, false
}

func (s CredentialService) includesPath(path dbus.ObjectPath) bool {
	if !path.IsValid() {
		return false
	}
	for _, root := range s.Paths {
		if string(path) == root || strings.HasPrefix(string(path), root+"/") {
			return true
		}
	}
	return false
}

func (s CredentialService) method(path dbus.ObjectPath, iface, name, signature string) (CredentialMethod, bool) {
	if !s.includesPath(path) {
		return CredentialMethod{}, false
	}
	for _, method := range s.Interfaces[iface].Methods {
		if method.Name == name && slices.Contains(method.Signatures, signature) {
			return method, true
		}
	}
	return CredentialMethod{}, false
}
