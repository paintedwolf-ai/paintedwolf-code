package hostresources

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

const (
	usesMax         = 8
	useLabelBytes   = 40
	useResourcesMax = 4
	usePortsMax     = 4
)

var useIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// AgentRole is the kind of agent a prompt surface serves.
type AgentRole string

const (
	RoleCoordinator AgentRole = "coordinator"
	RoleWorker      AgentRole = "worker"
)

// Use is one way agents use a resource that needs more than declaring its id.
type Use struct {
	ID       string         `yaml:"id"`
	Label    string         `yaml:"label"`
	Roles    []AgentRole    `yaml:"roles,omitempty"`
	Requires UseRequirement `yaml:"requires"`
}

// UseRequirement is what a use needs beyond the resource itself.
type UseRequirement struct {
	// Capabilities are capability_request fields the use needs.
	Capabilities []string `yaml:"capabilities,omitempty"`
	// Alone marks host_execution that must not be combined with other fields.
	Alone bool `yaml:"alone,omitempty"`
	// Ports are the TCP ports the capabilities apply to.
	Ports []int `yaml:"ports,omitempty"`
	// AnyResource lists catalog resources one of which the use runs through.
	AnyResource []string `yaml:"any_resource,omitempty"`
}

func validateUses(id string, uses []Use) error {
	if len(uses) > usesMax {
		return fmt.Errorf("host resources catalog: %q has more than %d uses", id, usesMax)
	}
	fields := toolcontract.Contract{Capabilities: ^toolcontract.Capability(0)}.CapabilityRequestFields()
	seen := map[string]struct{}{}
	for _, use := range uses {
		if !useIDPattern.MatchString(use.ID) || len(use.ID) > 64 {
			return fmt.Errorf("host resources catalog: %q has invalid use id %q", id, use.ID)
		}
		if _, duplicate := seen[use.ID]; duplicate {
			return fmt.Errorf("host resources catalog: %q repeats use %q", id, use.ID)
		}
		seen[use.ID] = struct{}{}
		if err := validateUseLabel(use.Label); err != nil {
			return fmt.Errorf("host resources catalog: %q use %q: %w", id, use.ID, err)
		}
		for _, role := range use.Roles {
			if role != RoleCoordinator && role != RoleWorker {
				return fmt.Errorf("host resources catalog: %q use %q has invalid role %q", id, use.ID, role)
			}
		}
		if err := validateUseRequirement(use.Requires, fields); err != nil {
			return fmt.Errorf("host resources catalog: %q use %q: %w", id, use.ID, err)
		}
	}
	return nil
}

func validateUseLabel(label string) error {
	if strings.TrimSpace(label) != label || label == "" || len(label) > useLabelBytes || !utf8.ValidString(label) {
		return fmt.Errorf("label must be 1 to %d bytes without surrounding space", useLabelBytes)
	}
	for _, r := range label {
		if r < 0x20 || r == 0x7f || r == '`' || r == '{' || r == '}' {
			return fmt.Errorf("label %q has a control, backtick or brace character", label)
		}
	}
	return nil
}

func validateUseRequirement(req UseRequirement, fields []string) error {
	if len(req.Capabilities) == 0 && len(req.AnyResource) == 0 {
		return fmt.Errorf("requires capabilities or any_resource")
	}
	for _, capability := range req.Capabilities {
		if !slices.Contains(fields, capability) {
			return fmt.Errorf("unknown capability_request field %q", capability)
		}
	}
	if req.Alone && !slices.Equal(req.Capabilities, []string{"host_execution"}) {
		return fmt.Errorf("alone applies only to capabilities [host_execution]")
	}
	if len(req.Ports) > usePortsMax || len(req.AnyResource) > useResourcesMax {
		return fmt.Errorf("at most %d ports and %d alternative resources", usePortsMax, useResourcesMax)
	}
	for _, port := range req.Ports {
		if port < 1 || port > 65535 {
			return fmt.Errorf("invalid port %d", port)
		}
	}
	for _, resource := range req.AnyResource {
		if len(resource) > IdentifierBytesMax || !idPattern.MatchString(resource) {
			return fmt.Errorf("invalid resource id %q", resource)
		}
	}
	return nil
}
