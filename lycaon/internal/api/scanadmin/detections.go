package scanadmin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/api/requestscope"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/projectcontrib"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// detectionPacksCtl serializes catalog changes and publication.
type detectionPacksCtl struct {
	mu        sync.Mutex
	configDir string
	publish   func(*detectionpack.Matcher)
	// sessions supplies extension-contributed packs, read per operation.
	sessions *session.Manager
}

// ReloadDetectionPacks rebuilds the process matcher from the current catalog.
func (s *Handler) ReloadDetectionPacks(ctx context.Context) {
	ctl := s.detectionPacks
	ctl.mu.Lock()
	defer ctl.mu.Unlock()
	if _, err := ctl.reloadLocked(ctx); err != nil {
		slog.WarnContext(ctx, "detection packs did not reload after an extension change", "error", err)
	}
}

func (s *Handler) HandleListDetectionPacks(w http.ResponseWriter, r *http.Request) {
	projectDir, err := s.detectionProjectDir(r)
	if err != nil {
		requestscope.ScopeError(s.responses, w, r, err)
		return
	}
	ctl := s.detectionPacks
	ctl.mu.Lock()
	defer ctl.mu.Unlock()
	cat, err := detectionpack.LoadCatalog(detectionpack.Input{
		ConfigDir:   ctl.configDir,
		ProjectDir:  projectDir,
		Contributed: ctl.contributedLocked(r.Context()),
	})
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, wire.DetectionPackList{
		Packs:    detectionPacksToDTO(cat, s.GateRepeatLedger),
		Rejected: detectionRejectedToDTO(cat.Rejected),
	})
}

// detectionRejectedToDTO always returns a non-nil map; rejected is required.
func detectionRejectedToDTO(rows []detectionpack.RejectedRow) map[string]wire.DetectionPackRejectedRow {
	out := make(map[string]wire.DetectionPackRejectedRow, len(rows))
	for _, row := range rows {
		out[row.ID] = wire.DetectionPackRejectedRow{
			ID:     row.ID,
			Code:   row.Code,
			Detail: row.Detail,
		}
	}
	return out
}

// detectionProjectDir resolves an enabled project scan-config directory.
func (s *Handler) detectionProjectDir(r *http.Request) (string, error) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		return "", nil
	}
	p, err := s.Projects.Get(r.Context(), projectID)
	if err != nil {
		return "", err
	}
	dir, err := requestscope.ProjectDir(r, s.Projects)
	if err != nil {
		return "", err
	}
	return requestscope.GatedProjectDir(s.Settings, p, dir, projectcontrib.SurfaceScanConfig), nil
}

func (s *Handler) HandleUpdateDetectionPack(w http.ResponseWriter, r *http.Request) {
	ctl := s.detectionPacks
	packID := httpio.EncodedPathID(r, "pack_id")
	var req wire.UpdateDetectionPackRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	if req.Enabled == nil {
		s.responses.Fail(w, wire.ApiErrorCodeInvalidRequest, "enabled is required")
		return
	}
	ctl.mu.Lock()
	defer ctl.mu.Unlock()
	cat, err := detectionpack.LoadCatalog(detectionpack.Input{
		ConfigDir:   ctl.configDir,
		Contributed: ctl.contributedLocked(r.Context()),
	})
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	// Keep disabled IDs for packs not in the current catalog.
	disabled, err := detectionpack.DisabledIDs(ctl.configDir)
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	found := false
	for _, p := range cat.Packs {
		en := p.Enabled
		if p.ID == packID {
			found = true
			en = *req.Enabled
		}
		delete(disabled, p.ID)
		if !en {
			disabled[p.ID] = struct{}{}
		}
	}
	if !found {
		s.responses.Fail(w, wire.ApiErrorCodeDetectionPackNotFound, "detection pack not found")
		return
	}
	ids := make([]string, 0, len(disabled))
	for id := range disabled {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if err := detectionpack.WriteDisabledIDs(ctl.configDir, ids); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	cat, err = ctl.reloadLocked(r.Context())
	if err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	p, ok := cat.PackByID(packID)
	if !ok {
		s.responses.Fail(w, wire.ApiErrorCodeDetectionPackNotFound, "detection pack not found")
		return
	}
	httpio.WriteJSON(w, http.StatusOK, detectionPackToDTO(p, s.GateRepeatLedger))
}

func (s *Handler) HandleImportDetectionPack(w http.ResponseWriter, r *http.Request) {
	ctl := s.detectionPacks
	var req wire.DetectionPackImportRequest
	if err := httpio.DecodeJSON(w, r, &req); err != nil {
		s.responses.DecodeError(w, r, err)
		return
	}
	ctl.mu.Lock()
	defer ctl.mu.Unlock()
	out, err := detectionpack.ImportPack(ctl.configDir, ctl.contributedLocked(r.Context()), detectionpack.ImportRequest{
		SourcePath: req.Path,
		DryRun:     req.DryRun,
		Replace:    req.Replace,
	})
	if err != nil {
		s.writeDetectionPackError(w, r, err)
		return
	}
	if !req.DryRun {
		if _, err := ctl.reloadLocked(r.Context()); err != nil {
			s.responses.InternalError(w, r, err)
			return
		}
	}
	rejected := make([]wire.DetectionPackRejectedRule, 0, len(out.RejectedRules))
	for _, rr := range out.RejectedRules {
		rejected = append(rejected, wire.DetectionPackRejectedRule{File: rr.File, Reason: rr.Reason})
	}
	ignored := out.Ignored
	if ignored == nil {
		ignored = []string{}
	}
	rehearsal := make([]string, 0, len(out.Rehearsal))
	for _, finding := range out.Rehearsal {
		rehearsal = append(rehearsal, finding.Message())
	}
	httpio.WriteJSON(w, http.StatusCreated, wire.DetectionPackImportResult{
		DryRun:        req.DryRun,
		Pack:          detectionPackToDTO(out.Pack, s.GateRepeatLedger),
		RejectedRules: rejected,
		Ignored:       ignored,
		Rehearsal:     rehearsal,
	})
}

func (s *Handler) HandleDeleteDetectionPack(w http.ResponseWriter, r *http.Request) {
	ctl := s.detectionPacks
	packID := httpio.EncodedPathID(r, "pack_id")
	ctl.mu.Lock()
	defer ctl.mu.Unlock()
	if err := detectionpack.RemoveDevicePack(ctl.configDir, ctl.contributedLocked(r.Context()), packID); err != nil {
		s.writeDetectionPackError(w, r, err)
		return
	}
	if _, err := ctl.reloadLocked(r.Context()); err != nil {
		s.responses.InternalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *detectionPacksCtl) contributedLocked(ctx context.Context) []detectionpack.Pack {
	view := c.sessions.Catalog().DeviceView(ctx)
	if view == nil {
		return nil
	}
	return view.DetectionPacks()
}

func (c *detectionPacksCtl) reloadLocked(ctx context.Context) (*detectionpack.Catalog, error) {
	cat, err := detectionpack.LoadCatalog(detectionpack.Input{
		ConfigDir:   c.configDir,
		Contributed: c.contributedLocked(ctx),
	})
	if err != nil {
		return nil, err
	}
	c.publish(detectionpack.NewMatcher(cat))
	return cat, nil
}

func (s *Handler) writeDetectionPackError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, detectionpack.ErrPackNotFound):
		s.responses.Fail(w, wire.ApiErrorCodeDetectionPackNotFound, "detection pack not found")
	case errors.Is(err, detectionpack.ErrPackNotRemovable):
		s.responses.Fail(w, wire.ApiErrorCodeDetectionPackNotRemovable, "this detection pack cannot be removed")
	case errors.Is(err, detectionpack.ErrPackIDCollision):
		s.responses.Fail(w, wire.ApiErrorCodeDetectionPackIdCollision, "a detection pack with this id already exists")
	case errors.Is(err, detectionpack.ErrPackInvalid):
		s.responses.FailReason(w, wire.ApiErrorCodeInvalidRequest, "the detection pack is invalid")
	default:
		s.responses.InternalError(w, r, err)
	}
}

func detectionPacksToDTO(cat *detectionpack.Catalog, ledger *approvalstate.GateRepeatLedger) []wire.DetectionPack {
	if cat == nil {
		return []wire.DetectionPack{}
	}
	out := make([]wire.DetectionPack, 0, len(cat.Packs))
	for _, p := range cat.Packs {
		out = append(out, detectionPackToDTO(p, ledger))
	}
	return out
}

func detectionRuleReasonKey(packID, ruleID string) string {
	return "authority_misuse:" + packID + "/" + ruleID
}

func detectionPackToDTO(p detectionpack.Pack, ledger *approvalstate.GateRepeatLedger) wire.DetectionPack {
	counts := map[string]int{}
	if ledger != nil {
		counts = ledger.CountsSinceLaunch()
	}
	rules := make([]wire.DetectionRuleSummary, 0, len(p.Rules))
	anyRecent := false
	for _, r := range p.Rules {
		recent := counts[detectionRuleReasonKey(p.ID, r.ID)]
		if recent > 0 {
			anyRecent = true
		}
		rules = append(rules, wire.DetectionRuleSummary{
			ID:                r.ID,
			Title:             r.Title,
			Description:       r.Description,
			Level:             string(r.Level),
			Supported:         r.Supported,
			UnsupportedReason: r.UnsupportedReason,
			References:        append([]string(nil), r.References...),
		})
		if recent > 0 {
			rules[len(rules)-1].RecentAsks = recent
		}
	}
	if anyRecent {
		sort.SliceStable(rules, func(i, j int) bool {
			if rules[i].RecentAsks != rules[j].RecentAsks {
				return rules[i].RecentAsks > rules[j].RecentAsks
			}
			return rules[i].Title < rules[j].Title
		})
	}
	loadWarnings := append([]string(nil), p.LoadWarnings...)
	// Runtime rule failures share the catalog warning list.
	loadWarnings = append(loadWarnings, detectionpack.SkippedRuleWarnings(p.ID)...)
	return wire.DetectionPack{
		ID:                 p.ID,
		Label:              p.Label,
		Description:        p.Description,
		Source:             p.Source,
		ProviderPackID:     p.ProviderPackID,
		Removable:          p.Removable(),
		Enabled:            p.Enabled,
		LoadWarnings:       loadWarnings,
		EquivalentBinaries: flattenEquivalents(p.Equivalents),
		Rules:              rules,
	}
}

func flattenEquivalents(eq map[string][]string) []string {
	if len(eq) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	var out []string
	for _, alts := range eq {
		for _, a := range alts {
			if a == "" {
				continue
			}
			if _, ok := seen[a]; ok {
				continue
			}
			seen[a] = struct{}{}
			out = append(out, a)
		}
	}
	sort.Strings(out)
	return out
}
