package mcp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// CatalogLayer names which mcp.yaml layer last contributed a connection field.
type CatalogLayer string

const (
	CatalogLayerDistro  CatalogLayer = "distro"
	CatalogLayerUser    CatalogLayer = "user"
	CatalogLayerProject CatalogLayer = "project"
)

// Closed-set RejectedRow reason codes. Exact strings are contract.
const (
	RejectProjectStdioForbidden   = "project_stdio_forbidden"
	RejectProjectHeadersForbidden = "project_headers_forbidden"
	RejectProjectRemoteForbidden  = "project_remote_forbidden"
	RejectProjectEnableForbidden  = "project_enable_forbidden"
	RejectRemoteRequiresHTTPS     = "remote_requires_https"
	RejectInvalidURL              = "invalid_url"
	RejectInvalidEntry            = "invalid_entry"
	RejectDuplicateID             = "duplicate_id"
	RejectOverlayUnknownField     = "overlay_unknown_field"
	RejectUnreadableLayer         = "unreadable_layer"
)

// MCPProviderOverlay is one user/project mcp.yaml row. Pointer fields omitempty on write:
// nil means inherit; non-nil empty slice/map clears.
type MCPProviderOverlay struct {
	ID               string              `yaml:"id"`
	URL              *string             `yaml:"url,omitempty"`
	Command          *string             `yaml:"command,omitempty"`
	Args             *[]string           `yaml:"args,omitempty"`
	Env              *map[string]string  `yaml:"env,omitempty"`
	Headers          *map[string]string  `yaml:"headers,omitempty"`
	Token            *string             `yaml:"token,omitempty"`
	Version          *string             `yaml:"version,omitempty"`
	Enabled          *bool               `yaml:"enabled,omitempty"`
	Recipe           *string             `yaml:"recipe,omitempty"`
	CredentialWire   *string             `yaml:"credential_wire,omitempty"`
	CredentialHeader *string             `yaml:"credential_header,omitempty"`
	ToolLoading      *api.McpToolLoading `yaml:"tool_loading,omitempty"`
}

// MergedMCPProviderEntry is an accepted catalog row after MergeMCPCatalog.
type MergedMCPProviderEntry struct {
	MCPProviderEntry
	ConnectionSource CatalogLayer
}

// RejectedRow is a catalog row refused by trust or shape gates.
type RejectedRow struct {
	ID     string
	Layer  CatalogLayer
	Reason string
	Class  ProviderClass
}

type mergeScratch struct {
	entry   MCPProviderEntry
	source  CatalogLayer
	present bool
}

// MergeMCPCatalog merges distro → user → project. err is reserved for caller
// parse/duplicate failures on distro/user; project failures are RejectedRows.
func MergeMCPCatalog(distro *DistroMCPConfig, user, project *UserMCPConfig) (catalog []MergedMCPProviderEntry, rejected []RejectedRow, err error) {
	byID := map[string]*mergeScratch{}
	order := make([]string, 0)

	if distro != nil {
		seen := map[string]struct{}{}
		for _, s := range distro.Providers {
			id := strings.TrimSpace(s.ID)
			if id == "" {
				rejected = append(rejected, rejectedEntry("", CatalogLayerDistro, RejectInvalidEntry, s))
				continue
			}
			if _, dup := seen[id]; dup {
				return nil, nil, &DuplicateIDError{Layer: CatalogLayerDistro, ID: id}
			}
			seen[id] = struct{}{}
			entry := s
			entry.ID = id
			normalizeEntryURL(&entry)
			if reason := validateMergedEntry(entry, CatalogLayerDistro); reason != "" {
				rejected = append(rejected, rejectedEntry(id, CatalogLayerDistro, reason, entry))
				continue
			}
			byID[id] = &mergeScratch{entry: entry, source: CatalogLayerDistro, present: true}
			order = append(order, id)
		}
	}

	applyOverlayLayer := func(cfg *UserMCPConfig, layer CatalogLayer) {
		if cfg == nil {
			return
		}
		for _, ov := range cfg.Providers {
			id := strings.TrimSpace(ov.ID)
			if id == "" {
				rejected = append(rejected, rejectedOverlay("", layer, RejectInvalidEntry, ov))
				continue
			}
			if ov.ToolLoading != nil && !validToolLoading(*ov.ToolLoading) {
				rejected = append(rejected, rejectedOverlay(id, layer, RejectInvalidEntry, ov))
				continue
			}
			if reason := projectOverlayForbidden(ov, layer); reason != "" {
				rejected = append(rejected, rejectedOverlay(id, layer, reason, ov))
				continue
			}
			if overlayNamesBothTransports(ov) {
				rejected = append(rejected, rejectedOverlay(id, layer, RejectInvalidEntry, ov))
				continue
			}

			prev, existed := byID[id]
			var scratch mergeScratch
			if existed && prev.present {
				scratch = *prev
			} else {
				scratch = mergeScratch{
					entry:  MCPProviderEntry{ID: id},
					source: layer,
				}
			}

			applyOverlay(&scratch, ov, layer)

			// Leave byID untouched so a refused overlay keeps the inherited row.
			// Shape validation first: more specific than projectEnableForbidden.
			if reason := validateMergedEntry(scratch.entry, scratch.source); reason != "" {
				rejected = append(rejected, rejectedEntry(id, layer, reason, scratch.entry))
				continue
			}
			if reason := projectEnableForbidden(scratch.entry, ov, layer); reason != "" {
				rejected = append(rejected, rejectedEntry(id, layer, reason, scratch.entry))
				continue
			}
			if !existed {
				order = append(order, id)
			}
			scratch.present = true
			cp := scratch
			byID[id] = &cp
		}
	}

	applyOverlayLayer(user, CatalogLayerUser)
	applyOverlayLayer(project, CatalogLayerProject)

	catalog = make([]MergedMCPProviderEntry, 0, len(order))
	for _, id := range order {
		s := byID[id]
		if s == nil || !s.present {
			continue
		}
		catalog = append(catalog, MergedMCPProviderEntry{
			MCPProviderEntry: s.entry,
			ConnectionSource: s.source,
		})
	}
	return catalog, rejected, nil
}

func rejectedEntry(id string, layer CatalogLayer, reason string, entry MCPProviderEntry) RejectedRow {
	return RejectedRow{ID: id, Layer: layer, Reason: reason, Class: entry.Class()}
}

func rejectedOverlay(id string, layer CatalogLayer, reason string, ov MCPProviderOverlay) RejectedRow {
	entry := MCPProviderEntry{ID: id}
	if ov.URL != nil {
		entry.URL = strings.TrimSpace(*ov.URL)
	}
	if ov.Command != nil {
		entry.Command = strings.TrimSpace(*ov.Command)
	}
	return rejectedEntry(id, layer, reason, entry)
}

func projectOverlayForbidden(ov MCPProviderOverlay, layer CatalogLayer) string {
	if layer != CatalogLayerProject {
		return ""
	}
	if ov.Command != nil || ov.Args != nil || ov.Env != nil || ov.Version != nil {
		return RejectProjectStdioForbidden
	}
	if ov.Headers != nil || ov.Token != nil {
		return RejectProjectHeadersForbidden
	}
	if ov.URL != nil {
		u := strings.TrimSpace(*ov.URL)
		if u == "" {
			return ""
		}
		_, loopback, reject := ClassifyHTTPURL(u)
		if reject == RejectInvalidURL {
			return RejectInvalidURL
		}
		if reject != "" || !loopback {
			return RejectProjectRemoteForbidden
		}
	}
	return ""
}

// overlayValue reads a pointer overlay field: set reports whether the layer spoke at
// all, and the trimmed value distinguishes naming a value from clearing one.
func overlayValue(p *string) (value string, set bool) {
	if p == nil {
		return "", false
	}
	return strings.TrimSpace(*p), true
}

// overlayNamesBothTransports is true when a row names both a URL and a command.
// Naming one and clearing the other is a transport override; nil means inherit.
func overlayNamesBothTransports(ov MCPProviderOverlay) bool {
	url, urlSet := overlayValue(ov.URL)
	cmd, cmdSet := overlayValue(ov.Command)
	return urlSet && cmdSet && url != "" && cmd != ""
}

// projectEnableForbidden rejects unauthorized project declarations.
// Disabling an existing provider remains allowed.
func projectEnableForbidden(entry MCPProviderEntry, ov MCPProviderOverlay, layer CatalogLayer) string {
	if layer != CatalogLayerProject {
		return ""
	}
	enabling := ov.Enabled != nil && *ov.Enabled
	repointing := ov.URL != nil || ov.Command != nil
	if !enabling && !repointing {
		return ""
	}
	// A disabled repoint cannot be reached until something enables it.
	if repointing && !enabling && !entry.Enabled {
		return ""
	}
	switch {
	case entry.HasStaticHTTPAuth(),
		strings.TrimSpace(entry.Command) != "",
		!isLoopbackURL(entry.URL):
		return RejectProjectEnableForbidden
	}
	return ""
}

func applyOverlay(dst *mergeScratch, ov MCPProviderOverlay, layer CatalogLayer) {
	if ov.Enabled != nil {
		dst.entry.Enabled = *ov.Enabled
	} else if !dst.present {
		dst.entry.Enabled = false
	}
	// Transport is one-of. Resolve which side is set, then write both sides
	// together so a nil unused field cannot inherit the other transport.
	url, urlSet := overlayValue(ov.URL)
	cmd, cmdSet := overlayValue(ov.Command)
	switch {
	case urlSet && url != "":
		dst.entry.URL = url
		normalizeEntryURL(&dst.entry)
		dst.entry.Command = ""
		dst.entry.Args = nil
		dst.source = layer
	case cmdSet && cmd != "":
		dst.entry.Command = cmd
		dst.entry.URL = ""
		dst.source = layer
	case urlSet || cmdSet:
		// Clears only. Whichever side the layer named is dropped; the row is left
		// without a transport unless the same overlay supplies one elsewhere.
		if urlSet {
			dst.entry.URL = ""
		}
		if cmdSet {
			dst.entry.Command = ""
			dst.entry.Args = nil
		}
		dst.source = layer
	}
	if ov.Args != nil {
		dst.entry.Args = append([]string(nil), (*ov.Args)...)
		if dst.entry.Command != "" {
			dst.source = layer
		}
	}
	if ov.Env != nil {
		dst.entry.Env = cloneStringMap(*ov.Env)
	}
	if ov.Headers != nil {
		dst.entry.Headers = cloneStringMap(*ov.Headers)
	}
	if ov.Token != nil {
		dst.entry.Token = strings.TrimSpace(*ov.Token)
	}
	if ov.Version != nil {
		dst.entry.Version = strings.TrimSpace(*ov.Version)
	}
	if ov.Recipe != nil {
		dst.entry.Recipe = strings.TrimSpace(*ov.Recipe)
	}
	if ov.CredentialWire != nil {
		dst.entry.CredentialWire = strings.TrimSpace(*ov.CredentialWire)
	}
	if ov.CredentialHeader != nil {
		dst.entry.CredentialHeader = strings.TrimSpace(*ov.CredentialHeader)
	}
	if ov.ToolLoading != nil {
		dst.entry.ToolLoading = *ov.ToolLoading
	}
}

func validateMergedEntry(entry MCPProviderEntry, source CatalogLayer) string {
	if strings.TrimSpace(entry.ID) == "" {
		return RejectInvalidEntry
	}
	if !validOptionalToolLoading(entry.ToolLoading) {
		return RejectInvalidEntry
	}
	rawURL := strings.TrimSpace(entry.URL)
	cmd := strings.TrimSpace(entry.Command)
	switch {
	case rawURL != "" && cmd != "":
		return RejectInvalidEntry
	case rawURL == "" && cmd == "":
		return RejectInvalidEntry
	}
	if reason := validateCredentialPlacement(entry.CredentialWire, entry.CredentialHeader); reason != "" {
		return reason
	}
	if rawURL != "" {
		if len(entry.Env) > 0 {
			return RejectInvalidEntry
		}
		// Project cannot set Token/Headers; a non-empty value is inherited.
		// Repointing would send those credentials to a project-chosen URL.
		if source == CatalogLayerProject && (len(entry.Headers) > 0 || strings.TrimSpace(entry.Token) != "") {
			return RejectProjectHeadersForbidden
		}
		_, loopback, reject := ClassifyHTTPURL(rawURL)
		if reject != "" {
			if source == CatalogLayerProject && reject == RejectRemoteRequiresHTTPS {
				return RejectProjectRemoteForbidden
			}
			return reject
		}
		if !loopback && source == CatalogLayerProject {
			return RejectProjectRemoteForbidden
		}
	}
	if cmd != "" {
		if len(entry.Headers) > 0 || strings.TrimSpace(entry.Token) != "" {
			return RejectInvalidEntry
		}
		if source == CatalogLayerProject {
			return RejectProjectStdioForbidden
		}
	}
	return ""
}

// normalizeEntryURL writes the canonical form. A rejected URL stays as typed.
func normalizeEntryURL(entry *MCPProviderEntry) {
	raw := strings.TrimSpace(entry.URL)
	if raw == "" {
		return
	}
	if canonical, _, reject := ClassifyHTTPURL(raw); reject == "" {
		entry.URL = canonical
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// DuplicateIDError names a duplicate provider id so callers can branch on the type.
type DuplicateIDError struct {
	Layer CatalogLayer
	ID    string
}

func (e *DuplicateIDError) Error() string {
	return fmt.Sprintf("mcp %s: %s: %s", e.Layer, RejectDuplicateID, e.ID)
}

// IsDuplicateIDError reports whether err is a duplicate-id layer failure.
func IsDuplicateIDError(err error) bool {
	var dup *DuplicateIDError
	return errors.As(err, &dup)
}

func duplicateOverlayIDs(providers []MCPProviderOverlay) error {
	seen := map[string]struct{}{}
	for _, s := range providers {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			return &DuplicateIDError{Layer: CatalogLayerUser, ID: id}
		}
		seen[id] = struct{}{}
	}
	return nil
}

func duplicateDistroIDs(providers []MCPProviderEntry) error {
	seen := map[string]struct{}{}
	for _, s := range providers {
		id := strings.TrimSpace(s.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			return &DuplicateIDError{Layer: CatalogLayerDistro, ID: id}
		}
		seen[id] = struct{}{}
	}
	return nil
}
