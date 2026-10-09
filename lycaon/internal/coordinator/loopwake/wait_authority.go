package loopwake

import (
	"context"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

func screenWaitURLs(ctx context.Context, deps WaitToolDeps, tctx tools.ToolContext, conditions []awaitstore.Condition) error {
	if deps.SecretMatcher == nil || deps.SecretMatcher.Inert() {
		return nil
	}
	ctx = secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{
		SessionID: tctx.SessionID, RootSessionID: rootSessionID(tctx), ProjectID: tctx.ProjectID,
		ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.ToolCallID,
	})
	for _, condition := range conditions {
		if condition.Kind != "http_ready" {
			continue
		}
		matches := deps.SecretMatcher.ScreenContext(ctx, condition.URL)
		if len(matches) == 0 {
			continue
		}
		match := matches[0]
		return &tools.ToolReject{Code: "WAIT_PROBE_SECRET_UNSUPPORTED", Data: map[string]any{
			"surface": "wait_probe", "rule_id": match.RuleID, "shape": match.GenericShape,
		}}
	}
	return nil
}

func validateConditionAuthority(conditions []awaitstore.Condition, tctx tools.ToolContext) error {
	for _, condition := range conditions {
		switch condition.Kind {
		case "port_ready":
			if !tctx.LoopbackConnectGranted || !portCovered(tctx.LoopbackConnectPorts, condition.Port) {
				return &tools.ToolReject{Code: isolation.CodeTryLoopbackConnect, Data: map[string]any{"port": condition.Port}}
			}
		case "http_ready":
			target, _ := url.Parse(condition.URL)
			if target == nil || !egress.SyntacticLoopback(target.Hostname()) {
				continue
			}
			port := 80
			if strings.EqualFold(target.Scheme, "https") {
				port = 443
			}
			if target.Port() != "" {
				port, _ = strconv.Atoi(target.Port())
			}
			if !tctx.LoopbackConnectGranted || !portCovered(tctx.LoopbackConnectPorts, uint16(port)) {
				return &tools.ToolReject{Code: isolation.CodeTryLoopbackConnect, Data: map[string]any{"port": port}}
			}
		}
	}
	return nil
}

func rootSessionID(tctx tools.ToolContext) string {
	if root := strings.TrimSpace(tctx.ParentSessionID); root != "" {
		return root
	}
	return strings.TrimSpace(tctx.SessionID)
}

func validateProfileConditions(profile string, conditions []awaitstore.Condition, profiles map[string]map[string]bool) error {
	if len(profiles) == 0 {
		return nil
	}
	allowed := profiles[strings.TrimSpace(profile)]
	for _, condition := range conditions {
		if !allowed[condition.Kind] {
			kinds := make([]string, 0, len(allowed))
			for kind, enabled := range allowed {
				if enabled {
					kinds = append(kinds, kind)
				}
			}
			sort.Strings(kinds)
			return &tools.ToolReject{Code: "WAIT_CONDITION_NOT_ALLOWED", Data: map[string]any{"condition": condition.Kind, "profile": profile, "wait_allowed_conditions": kinds}}
		}
	}
	return nil
}
