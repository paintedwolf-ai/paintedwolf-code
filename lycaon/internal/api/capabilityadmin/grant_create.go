package capabilityadmin

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/projectview"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/approvals"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/hostscope"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/internal/people/personactions"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/settings"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func (s *Grants) HandleResolveSocketGrant(w http.ResponseWriter, r *http.Request) {
	var req wire.ResolveSocketGrantRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	path := strings.TrimSpace(req.SocketPath)
	if path == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "socket_path is required")
		return
	}
	grant, err := confine.ResolveSocketRequest(path)
	if err != nil {
		s.writeSocketResolveError(w, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.ResolveSocketGrantResponse{
		ApprovedPath:       grant.ApprovedPath,
		ResolvedPath:       grant.ResolvedPath,
		EffectiveAuthority: wire.SocketCapabilityAuthorityOutsideSandboxDaemon,
		AuthorityWarning:   wire.SocketGrantAuthorityWarning,
	})
}

func (s *Grants) HandleCreateApprovalGrant(w http.ResponseWriter, r *http.Request) {
	s.authorityMu.Lock()
	defer s.authorityMu.Unlock()
	var req wire.CreateApprovalGrantRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.Scope != wire.ApprovalGrantScopeProject && req.Scope != wire.ApprovalGrantScopeDevice {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "settings grants require project or device scope")
		return
	}
	switch req.Category {
	case wire.ApprovalGrantCategorySocketPath:
	case wire.ApprovalGrantCategoryHost:
		s.createHostGrant(w, r, req)
		return
	default:
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "only categories socket_path and host may be created from settings")
		return
	}
	path := strings.TrimSpace(req.SocketPath)
	if path == "" || !filepath.IsAbs(path) {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "socket_path must be an absolute existing AF_UNIX socket")
		return
	}
	resolved, err := confine.ResolveSocketRequest(path)
	if err != nil {
		s.writeSocketResolveError(w, err)
		return
	}
	projectID := strings.TrimSpace(req.ProjectID)
	var projectDir string
	if projectID != "" && s.Projects != nil {
		if p, perr := s.Projects.Get(r.Context(), projectID); perr == nil && p != nil {
			projectDir = strings.TrimSpace(project.PrimaryRootPath(p))
		}
	}
	if req.Scope == wire.ApprovalGrantScopeProject && projectID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "project scope requires project_id")
		return
	}
	now := time.Now().UTC()
	expiresAt := req.ExpiresAt
	var expiresWhen string
	if expiresAt == nil {
		if req.Scope == wire.ApprovalGrantScopeProject {
			exp := now.Add(7 * 24 * time.Hour)
			expiresAt = &exp
			expiresWhen = "in 7 days or when revoked"
		} else {
			exp := now.Add(30 * 24 * time.Hour)
			expiresAt = &exp
			expiresWhen = "in 30 days or when revoked"
		}
	} else {
		exp := expiresAt.UTC()
		expiresAt = &exp
		expiresWhen = "at the chosen expiry or when revoked"
	}
	witness := hitl.ApprovalGrantWitness{FSJailed: true, Egress: hitl.ContainedEgressProxy}
	id := hitl.ApprovalGrantID(
		hitl.ApprovalGrantScope(req.Scope),
		string(settings.ApprovalCategorySocketPath),
		resolved.ApprovedPath+"\x00"+resolved.ResolvedPath,
		"",
		projectID,
		witness,
		nil,
	)
	title := "Allow local service on this device"
	if req.Scope == wire.ApprovalGrantScopeProject {
		title = "Allow local service for this project"
	}
	persisted := settings.ApprovalGrant{
		ID:                id,
		Scope:             hitl.ApprovalGrantScope(req.Scope),
		Category:          settings.ApprovalCategorySocketPath,
		Pattern:           resolved.ApprovedPath,
		ProjectID:         projectID,
		ProjectDir:        projectDir,
		Title:             title,
		Coverage:          "connect to `" + resolved.ApprovedPath + "`",
		GrantedAt:         now,
		ExpiresAt:         expiresAt,
		ExpiresWhen:       expiresWhen,
		ReaskWhen:         "the socket target, project, or confinement changes",
		Witness:           witness,
		ApprovedPath:      resolved.ApprovedPath,
		ResolvedPath:      resolved.ResolvedPath,
		Source:            "settings",
		GrantedByPersonID: requestscope.Caller(r).ID,
	}
	if _, err := s.Settings.Approvals.UpsertGlobalGrant(persisted); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	personactions.Note(r.Context(), "grant_id", persisted.ID)
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), "approvals", string(llm.SettingsScopeGlobal), "", "updated")
	mapped := approvals.ApprovalGrants([]hitl.ApprovalGrant{socketGrantToDomain(persisted)})
	if len(mapped) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeInternalError, "approval grant mapping failed")
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, mapped[0])
}

// createHostGrant uses the same host-pattern derivation as approval cards.
func (s *Grants) createHostGrant(w http.ResponseWriter, r *http.Request, req wire.CreateApprovalGrantRequest) {
	site, port := hostscope.SplitTunnelPattern(req.HostPattern)
	site = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(site, "*.")))
	if site == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "host_pattern is required for category host")
		return
	}
	pattern := hostscope.Pattern(site)
	if port != 0 {
		pattern = hostscope.TunnelPattern(site, port)
	}
	if pattern == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "host_pattern is not a host the app would lease")
		return
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if req.Scope == wire.ApprovalGrantScopeProject && projectID == "" {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "project scope requires project_id")
		return
	}
	var projectDir string
	if projectID != "" && s.Projects != nil {
		if p, perr := s.Projects.Get(r.Context(), projectID); perr == nil && p != nil {
			projectDir = strings.TrimSpace(project.PrimaryRootPath(p))
		}
	}
	now := time.Now().UTC()
	lifetime, expiresWhen, title := hitl.ProjectLeaseDuration, hitl.ExpiresIn7DaysOrRevoked, hitl.TitleAllowForThisProject
	if req.Scope == wire.ApprovalGrantScopeDevice {
		lifetime, expiresWhen, title = hitl.DeviceLeaseDuration, hitl.ExpiresIn30DaysOrRevoked, hitl.TitleAllowOnThisDevice
	}
	expiresAt := now.Add(lifetime)
	if req.ExpiresAt != nil {
		expiresAt = req.ExpiresAt.UTC()
		expiresWhen = "at the chosen expiry or when revoked"
	}
	witness := hitl.ApprovalGrantWitness{FSJailed: true, Egress: hitl.ContainedEgressProxy}
	persisted := settings.ApprovalGrant{
		ID:                hitl.ApprovalGrantID(hitl.ApprovalGrantScope(req.Scope), string(settings.ApprovalCategoryHost), pattern, "", projectID, witness, nil),
		Scope:             hitl.ApprovalGrantScope(req.Scope),
		Category:          settings.ApprovalCategoryHost,
		Pattern:           pattern,
		ProjectID:         projectID,
		ProjectDir:        projectDir,
		Title:             title,
		Coverage:          hostscope.Coverage(pattern),
		GrantedAt:         now,
		ExpiresAt:         &expiresAt,
		ExpiresWhen:       expiresWhen,
		ReaskWhen:         hitl.ReaskWhenDifferentSiteOrPort,
		Witness:           witness,
		Source:            "settings",
		GrantedByPersonID: requestscope.Caller(r).ID,
	}
	if _, err := s.Settings.Approvals.UpsertGlobalGrant(persisted); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	personactions.Note(r.Context(), "grant_id", persisted.ID)
	projectview.PublishSettings(s.Events, s.Projects, r.Context(), "approvals", string(llm.SettingsScopeGlobal), "", "updated")
	mapped := approvals.ApprovalGrants([]hitl.ApprovalGrant{persisted.ToDomain()})
	if len(mapped) == 0 {
		s.responses.Fail(w, wire.ApiErrorCodeInternalError, "approval grant mapping failed")
		return
	}
	httpio.WriteJSON(w, http.StatusCreated, mapped[0])
}

func (s *Grants) writeSocketResolveError(w http.ResponseWriter, err error) {
	var se *confine.SocketResolveError
	if errors.As(err, &se) && se != nil {
		s.responses.FailDetails(w, se.Code, map[string]any{"field": "socket_path"}, "the socket path cannot be granted")
		return
	}
	s.responses.FailDetails(w, confine.SocketResolveInvalid, map[string]any{"field": "socket_path"}, "the socket path cannot be granted")
}
