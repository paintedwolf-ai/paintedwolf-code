package catalogload

import (
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/agentdef"
	"github.com/lycaon/lycaon/internal/egressclass"
	"github.com/lycaon/lycaon/internal/preflight"
	"github.com/lycaon/lycaon/internal/usernotice"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

const noticesDir = "lycaon/config/packs/painted-wolf/platform/host/user-notices"

// preflightProbeCodes are the codes the readiness probes raise.
var preflightProbeCodes = []string{
	preflight.CodeOSBelowFloor, preflight.CodeConfigDirUnwritable, preflight.CodeHostFileLimitReached,
	preflight.CodeDiskSpaceLow, preflight.CodeCommandPathLimited, preflight.CodeGitEngineUnavailable,
	preflight.CodeScannerEngineUnavailable, preflight.CodeBrowserEngineUnavailable, preflight.CodeDecisionEngineUnavailable,
	preflight.CodeOCREngineUnavailable, preflight.CodeNoProviderConfigured, preflight.CodeLiteUnavailable,
}

func notices(r *invariants.Run) *usernotice.Config {
	return invariants.Part(r.Corpus, "user notices", func() (*usernotice.Config, error) {
		return usernotice.LoadNoticeDir(r.Path(noticesDir))
	})
}

// surfaceClosure asserts every raised code has a visible notice on surface
// and every notice claiming surface is raised.
func surfaceClosure(r *invariants.Run, surface string, raised map[string]bool, claimed []string, alsoRaised func(string) bool) {
	cfg := notices(r)
	r.Expect(len(raised), "codes raised on "+surface)
	for code := range raised {
		entry, ok := cfg.UserNotices[code]
		switch {
		case !ok:
			r.Report("%s raises %s, which has no user notice", surface, code)
		case !entry.IsUserVisible():
			r.Report("%s raises %s, whose notice is not user visible", surface, code)
		case !entry.HasSurface(surface):
			r.Report("%s raises %s, whose notice omits surfaces: [%s]", surface, code, surface)
		}
	}
	for _, code := range claimed {
		if !raised[code] && !alsoRaised(code) {
			r.Report("notice %s claims surface %s, which never raises it", code, surface)
		}
	}
}

var noticeClosure = []invariants.Invariant{{
	// Every raised API error code is declared, and every declared code raised.
	Name:    "api-error-codes-close-over-the-ledger",
	Anchors: []invariants.Anchor{invariants.Func("pkg/api", "AllApiErrorCodeValues")},
	Check: func(r *invariants.Run) {
		emitted := errorCodeInventory(r).emitted()
		declared := map[string]bool{}
		for _, code := range wire.AllApiErrorCodeValues() {
			declared[string(code)] = true
		}
		for code := range emitted {
			if !declared[code] {
				r.Report("production raises undeclared code %s; add it to docs/openapi/vocab/ApiErrorCode.yaml", code)
			}
		}
		for code := range declared {
			if !emitted[code] {
				r.Report("the ledger declares %s, which nothing raises", code)
			}
		}
	},
}, {
	Name:    "user-notice-host-error-closure",
	Anchors: []invariants.Anchor{invariants.Named("internal/api", "promptHostErrorCode")},
	Check: func(r *invariants.Run) {
		surfaceClosure(r, "host_error", errorCodeInventory(r).hostErrors, usernotice.HostErrorCodes(notices(r)), func(string) bool { return false })
	},
}, {
	Name:    "user-notice-worker-failure-closure",
	Anchors: []invariants.Anchor{invariants.Func("internal/worker", "ExecuteFailureCode")},
	Check: func(r *invariants.Run) {
		surfaceClosure(r, "worker_failure", errorCodeInventory(r).workerFailures, noticeSurfaceCodes(notices(r), "worker_failure"), func(string) bool { return false })
	},
}, {
	// HTTP notices are raised by handlers or handed to them as typed values;
	// a few are raised outside writeError by design.
	Name:    "user-notice-http-closure",
	Anchors: []invariants.Anchor{invariants.Dir(noticesDir)},
	Exempt: []invariants.Exemption{
		{Key: "outbound_secret_denied", Reason: "tool-reject facing notice; the path is tool_approval and a structured reject"},
		{Key: "GIT_INTERNALS_WRITE_DENIED", Reason: "tool-reject facing notice for the native git-internals write sink"},
		{Key: "GIT_SIGNING_UNSUPPORTED", Reason: "tool and gitexec facing notice when repository policy requires signing"},
		{Key: "GIT_REPO_CONFIG_UNSAFE", Reason: "tool and gitexec facing notice when a repository config names programs git would run"},
	},
	Check: func(r *invariants.Run) {
		inv := errorCodeInventory(r)
		surfaceClosure(r, "http", inv.handlers, noticeSurfaceCodes(notices(r), "http"), func(code string) bool { return inv.typed[code] || r.Exempt(code) })
	},
}, {
	// The ledger and the notice catalog list the same codes, beyond the
	// notices raised outside the API.
	Name:    "user-notice-ledger-is-documented",
	Anchors: []invariants.Anchor{invariants.Func("pkg/api", "AllApiErrorCodeValues")},
	Exempt: append([]invariants.Exemption{
		{Key: "session_aborted", Reason: "documented user_visible:false; a host abort is not surfaced on the notice rail"},
		{Key: "GIT_INTERNALS_WRITE_DENIED", Reason: "tool-reject facing notice for the native git-internals write sink"},
		{Key: "GIT_SIGNING_UNSUPPORTED", Reason: "gitexec and tool facing notice when repository policy requires signing"},
		{Key: "GIT_REPO_CONFIG_UNSAFE", Reason: "gitexec and tool facing notice when a repository config names programs git would run"},
		{Key: "outbound_secret_denied", Reason: "approval-card facing notice for the outbound secret screen"},
	}, preflightNoticeExemptions()...),
	Check: func(r *invariants.Run) {
		cfg := notices(r)
		declared := map[string]bool{}
		for _, code := range wire.AllApiErrorCodeValues() {
			declared[string(code)] = true
			if _, ok := cfg.UserNotices[string(code)]; !ok {
				r.Report("the ledger declares %s, which has no user notice", code)
			}
		}
		for code := range cfg.UserNotices {
			if !declared[code] && !r.Exempt(code) {
				r.Report("user notice %s is not in the ApiErrorCode vocabulary", code)
			}
		}
	},
}, {
	// A probe's code has a visible preflight notice with a title and a
	// suggested action: preflight reports and never remediates.
	Name:    "preflight-probe-codes-have-actionable-notices",
	Anchors: []invariants.Anchor{invariants.Package("internal/preflight")},
	Check: func(r *invariants.Run) {
		cfg := notices(r)
		for _, code := range preflightProbeCodes {
			entry, ok := cfg.UserNotices[code]
			if !ok || !entry.IsUserVisible() || !entry.HasSurface("preflight") {
				r.Report("preflight code %s needs a user-visible notice on surfaces: [preflight]", code)
				continue
			}
			copy := usernotice.RenderCopy(entry, nil)
			if strings.TrimSpace(copy.SuggestedAction) == "" || strings.TrimSpace(copy.Title) == "" {
				r.Report("preflight code %s needs a title and a suggested action", code)
			}
		}
	},
}, {
	// Every notice on the preflight surface is raised by a probe and scoped
	// to the app, the only scope readiness renders.
	Name:    "preflight-notices-are-raised-and-app-scoped",
	Anchors: []invariants.Anchor{invariants.Dir(noticesDir)},
	Check: func(r *invariants.Run) {
		cfg := notices(r)
		for _, code := range noticeSurfaceCodes(cfg, "preflight") {
			if !contains(preflightProbeCodes, code) {
				r.Report("notice %s claims surfaces: [preflight], which no probe raises", code)
			}
			if entry := cfg.UserNotices[code]; entry.Notification != nil {
				for _, res := range entry.Notification.Normalized() {
					if res.Scope != usernotice.ScopeApp {
						r.Report("preflight notice %s resolution %s is scoped %s; readiness renders app scope only", code, res.ID, res.Scope)
					}
				}
			}
		}
	},
}, {
	// An egress failure the user should see has a visible notice.
	Name:    "egress-failure-codes-have-notices",
	Anchors: []invariants.Anchor{invariants.Func("internal/egressclass", "All")},
	Check: func(r *invariants.Run) {
		cfg := notices(r)
		for _, c := range egressclass.All() {
			if c.NoticeCode == "" {
				continue
			}
			if entry, ok := cfg.UserNotices[c.NoticeCode]; !ok || !entry.IsUserVisible() {
				r.Report("egress class %s notice %s needs a user-visible row in host/user-notices", c.ID, c.NoticeCode)
			}
		}
	},
}, {
	// The agent catalog loads with its profiles.
	Name:    "agent-config-loads",
	Anchors: []invariants.Anchor{invariants.Func("internal/agentdef", "LoadEffective")},
	Check: func(r *invariants.Run) {
		profiles, err := agentdef.LoadEffective()
		r.Must(err)
		r.Expect(len(profiles), "agent profiles")
	},
}, {
	// The demo workflows ship in the bundled catalog.
	Name:    "bundled-catalog-ships-the-demo-workflows",
	Anchors: []invariants.Anchor{invariants.Func("internal/workflow/definition", "RegistryFromDirs")},
	Check: func(r *invariants.Run) {
		all := manifests(r)
		for _, key := range []string{"plan@1.0.1", "plan@1.0.0", "security-survey@2.0.1", "recon-pack@1.0.1", "recon-pack@1.0.0", "bugbash@1.1.0", "options@1.0.0", "implement@1.0.0"} {
			if _, ok := all[key]; !ok {
				r.Report("the bundled catalog has no %s", key)
			}
		}
	},
}}

func preflightNoticeExemptions() []invariants.Exemption {
	out := make([]invariants.Exemption, 0, len(preflightProbeCodes))
	for _, code := range preflightProbeCodes {
		out = append(out, invariants.Exemption{Key: code, Reason: "a preflight probe raises it outside writeError"})
	}
	return out
}

// noticeSurfaceCodes lists, sorted, the user-visible notice codes registered for surface.
func noticeSurfaceCodes(cfg *usernotice.Config, surface string) []string {
	var codes []string
	for code, entry := range cfg.UserNotices {
		if entry.IsUserVisible() && entry.HasSurface(surface) {
			codes = append(codes, code)
		}
	}
	sort.Strings(codes)
	return codes
}
