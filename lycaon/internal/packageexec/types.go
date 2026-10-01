// Package packageexec identifies package-manager actions that execute remote code.
package packageexec

import "time"

// Operation is a catalog-authored package execution class.
type Operation string

const (
	OperationRemoteExecute          Operation = "remote_execute"
	OperationDependencyInstall      Operation = "dependency_install"
	OperationSourceBuildInstall     Operation = "source_build_install"
	OperationToolInstall            Operation = "tool_install"
	OperationNativeExtensionInstall Operation = "native_extension_install"
	OperationPluginExecute          Operation = "plugin_execute"
)

// IdentityStatus says how much registry identity the host resolved.
type IdentityStatus string

const (
	IdentityResolved    IdentityStatus = "resolved"
	IdentityNotFound    IdentityStatus = "not_found"
	IdentityUnavailable IdentityStatus = "unavailable"
	IdentityUnsupported IdentityStatus = "unsupported"
)

// Package is one package coordinate reviewed by the human.
type Package struct {
	System              string         `json:"system,omitempty"`
	Name                string         `json:"name"`
	RequestedVersion    string         `json:"requested_version,omitempty"`
	ResolvedVersion     string         `json:"resolved_version,omitempty"`
	PublishedAt         *time.Time     `json:"published_at,omitempty"`
	AgeDays             *int           `json:"age_days,omitempty"`
	Registry            string         `json:"registry,omitempty"`
	SourceRepository    string         `json:"source_repository,omitempty"`
	VerifiedAttestation bool           `json:"verified_attestation,omitempty"`
	Status              IdentityStatus `json:"status"`
	StatusDetail        string         `json:"status_detail,omitempty"`
	ResolutionNonce     string         `json:"resolution_nonce,omitempty"`
}

// Execution is the host-derived identity and reduced boundary for one action.
type Execution struct {
	Manager           string    `json:"manager"`
	Operation         Operation `json:"operation"`
	Packages          []Package `json:"packages"`
	AllowedHosts      []string  `json:"allowed_hosts,omitempty"`
	SensitiveReads    []string  `json:"-"`
	ApprovedReadPaths []string  `json:"approved_read_paths,omitempty"`
}

// ExactIdentity returns the package identity included in approval/grant keys.
func (p Package) ExactIdentity() string {
	version := p.ResolvedVersion
	if version == "" {
		version = p.RequestedVersion
	}
	return p.System + "\x1f" + p.Name + "\x1f" + version + "\x1f" + p.ResolutionNonce
}
