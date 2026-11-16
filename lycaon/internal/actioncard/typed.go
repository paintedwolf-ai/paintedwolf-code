package actioncard

import (
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

// typedFacts are the facts any tool's result can carry in its typed fields.
// A family's own facts come first, so they win where both name a kind.
func typedFacts(result api.ToolResult) []api.ActionFact {
	var facts []api.ActionFact
	// A terminal verdict satisfied the review gate; any other sends the work round again.
	if verdict := result.Verdict; verdict != nil {
		facts = append(facts, resultFact(map[bool]string{true: "pass", false: "fail"}[verdict.Terminal]))
	}
	if fact, ok := changedFromPreviews(append(slices.Clone(result.FileEditPreviews), result.PromotionPreviews...), result.FileEditsOmitted); ok {
		facts = append(facts, fact)
	}
	if visual := result.Visual; visual != nil {
		facts = append(facts, api.ActionFact{Kind: api.ActionFactCaptured, Caption: visual.Caption, ArtifactID: visual.ID})
	}
	if fact, ok := networkFact(result.ExternalAccess); ok {
		facts = append(facts, fact)
	}
	if fact, ok := processFromResult(result); ok {
		facts = append(facts, fact)
	}
	if dispatch := result.Dispatch; dispatch != nil {
		facts = append(facts, api.ActionFact{Kind: api.ActionFactWorker, Role: dispatch.AgentType})
	}
	if decision := result.CheckpointDecision; decision != nil && decision.Status == api.CheckpointStatusApproved {
		facts = append(facts, api.ActionFact{Kind: api.ActionFactStoppedBy, By: "approval", DecidedBy: "person"})
	}
	return facts
}

// statusFacts state how a call ended when its family has not: a refusal and
// its code, or completion. A running process already states a success.
func statusFacts(result api.ToolResult, facts []api.ActionFact) []api.ActionFact {
	if _, ok := factOf(facts, api.ActionFactResult); ok {
		return nil
	}
	status := resultStatus(result)
	if process, ok := factOf(facts, api.ActionFactProcess); ok && process.State == "running" && status != statusError {
		return nil
	}
	var out []api.ActionFact
	switch status {
	case statusError:
		out = append(out, resultFact(rejectedOutcome(result)))
		if code := stopCode(result); code != "" {
			out = append(out, stoppedByFact("host_stop", code))
		}
	case statusDone:
		out = append(out, resultFact("completed"))
	}
	return out
}

// stopCode is the host's code for a refused or failed call.
func stopCode(result api.ToolResult) string {
	if code := firstCode(result); code != "" {
		return code
	}
	if invocation := result.Invocation; invocation != nil && invocation.Failure != nil {
		return invocation.Failure.Code
	}
	return ""
}

// defaultRow is the row a family leaves unset: how the call ended, then what
// it changed, found or captured.
func defaultRow(facts []api.ActionFact) []api.ActionFactKind {
	row := []api.ActionFactKind{}
	for _, group := range [][]api.ActionFactKind{
		{api.ActionFactResult, api.ActionFactProcess},
		{api.ActionFactChanged, api.ActionFactFound, api.ActionFactSteps, api.ActionFactCaptured},
	} {
		for _, kind := range group {
			if _, ok := factOf(facts, kind); ok {
				row = append(row, kind)
				break
			}
		}
	}
	return row
}

func changedFromPreviews(previews []api.FileEditPreview, omitted int) (api.ActionFact, bool) {
	if len(previews) == 0 && omitted == 0 {
		return api.ActionFact{}, false
	}
	var files, added, removed, created int
	for _, preview := range previews {
		if preview.Created {
			created++
		} else {
			files++
		}
		added += preview.Added
		removed += preview.Removed
	}
	return changedFact(files+omitted, added, removed, 0, created), true
}

// fileOpener opens the one file a call changed.
func fileOpener(previews []api.FileEditPreview) *api.ActionOpener {
	if len(previews) != 1 || previews[0].Deleted || strings.TrimSpace(previews[0].Path) == "" {
		return nil
	}
	return &api.ActionOpener{Kind: "file", Path: previews[0].Path}
}

const maxHosts = 3

func networkFact(access *api.ExternalAccess) (api.ActionFact, bool) {
	if access == nil {
		return api.ActionFact{}, false
	}
	var hosts []string
	for _, endpoint := range access.Endpoints {
		host := strings.TrimSpace(endpoint.Host)
		if host != "" && !slices.Contains(hosts, host) && len(hosts) < maxHosts {
			hosts = append(hosts, host)
		}
	}
	egress := ""
	for _, mode := range access.Modes {
		switch mode {
		case api.ExternalAccessModeDirectIP, api.ExternalAccessModeFullBypass:
			egress = "direct"
		case api.ExternalAccessModeLocalSocket:
			if egress == "" {
				egress = "local_only"
			}
		case api.ExternalAccessModeMediatedHTTP, api.ExternalAccessModeMediatedSocks:
			if egress == "" {
				egress = "proxy_only"
			}
		}
	}
	if len(hosts) == 0 && egress == "" {
		return api.ActionFact{}, false
	}
	return api.ActionFact{Kind: api.ActionFactNetwork, Hosts: hosts, Egress: egress}, true
}

// processFromResult is a background process the call started or found,
// with the port it listens on when the host attributed one.
func processFromResult(result api.ToolResult) (api.ActionFact, bool) {
	process, listener := result.Process, result.Listener
	if process == nil && listener == nil {
		return api.ActionFact{}, false
	}
	fact := processFact("finished")
	if process == nil || process.Running {
		fact.State = "running"
	}
	if listener != nil {
		if _, port, err := net.SplitHostPort(listener.Address); err == nil {
			if n, err := strconv.Atoi(port); err == nil {
				fact.Port = &n
			}
		}
	}
	return fact, true
}

func factOf(facts []api.ActionFact, kind api.ActionFactKind) (api.ActionFact, bool) {
	for _, fact := range facts {
		if fact.Kind == kind {
			return fact, true
		}
	}
	return api.ActionFact{}, false
}
