package api

import (
	"net/http"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/platformfloor"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/promptattach"
	"github.com/lycaon/lycaon/internal/promptattach/format"
	"github.com/lycaon/lycaon/internal/usernotice"
	"github.com/lycaon/lycaon/internal/version"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// handlePreflight runs and renders every probe.
func (s *Server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	results, overall := preflight.Default().Run(r.Context(), s.preflightEnv)
	if s.responses.Logger != nil {
		codes := make([]string, 0, len(results))
		for _, res := range results {
			if res.Status != preflight.StatusOK {
				codes = append(codes, res.ID+"="+res.Code)
			}
		}
		if overall != preflight.StatusOK {
			s.responses.Logger.WarnContext(r.Context(), "preflight", "component", "preflight", "overall", string(overall), "not_ok", codes)
		} else {
			s.responses.Logger.InfoContext(r.Context(), "preflight", "component", "preflight", "overall", string(overall), "not_ok", codes)
		}
	}

	report := wire.PreflightReport{
		Overall:                string(overall),
		Probes:                 make([]wire.PreflightProbe, 0, len(results)),
		AttachmentCapabilities: attachmentCapabilitiesWire(s.Prompt.Caps),
	}
	facts := preflightHostFacts(s.preflightEnv)
	now := time.Now()
	for _, res := range results {
		report.Probes = append(report.Probes, s.preflightProbeWire(res, facts, now))
	}

	httpio.WriteJSON(w, http.StatusOK, report)
}

func attachmentCapabilitiesWire(caps promptattach.Caps) wire.AttachmentCapabilities {
	return wire.AttachmentCapabilities{
		AutoAttachPasteBytes:     caps.Composer.AutoAttachPaste.Int(),
		MaxInlineTextBytes:       caps.Composer.MaxInlineText.Int(),
		MaxAttachments:           caps.Counts.MaxAttachments,
		MaxReferences:            caps.Counts.MaxReferences,
		MaxImages:                caps.Counts.MaxImages,
		MaxUploadBytes:           caps.Transport.MaxUpload.Int64(),
		MaxImageBytes:            caps.Transport.MaxImage.Int64(),
		MaxBodyBytes:             caps.Materialization.MaxBody.Int64(),
		MaxTurnBytes:             caps.Materialization.MaxTurn.Int64(),
		MaxBodyPreviewBytes:      caps.Prompt.MaxBodyPreview.Int(),
		MaxLargeTextPreviewBytes: caps.Prompt.MaxLargeTextPreview.Int(),
		MaxTurnPreviewBytes:      caps.Prompt.MaxTurnPreview.Int(),
		MaxDocumentBytes:         caps.Document.MaxBody.Int64(),
		MaxVideoBytes:            caps.Video.MaxBody.Int64(),
		ImageMIMETypes:           format.RasterMIMEs(),
		VideoMIMETypes:           format.VideoMIMEs(),
		TextMIMETypes:            format.TextFamilyMIMEs(),
		TextExtensions:           format.TextFamilyExtensions(),
		TextBasenames:            format.TextFamilyBasenames(),
	}
}

// preflightHostFacts builds support-report facts.
func preflightHostFacts(env preflight.Env) preflight.HostFacts {
	facts := preflight.HostFacts{
		AppVersion:    version.Version,
		Build:         buildRevision(),
		SchemaVersion: db.SchemaVersion,
		OSName:        runtime.GOOS,
		OSFloor:       platformfloor.MacOSMin(),
		Arch:          runtime.GOARCH,
	}
	if env.OSProductVer != nil {
		if got, err := env.OSProductVer(); err == nil {
			facts.OSVersion = got
		}
	}
	return facts
}

// buildRevision reads the stamped VCS revision.
func buildRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
}

// preflightProbeWire renders one probe result.
func (s *Server) preflightProbeWire(res preflight.Result, facts preflight.HostFacts, now time.Time) wire.PreflightProbe {
	probe := wire.PreflightProbe{
		ID:     res.ID,
		Status: string(res.Status),
		Code:   res.Code,
		Detail: res.Detail,
	}
	if res.Code == "" || s == nil || s.responses.Notices == nil {
		return probe
	}

	ctx := preflightNoticeContext(res.Detail)
	copy := s.responses.Notices.RenderWire(res.Code, ctx)
	probe.Title = copy.Title
	probe.Message = copy.Message
	probe.SuggestedAction = copy.SuggestedAction
	for _, a := range copy.Actions {
		probe.Actions = append(probe.Actions, wire.NoticeAction(a))
	}

	placement, ok := s.responses.Notices.Placement(res.Code, ctx)
	if !ok {
		return probe
	}
	probe.Tier = wire.NoticeTier(placement.Tier)
	probe.Scope = wire.NoticeScope(placement.Scope)
	probe.Resolution = placement.ID
	if placement.Tier == usernotice.TierCatastrophic {
		probe.CatastrophicDetail = preflight.BuildCatastrophicDetail(withResolution(res, placement.ID), facts, now)
	}
	return probe
}

// withResolution adds the catalog resolution to probe detail.
func withResolution(res preflight.Result, resolution string) preflight.Result {
	detail := make(map[string]string, len(res.Detail)+1)
	for k, v := range res.Detail {
		detail[k] = v
	}
	detail["resolution"] = resolution
	res.Detail = detail
	return res
}

// preflightNoticeContext feeds a probe's structured detail to the copy template.
func preflightNoticeContext(detail map[string]string) map[string]any {
	if len(detail) == 0 {
		return nil
	}
	ctx := make(map[string]any, len(detail))
	for k, v := range detail {
		ctx[k] = v
	}
	return ctx
}
