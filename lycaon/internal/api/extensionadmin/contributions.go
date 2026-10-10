package extensionadmin

import (
	"context"
	"errors"
	"net/http"

	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/catalogview"
	"github.com/lycaon/lycaon/internal/commandinvoke"
	"github.com/lycaon/lycaon/internal/contribframe"
	"github.com/lycaon/lycaon/internal/contribution"
	"github.com/lycaon/lycaon/internal/filekind"
	"github.com/lycaon/lycaon/internal/mcp"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// contributionFrameLRUCap bounds retained frames for stale-revision compare.
const contributionFrameLRUCap = 8

// ContributionRuntime carries the dispatch collaborators the app wires in.
type ContributionRuntime struct {
	Receipts  commandinvoke.Receipts
	Authority commandinvoke.Authority
}

func (s *Contributions) CaptureContributionFrame(ctx context.Context, projectID, projectDir string) (*contribframe.Frame, error) {
	// Project scope affects provider readiness, not the device contribution set.
	view := s.Sessions.Catalog.DeviceView(ctx)
	gen := s.MCP.CurrentGeneration(ctx, mcp.CallScope{ProjectID: projectID, ProjectDir: projectDir})
	if frame, ok := s.cachedDeviceContributionFrame(view, gen); ok {
		s.rememberContributionFrame(frame)
		return frame, nil
	}
	frame, err := contribframe.Build(view, gen)
	if err != nil {
		return nil, err
	}
	s.storeDeviceContributionFrame(frame)
	s.rememberContributionFrame(frame)
	return frame, nil
}

func (s *Contributions) cachedDeviceContributionFrame(view *catalogview.View, gen *mcp.ResourceGeneration) (*contribframe.Frame, bool) {
	if view == nil || view.Catalog == nil || gen == nil {
		return nil, false
	}
	s.contribFramesMu.Lock()
	defer s.contribFramesMu.Unlock()
	cached := s.contribDeviceFrame
	if cached == nil || cached.View == nil || cached.View.Catalog == nil || cached.MCP == nil {
		return nil, false
	}
	if cached.View.Catalog.Revision != view.Catalog.Revision || cached.MCP.Revision != gen.Revision {
		return nil, false
	}
	return cached, true
}

func (s *Contributions) storeDeviceContributionFrame(frame *contribframe.Frame) {
	s.contribFramesMu.Lock()
	s.contribDeviceFrame = frame
	s.contribFramesMu.Unlock()
}

func (s *Contributions) invalidateDeviceContributionFrame() {
	s.contribFramesMu.Lock()
	s.contribDeviceFrame = nil
	s.contribFramesMu.Unlock()
}

// WarmContributionFrame compiles the published device catalog for first use.
func (s *Contributions) WarmContributionFrame(ctx context.Context) error {
	view := s.Sessions.Catalog.PublishedDeviceView(ctx)
	if view == nil {
		return nil
	}
	gen := s.MCP.CurrentGeneration(ctx, mcp.CallScope{})
	if frame, ok := s.cachedDeviceContributionFrame(view, gen); ok {
		s.rememberContributionFrame(frame)
		return nil
	}
	frame, err := contribframe.Build(view, gen)
	if err != nil {
		return err
	}
	s.storeDeviceContributionFrame(frame)
	s.rememberContributionFrame(frame)
	return nil
}

// rememberContributionFrame retains revisions for subgraph comparison.
func (s *Contributions) rememberContributionFrame(frame *contribframe.Frame) {
	s.contribFramesMu.Lock()
	defer s.contribFramesMu.Unlock()
	if s.contribFrames == nil {
		s.contribFrames = map[string]*contribframe.Frame{}
	}
	if _, ok := s.contribFrames[frame.Revision]; ok {
		s.touchContributionFrameLocked(frame.Revision)
		return
	}
	s.contribFrames[frame.Revision] = frame
	s.contribFrameOrder = append(s.contribFrameOrder, frame.Revision)
	for len(s.contribFrameOrder) > contributionFrameLRUCap {
		oldest := s.contribFrameOrder[0]
		s.contribFrameOrder = s.contribFrameOrder[1:]
		delete(s.contribFrames, oldest)
	}
}

func (s *Contributions) touchContributionFrameLocked(revision string) {
	for index, current := range s.contribFrameOrder {
		if current != revision {
			continue
		}
		s.contribFrameOrder = append(s.contribFrameOrder[:index], s.contribFrameOrder[index+1:]...)
		s.contribFrameOrder = append(s.contribFrameOrder, revision)
		return
	}
}

func (s *Contributions) contributionFrameByRevision(revision string) (*contribframe.Frame, bool) {
	s.contribFramesMu.Lock()
	defer s.contribFramesMu.Unlock()
	frame, ok := s.contribFrames[revision]
	if ok {
		s.touchContributionFrameLocked(revision)
	}
	return frame, ok
}

// HandleGetContributions serves one complete projection of a captured frame.
func (s *Contributions) HandleGetContributions(w http.ResponseWriter, r *http.Request) {
	frame, err := s.CaptureContributionFrame(r.Context(), "", "")
	if err != nil {
		s.writeContributionFrameError(w, r, err)
		return
	}
	etag := `"` + frame.Revision + `"`
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httpio.WriteJSON(w, http.StatusOK, contribframe.Project(frame))
}

// hostFactState contains inputs available to host fact binders.
type hostFactState struct {
	frame     *contribframe.Frame
	sess      *wire.Session
	invokeCtx wire.CommandInvokeContext
}

// hostFactBinders exposes complete binder coverage for validation.
var hostFactBinders = map[string]func(hostFactState, string) bool{
	// Invocation routes are project-scoped.
	"project_open":   func(hostFactState, string) bool { return true },
	"session_exists": func(s hostFactState, _ string) bool { return s.sess != nil },
	"session_idle": func(s hostFactState, _ string) bool {
		return s.sess != nil && s.sess.Status == wire.SessionStatusIdle
	},
	"activity_live": func(s hostFactState, _ string) bool {
		return s.sess != nil && s.sess.Status == wire.SessionStatusBusy
	},
	"editor_active":   func(s hostFactState, _ string) bool { return s.invokeCtx.Path != "" },
	"editor_editable": func(s hostFactState, _ string) bool { return s.invokeCtx.Path != "" },
	"editor_has_selection": func(s hostFactState, _ string) bool {
		return s.invokeCtx.Path != "" &&
			s.invokeCtx.StartLine > 0 &&
			s.invokeCtx.EndLine >= s.invokeCtx.StartLine
	},
	"editor_has_symbol":  func(s hostFactState, _ string) bool { return s.invokeCtx.Symbol != "" },
	"editor_has_finding": func(s hostFactState, _ string) bool { return s.invokeCtx.FindingID != "" },
	"editor_language": func(s hostFactState, operand string) bool {
		if s.invokeCtx.Path == "" {
			return false
		}
		return filekind.LanguageForPath(s.invokeCtx.Path) == operand
	},
	"mcp_requirement_ready": func(s hostFactState, operand string) bool {
		return s.frame.RequirementReady(operand)
	},
	"configuration_on": func(s hostFactState, operand string) bool {
		return s.frame.ConfigurationOn(operand)
	},
}

// Require complete host fact coverage at startup.
func init() {
	bound := make([]string, 0, len(hostFactBinders))
	for name := range hostFactBinders {
		bound = append(bound, name)
	}
	if err := contribution.RequireHostFactCoverage(bound); err != nil {
		panic("contribution host fact binding: " + err.Error())
	}
}

// hostFactLookup binds facts for one invocation.
func hostFactLookup(frame *contribframe.Frame, sess *wire.Session, invokeCtx wire.CommandInvokeContext) func(fact, operand string) bool {
	state := hostFactState{frame: frame, sess: sess, invokeCtx: invokeCtx}
	return func(fact, operand string) bool {
		binder, ok := hostFactBinders[fact]
		if !ok {
			return false
		}
		return binder(state, operand)
	}
}

// writeContributionFrameError answers a frame capture that had no catalog view
// or MCP generation to read as unavailable, and any other failure as internal.
func (s *Contributions) writeContributionFrameError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, contribframe.ErrSourceUnavailable) {
		s.responses.Fail(w, wire.ApiErrorCodeContributionsUnavailable, "contributions are not available yet")
		return
	}
	s.responses.InternalError(w, r, err)
}
