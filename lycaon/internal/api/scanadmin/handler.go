package scanadmin

import (
	"github.com/lycaon/lycaon/internal/api/httpio"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
)

// Deps are the scan routes' dependencies, fixed at construction.
type Deps struct {
	// DetectionPacksDir holds device detection packs; PublishDetections installs
	// each reloaded matcher.
	DetectionPacksDir string
	PublishDetections func(*detectionpack.Matcher)
	GateRepeatLedger  *approvalstate.GateRepeatLedger
	Registry          scan.CodeScannerRegistry
	ModuleRoot        string
	Projects          project.Registry
	Cadence           *scancadence.Service
	Coordinator       scan.ScanCoordinator
	Sessions          *session.Manager
	Settings          *settings.Service
}

type Handler struct {
	Deps
	detectionPacks *detectionPacksCtl
	responses      *httpio.Responder
}

func New(responses *httpio.Responder, deps Deps) Handler {
	httpio.RequireDependencies("scanadmin",
		httpio.Required{Name: "Cadence", Present: deps.Cadence != nil},
		httpio.Required{Name: "Coordinator", Present: deps.Coordinator != nil},
		httpio.Required{Name: "DetectionPacksDir", Present: deps.DetectionPacksDir != ""},
		httpio.Required{Name: "ModuleRoot", Present: deps.ModuleRoot != ""},
		httpio.Required{Name: "Projects", Present: deps.Projects != nil},
		httpio.Required{Name: "PublishDetections", Present: deps.PublishDetections != nil},
		httpio.Required{Name: "Sessions", Present: deps.Sessions != nil},
	)
	return Handler{Deps: deps, responses: responses, detectionPacks: &detectionPacksCtl{
		configDir: deps.DetectionPacksDir, publish: deps.PublishDetections, sessions: deps.Sessions,
	}}
}
