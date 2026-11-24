package watch

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

// ToolDeps wires watch and watch_control handlers to store and session policy.
type ToolDeps struct {
	Store               *Store
	ProfileConditions   map[string]map[string]bool
	SecretMatcher       *secretmatch.Matcher
	WorkerKnown         func(sessionID, handle string) bool
	ProcessKnown        func(sessionID, handle string) bool
	ProjectChat         func(ctx context.Context, projectID string) bool
	StandingWatchExists func(projectID, watchID string) bool
	OverlappingStanding func(ctx context.Context, projectID string, conditions []awaitstore.Condition) []OverlappingStandingWatch
}

type watchToolReceipt struct {
	WatchID             string                      `json:"watch_id"`
	Handle              string                      `json:"handle"`
	State               string                      `json:"state"`
	Delivery            DeliveryMode                `json:"delivery"`
	Destination         string                      `json:"destination"`
	ExpiresAt           string                      `json:"expires_at"`
	WaitCondition       awaitstore.Condition        `json:"wait_condition"`
	PendingMatches      int                         `json:"pending_matches"`
	Hint                string                      `json:"hint,omitempty"`
	StandingDraft       bool                        `json:"standing_draft,omitempty"`
	OverlappingStanding *[]OverlappingStandingWatch `json:"overlapping_standing,omitempty"`
}

// RegisterWatchTools registers native watch and watch_control handlers.
func RegisterWatchTools(reg *tools.DefaultRegistry, deps ToolDeps) error {
	if err := reg.Register("watch", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return executeWatchTool(ctx, deps, args, tctx)
	}); err != nil {
		return err
	}
	return reg.Register("watch_control", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		return executeWatchControlTool(ctx, deps, args, tctx)
	})
}

func executeWatchTool(ctx context.Context, deps ToolDeps, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := checkTopLevelKeys(args, "conditions", "delivery", "repeat", "expires_at", "standing"); err != nil {
		return "", mapToolReject(err)
	}
	now := time.Now().UTC()
	if deps.Store != nil && deps.Store.clock != nil {
		now = deps.Store.clock.Now()
	}
	expiresAt, err := parseExpiresAtArg(args["expires_at"], now)
	if err != nil {
		return "", mapToolReject(err)
	}
	effectiveExpiry := expiresAt
	if effectiveExpiry.IsZero() {
		effectiveExpiry = now.Add(DefaultLifetime)
	}
	delivery, explicitDelivery, err := parseDeliveryArg(args["delivery"])
	if err != nil {
		return "", mapToolReject(err)
	}
	repeat, err := parseRepeatArg(args["repeat"])
	if err != nil {
		return "", mapToolReject(err)
	}
	profileID := strings.TrimSpace(tctx.Identity.Agent)
	isWorker := strings.TrimSpace(tctx.Identity.WorkerJobID) != "" || (profileID != "" && profileID != toolcontract.CoordinatorProfileID)
	if isWorker && delivery == DeliveryWake {
		return "", &toolrejection.ToolReject{
			Code: "WATCH_WAKE_NOT_ALLOWED",
			Data: map[string]any{"reason": "worker watches cannot request ambient wake"},
		}
	}
	conditions, err := parseConditionsArg(args["conditions"], now, effectiveExpiry, deps, profileID, tctx.Identity.SessionID, tctx.Source.Roots)
	if err != nil {
		return "", mapToolReject(err)
	}
	if err := validateLoopbackAuthority(conditions, tctx); err != nil {
		return "", err
	}
	if err := screenWatchURLs(ctx, deps.SecretMatcher, tctx, conditions); err != nil {
		return "", err
	}
	projectID := strings.TrimSpace(tctx.Identity.ProjectID)
	projectChat := projectID != ""
	if deps.ProjectChat != nil {
		projectChat = deps.ProjectChat(ctx, projectID)
	}
	standing, err := parseStandingDraftArg(args["standing"], conditions, delivery, explicitDelivery, isWorker, projectChat, projectID, deps)
	if err != nil {
		return "", mapToolReject(err)
	}
	if standing != nil {
		delivery = DeliveryCollect
	}
	if deps.Store == nil || !deps.Store.Configured() {
		return encodeToolJSON(watchToolReceipt{
			Handle:        "watch-1",
			State:         "pending",
			Delivery:      delivery,
			Destination:   ownerFromToolContext(tctx).Destination(),
			ExpiresAt:     effectiveExpiry.UTC().Format(time.RFC3339),
			WaitCondition: awaitstore.Condition{Kind: ConditionWatchMatch, Handles: []string{"watch-1"}},
			Hint:          "When you need this result, pass this condition to wait.",
		})
	}
	receipt, err := deps.Store.Register(ctx, RegisterRequest{
		Owner:        ownerFromToolContext(tctx),
		InvocationID: strings.TrimSpace(tctx.Identity.ToolCallID),
		Conditions:   conditions,
		Authority:    authorityFromToolContext(tctx),
		Delivery:     delivery,
		Repeat:       repeat,
		ExpiresAt:    expiresAt,
		Standing:     standing,
	})
	if err != nil {
		return "", mapToolReject(err)
	}
	wire := watchToolReceipt{
		WatchID:        receipt.WatchID,
		Handle:         receipt.Handle,
		State:          receipt.State,
		Delivery:       receipt.Delivery,
		Destination:    receipt.Destination,
		ExpiresAt:      receipt.ExpiresAt,
		WaitCondition:  receipt.WaitCondition,
		PendingMatches: receipt.PendingMatches,
		Hint:           receipt.Hint,
		StandingDraft:  receipt.StandingDraft,
	}
	if receipt.StandingDraft {
		overlaps := []OverlappingStandingWatch{}
		if deps.OverlappingStanding != nil {
			if found := deps.OverlappingStanding(ctx, projectID, conditions); len(found) > 0 {
				overlaps = found
			}
		}
		wire.OverlappingStanding = &overlaps
	}
	return encodeToolJSON(wire)
}

func executeWatchControlTool(ctx context.Context, deps ToolDeps, args map[string]any, tctx tools.ToolContext) (string, error) {
	if err := checkTopLevelKeys(args, "handle", "operation", "delivery"); err != nil {
		return "", mapToolReject(err)
	}
	handle, ok := args["handle"].(string)
	handle = strings.TrimSpace(handle)
	if !ok || handle == "" {
		return "", mapToolReject(invalidField("handle", "handle is required"))
	}
	op, ok := args["operation"].(string)
	op = strings.TrimSpace(op)
	if !ok || (op != "inspect" && op != "set_delivery" && op != "cancel") {
		return "", mapToolReject(invalidField("operation", "operation must be inspect, set_delivery, or cancel"))
	}
	_, hasDelivery := args["delivery"]
	if op != "set_delivery" && hasDelivery {
		return "", mapToolReject(invalidField("delivery", "delivery is accepted only with operation=set_delivery"))
	}
	owner := ownerFromToolContext(tctx)
	if deps.Store == nil || !deps.Store.Configured() {
		return "", &toolrejection.ToolReject{Code: "WATCH_HANDLE_UNKNOWN", Data: map[string]any{"handle": handle}}
	}
	switch op {
	case "inspect":
		view, err := deps.Store.Inspect(ctx, owner, handle)
		if err != nil {
			return "", mapToolReject(err)
		}
		return encodeToolJSON(view)
	case "cancel":
		receipt, err := deps.Store.Cancel(ctx, owner, handle)
		if err != nil {
			return "", mapToolReject(err)
		}
		return encodeToolJSON(receipt)
	default:
		if !hasDelivery {
			return "", mapToolReject(invalidField("delivery", "delivery is required with operation=set_delivery"))
		}
		mode, _, err := parseDeliveryArg(args["delivery"])
		if err != nil {
			return "", mapToolReject(err)
		}
		profileID := strings.TrimSpace(tctx.Identity.Agent)
		isWorker := owner.Kind == OwnerJob || (profileID != "" && profileID != toolcontract.CoordinatorProfileID)
		if isWorker && mode == DeliveryWake {
			return "", &toolrejection.ToolReject{
				Code: "WATCH_WAKE_NOT_ALLOWED",
				Data: map[string]any{"reason": "worker watches cannot request ambient wake"},
			}
		}
		updated, err := deps.Store.SetDelivery(ctx, owner, handle, mode)
		if err != nil {
			return "", mapToolReject(err)
		}
		return encodeToolJSON(map[string]any{"handle": handle, "delivery": updated})
	}
}

func encodeToolJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func checkTopLevelKeys(args map[string]any, allowed ...string) error {
	set := make(map[string]bool, len(allowed))
	for _, k := range allowed {
		set[k] = true
	}
	for k := range args {
		if !set[k] {
			return invalidField(k, "unexpected argument "+k)
		}
	}
	return nil
}

func parseExpiresAtArg(raw any, now time.Time) (time.Time, error) {
	if raw == nil {
		return time.Time{}, nil
	}
	s, ok := raw.(string)
	s = strings.TrimSpace(s)
	if !ok || s == "" {
		return time.Time{}, &ValidationError{Code: "WATCH_EXPIRY_INVALID", Field: "expires_at", Message: "expires_at must be an RFC 3339 timestamp"}
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, &ValidationError{Code: "WATCH_EXPIRY_INVALID", Field: "expires_at", Message: "expires_at must be a valid RFC 3339 timestamp"}
	}
	ahead := parsed.Sub(now)
	if ahead < MinLifetime || ahead > MaxLifetime {
		return time.Time{}, &ValidationError{Code: "WATCH_EXPIRY_INVALID", Field: "expires_at", Message: "expires_at must be between 1 minute and 30 days ahead"}
	}
	return parsed.UTC(), nil
}

func parseDeliveryArg(raw any) (DeliveryMode, bool, error) {
	if raw == nil {
		return DeliveryCollect, false, nil
	}
	s, ok := raw.(string)
	s = strings.TrimSpace(s)
	if !ok || (s != string(DeliveryCollect) && s != string(DeliveryWake)) {
		return "", true, invalidField("delivery", "delivery must be collect or wake")
	}
	return DeliveryMode(s), true, nil
}

func parseRepeatArg(raw any) (bool, error) {
	if raw == nil {
		return false, nil
	}
	b, ok := raw.(bool)
	if !ok {
		return false, invalidField("repeat", "repeat must be a boolean")
	}
	return b, nil
}

func ownerFromToolContext(tctx tools.ToolContext) Owner {
	return Owner{
		ProjectID:   tctx.Identity.ProjectID,
		SessionID:   tctx.Identity.SessionID,
		WorkerJobID: tctx.Identity.WorkerJobID,
	}.Normalized()
}

func authorityFromToolContext(tctx tools.ToolContext) Authority {
	var paths []string
	for _, r := range nonOutsideRoots(tctx.Source.Roots) {
		if p := strings.TrimSpace(r.Path); p != "" {
			paths = append(paths, p)
		}
	}
	var ports []uint16
	if tctx.Local.LoopbackConnectGranted {
		ports = append(ports, tctx.Local.LoopbackConnectPorts...)
	}
	return Authority{
		WorkspaceID:   strings.TrimSpace(tctx.Source.ActiveRootID),
		RootPaths:     paths,
		LoopbackPorts: ports,
	}
}

func validateLoopbackAuthority(conditions []awaitstore.Condition, tctx tools.ToolContext) error {
	for _, cond := range conditions {
		switch cond.Kind {
		case "port_ready":
			if !tctx.Local.LoopbackConnectGranted || !loopbackPortAllowed(tctx.Local.LoopbackConnectPorts, cond.Port) {
				return &toolrejection.ToolReject{Code: isolation.CodeTryLoopbackConnect, Data: map[string]any{"port": cond.Port}}
			}
		case "http_ready":
			target, _ := url.Parse(cond.URL)
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
			if !tctx.Local.LoopbackConnectGranted || !loopbackPortAllowed(tctx.Local.LoopbackConnectPorts, uint16(port)) {
				return &toolrejection.ToolReject{Code: isolation.CodeTryLoopbackConnect, Data: map[string]any{"port": port}}
			}
		}
	}
	return nil
}

func loopbackPortAllowed(allowed []uint16, port uint16) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, p := range allowed {
		if p == port {
			return true
		}
	}
	return false
}

func screenWatchURLs(ctx context.Context, matcher *secretmatch.Matcher, tctx tools.ToolContext, conditions []awaitstore.Condition) error {
	if matcher == nil || matcher.Inert() {
		return nil
	}
	root := strings.TrimSpace(tctx.Identity.ParentSessionID)
	if root == "" {
		root = strings.TrimSpace(tctx.Identity.SessionID)
	}
	ctx = secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{
		SessionID: tctx.Identity.SessionID, RootSessionID: root, ProjectID: tctx.Identity.ProjectID,
		ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.Identity.ToolCallID,
	})
	for _, cond := range conditions {
		if cond.Kind != "http_ready" {
			continue
		}
		if matches := matcher.ScreenContext(ctx, cond.URL); len(matches) > 0 {
			m := matches[0]
			return &toolrejection.ToolReject{Code: "WAIT_PROBE_SECRET_UNSUPPORTED", Data: map[string]any{
				"surface": "wait_probe", "rule_id": m.RuleID, "shape": m.GenericShape,
			}}
		}
	}
	return nil
}

func notAllowedReject(kind, profileID string, allowed map[string]bool) error {
	kinds := make([]string, 0, len(allowed))
	for k, ok := range allowed {
		if ok {
			kinds = append(kinds, k)
		}
	}
	sort.Strings(kinds)
	return &toolrejection.ToolReject{
		Code: "WAIT_CONDITION_NOT_ALLOWED",
		Data: map[string]any{"condition": kind, "profile": profileID, "wait_allowed_conditions": kinds},
	}
}

func mapToolReject(err error) error {
	if err == nil {
		return nil
	}
	var existing *toolrejection.ToolReject
	if errors.As(err, &existing) {
		return existing
	}
	var capErr *CapacityError
	if errors.As(err, &capErr) {
		handles := capErr.ReclaimableHandles
		if handles == nil {
			handles = []string{}
		}
		return &toolrejection.ToolReject{
			Code: "WATCH_CAPACITY_REACHED",
			Data: map[string]any{"watch_reclaimable_handles": handles},
		}
	}
	var unknown *UnknownHandleError
	if errors.As(err, &unknown) {
		return &toolrejection.ToolReject{
			Code: "WATCH_HANDLE_UNKNOWN",
			Data: map[string]any{"handle": unknown.Handle},
		}
	}
	var termErr *TerminalError
	if errors.As(err, &termErr) {
		return &toolrejection.ToolReject{
			Code: "WATCH_TERMINAL",
			Data: map[string]any{"handle": termErr.Handle},
		}
	}
	var valErr *ValidationError
	if errors.As(err, &valErr) {
		fields := valErr.InvalidFields()
		if fields == nil {
			fields = []string{}
		}
		return &toolrejection.ToolReject{
			Code: valErr.Code,
			Data: map[string]any{
				"field":                valErr.Field,
				"reason":               valErr.Message,
				"watch_invalid_fields": fields,
			},
		}
	}
	return err
}
