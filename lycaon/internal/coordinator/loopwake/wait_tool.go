package loopwake

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/batch"
	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

// One-second waits support short local readiness checks.
const (
	MinWaitSeconds     = 1
	DefaultWaitSeconds = 60
	MaxWaitSeconds     = 1800
)

// WaitToolResult is returned by the cross-role wait tool.
type WaitToolResult struct {
	UntilComplete bool                   `json:"until_complete,omitempty"`
	Status        string                 `json:"status"`
	LeaseID       string                 `json:"lease_id,omitempty"`
	WakeAt        string                 `json:"wake_at,omitempty"`
	Reason        string                 `json:"reason,omitempty"`
	TimeoutMS     int                    `json:"timeout_ms,omitempty"`
	Resumed       bool                   `json:"resumed,omitempty"`
	Conditions    []awaitstore.Condition `json:"conditions,omitempty"`
}

type WaitToolDeps struct {
	Store             *awaitstore.Store
	ProfileConditions map[string]map[string]bool
	SecretMatcher     *secretmatch.Matcher
	RuntimeContext    context.Context
}

type waitSubscription struct {
	Conditions         []awaitstore.Condition
	Triggers           []WaitTrigger
	ProcessHandles     []string
	WorkerHandles      []string
	ExplicitConditions bool
	UntilComplete      bool
	Bounded            bool
}

type waitRequest struct {
	waitSubscription
	Resume         bool
	TimeoutMS      int
	Reason         string
	ResumeDeadline time.Time
	// ResumeBounded carries whether the resumed wait had a deadline backstop.
	ResumeBounded bool
	ExplicitMode  bool
}

// RegisterWaitTool registers durable agent waits.
func RegisterWaitTool(reg *tools.DefaultRegistry, loop *WaitSubscriptions, deps WaitToolDeps) error {
	if reg == nil || loop == nil {
		return fmt.Errorf("registry and loop engine required")
	}
	loop.SetWaitStore(deps.Store)
	return reg.Register("wait", func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		request, err := prepareWaitRequest(ctx, loop, deps, args, tctx)
		if err != nil {
			return "", err
		}
		if request.UntilComplete {
			winner, ready := loop.processConditionOutcome(tctx.SessionID, awaitstore.Condition{
				Kind: "process_done", Handles: request.ProcessHandles,
			})
			if ready {
				out, err := surveyjson.Marshal(WaitToolResult{Status: "process_done", Conditions: []awaitstore.Condition{winner}})
				return string(out), err
			}
		}
		resume := request.Resume
		timeoutMS := request.TimeoutMS
		reason := request.Reason
		conditions := request.Conditions
		triggers := request.Triggers
		processHandles := request.ProcessHandles
		explicitConditions := request.ExplicitConditions
		triggers = ensureTimerBackstop(triggers)
		hasDeadline := args["timeout_ms"] != nil || (resume && request.ResumeBounded)
		if request.UntilComplete && !hasDeadline {
			triggers = removeWaitTrigger(triggers, WaitTriggerTimer)
		}
		triggerNames := make([]string, len(triggers))
		for i, t := range triggers {
			triggerNames[i] = string(t)
		}
		// These states require a real wait even when workers are already idle.
		batchPhase := loop.Policy.coordinatorBatchState(ctx, tctx.SessionID).Phase
		batchClosed := batchPhase == batch.PhaseClosed
		soloDurationWait := !explicitConditions && batchPhase == batch.PhasePreDispatch
		pendingUserInput := loop.Facts.sessionHasPendingUserInput(ctx, tctx.SessionID)
		if !batchClosed && !pendingUserInput && batchPhase != batch.PhasePreDispatch &&
			waitSubscribesNextWorkerDone(triggers) && loop.Cycles.WorkerCycleIsIdle(ctx, tctx.SessionID) {
			return waitAlreadySatisfied(
				"next_worker_done",
				"The dispatched workers have finished.",
				triggerNames,
			)
		}
		if !batchClosed && !soloDurationWait && !pendingUserInput && waitSubscribesAllWorkersIdle(triggers) && loop.Cycles.WorkerCycleIsIdle(ctx, tctx.SessionID) {
			return waitAlreadySatisfied(
				"all_workers_idle",
				"No workers are pending or running.",
				triggerNames,
			)
		}
		if !batchClosed && !pendingUserInput && waitSubscribesScanDone(triggers) && !loop.scanCycleOpen(ctx, tctx.SessionID) {
			return waitAlreadySatisfied(
				"scan_done",
				"No security scans are pending or running for this project.",
				triggerNames,
			)
		}
		if !request.UntilComplete && !batchClosed && !pendingUserInput && waitSubscribesProcessDone(triggers) && !loop.processCycleOpen(tctx.SessionID, processHandles) {
			return waitAlreadySatisfied(
				"process_done",
				"No selected commands are running for this session.",
				triggerNames,
			)
		}
		until, resumed := loop.Waits.ResolveWaitUntil(ctx, tctx.SessionID, resume && !request.ExplicitMode, time.Duration(timeoutMS)*time.Millisecond)
		if request.ResumeDeadline.After(time.Now().UTC()) {
			until, resumed = request.ResumeDeadline, true
		}
		if pendingUserInput && args["timeout_ms"] == nil {
			if pendingUntil := loop.Facts.pendingUserInputWaitDeadline(ctx, tctx.SessionID); pendingUntil.After(until) {
				until = pendingUntil
			}
		}
		if request.UntilComplete && !hasDeadline {
			until = time.Time{}
			resumed = resume
		}
		// Coordinator bounded waits keep a timer backstop.
		armedTriggers := triggers
		if strings.TrimSpace(tctx.WorkerJobID) != "" {
			// The durable worker queue schedules its own deadline.
			armedTriggers = removeWaitTrigger(armedTriggers, WaitTriggerTimer)
		}
		var leaseID string
		var lease awaitstore.Lease
		if deps.Store != nil {
			lease, err = deps.Store.Arm(ctx, awaitstore.Lease{
				SessionID: tctx.SessionID, RootSessionID: rootSessionID(tctx), ProjectID: tctx.ProjectID,
				ProjectDir: tctx.ActiveRootPath(), ToolCallID: tctx.ToolCallID, WorkerJobID: tctx.WorkerJobID,
				ProfileID: tctx.Agent, Deadline: until, UntilComplete: request.UntilComplete, Conditions: conditions,
				LoopbackPorts: append([]uint16(nil), tctx.LoopbackConnectPorts...), Reason: reason,
			})
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(tctx.WorkerJobID) == "" {
				// Keep a result that settled this lease during registration.
				if winner, ok := loop.Deliveries.waitWinner(tctx.SessionID); ok && winner.LeaseID != lease.ID {
					loop.Deliveries.waitWinners.CompareAndDelete(strings.TrimSpace(tctx.SessionID), winner)
				}
			}
			leaseID = lease.ID
		}
		loop.Waits.enterSleep(ctx, tctx.SessionID, sleepArm{
			until: until, untilComplete: request.UntilComplete, reason: reason,
			triggers: armedTriggers, processHandles: processHandles, workerHandles: request.WorkerHandles, mover: SleepMoverHost,
		})
		loop.Waits.MarkWaitCalled(tctx.SessionID)
		if deps.Store != nil {
			monitorCtx := deps.RuntimeContext
			if monitorCtx == nil {
				monitorCtx = context.Background()
			}
			startConditionMonitor(monitorCtx, loop, deps.Store, lease) //nolint:contextcheck // Monitor follows application lifetime.
		}
		if tctx.Out != nil {
			tctx.Out.OwnerRef = leaseID
			tctx.Out.Completion = &api.ToolCompletion{Operation: "wait", State: "parked", ResourceKind: "wait", ResourceID: leaseID}
		}
		result := WaitToolResult{
			Status: "parked", LeaseID: leaseID, Reason: reason, UntilComplete: request.UntilComplete,
			Resumed: resumed, Conditions: conditions,
		}
		if !until.IsZero() {
			result.WakeAt = until.Format(time.RFC3339)
		}
		if !resumed && (!request.UntilComplete || hasDeadline) {
			result.TimeoutMS = timeoutMS
		}
		out, err := surveyjson.Marshal(result)
		if err != nil {
			return "", err
		}
		return string(out), nil
	})
}

func waitRequestReject(err error) error {
	if tools.AsToolReject(err) != nil {
		return err
	}
	return &tools.ToolReject{Code: "TOOL_ARGS_INVALID", Data: map[string]any{
		"tool": "wait", "reason": err.Error(),
	}}
}

func prepareWaitRequest(ctx context.Context, loop *WaitSubscriptions, deps WaitToolDeps, args map[string]any, tctx tools.ToolContext) (waitRequest, error) {
	request, err := parseWaitRequest(args)
	if err != nil {
		return waitRequest{}, waitRequestReject(err)
	}
	if request.Resume {
		if err := restoreWaitRequest(ctx, loop, deps.Store, tctx, &request); err != nil {
			return waitRequest{}, err
		}
	}
	if err := validateProfileConditions(tctx.Agent, request.Conditions, deps.ProfileConditions); err != nil {
		return waitRequest{}, err
	}
	if err := screenWaitURLs(ctx, deps, tctx, request.Conditions); err != nil {
		return waitRequest{}, err
	}
	if err := validateConditionAuthority(request.Conditions, tctx); err != nil {
		return waitRequest{}, err
	}
	if request.UntilComplete {
		if err := validateCompletionWait(loop, tctx.SessionID, request.Conditions); err != nil {
			return waitRequest{}, waitRequestReject(err)
		}
		if deps.Store == nil {
			return waitRequest{}, waitRequestReject(fmt.Errorf("completion waits require a durable wait store"))
		}
	}
	return request, nil
}

func restoreWaitRequest(ctx context.Context, loop *WaitSubscriptions, store *awaitstore.Store, tctx tools.ToolContext, request *waitRequest) error {
	if strings.TrimSpace(tctx.WorkerJobID) == "" {
		if subscription, complete := loop.Waits.runtimeWaitSubscription(tctx.SessionID); complete {
			if !request.ExplicitConditions {
				request.Conditions = subscription.Conditions
				request.Triggers = subscription.Triggers
				request.ProcessHandles = subscription.ProcessHandles
				request.WorkerHandles = subscription.WorkerHandles
			}
			if !request.ExplicitMode {
				request.UntilComplete = subscription.UntilComplete
				request.ResumeBounded = subscription.Bounded
			}
			return nil
		}
	}
	lease, found, err := store.LatestResumeCandidate(ctx, tctx.SessionID)
	if err != nil || !found {
		return err
	}
	if !request.ExplicitMode {
		request.ResumeDeadline = lease.Deadline
		request.ResumeBounded = !lease.Deadline.IsZero()
		request.UntilComplete = lease.UntilComplete
	}
	if !request.ExplicitConditions {
		request.Conditions = append([]awaitstore.Condition(nil), lease.Conditions...)
		request.Triggers, request.ProcessHandles = triggersFromConditions(request.Conditions)
		request.WorkerHandles = workerHandlesFromConditions(request.Conditions)
	}
	return nil
}

func parseWaitRequest(args map[string]any) (waitRequest, error) {
	request := waitRequest{
		Resume:    parseWaitResumeArg(args["resume"]),
		TimeoutMS: DefaultWaitSeconds * 1000,
	}
	if value, ok := args["timeout_ms"]; ok {
		switch n := value.(type) {
		case float64:
			request.TimeoutMS = int(n)
		case int:
			request.TimeoutMS = n
		case int64:
			request.TimeoutMS = int(n)
		}
	}
	if request.TimeoutMS > MaxWaitSeconds*1000 {
		request.TimeoutMS = MaxWaitSeconds * 1000
	}
	request.Reason, _ = args["reason"].(string)
	request.Reason = strings.TrimSpace(request.Reason)
	if request.Reason == "" {
		request.Reason = "Waiting"
	}
	var err error
	request.waitSubscription, err = resolveConditions(args["conditions"])
	if err != nil {
		return waitRequest{}, err
	}
	if raw, present := args["until_complete"]; present {
		value, ok := raw.(bool)
		if !ok {
			return waitRequest{}, fmt.Errorf("until_complete must be a boolean")
		}
		request.UntilComplete = value
		request.ExplicitMode = true
	}
	if _, present := args["timeout_ms"]; present {
		request.ExplicitMode = true
	}
	return request, nil
}

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

func resolveConditions(raw any) (waitSubscription, error) {
	if raw == nil {
		return waitSubscription{Triggers: []WaitTrigger{WaitTriggerTimer}}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return waitSubscription{}, fmt.Errorf("conditions must be an array")
	}
	if len(items) == 0 {
		return waitSubscription{Triggers: []WaitTrigger{WaitTriggerTimer}, ExplicitConditions: true}, nil
	}
	conditions := make([]awaitstore.Condition, 0, len(items))
	triggers := make([]WaitTrigger, 0, len(items)+1)
	var handles []string
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return waitSubscription{}, fmt.Errorf("each wait condition must be an object")
		}
		kind, _ := obj["kind"].(string)
		trigger := WaitTrigger(strings.TrimSpace(kind))
		if _, allowed := validWaitTriggers[trigger]; !allowed || trigger == WaitTriggerTimer {
			return waitSubscription{}, fmt.Errorf("unsupported wait condition %q", kind)
		}
		if err := validateConditionShape(trigger, obj); err != nil {
			return waitSubscription{}, err
		}
		condition := awaitstore.Condition{Kind: string(trigger)}
		if rawHandles, present := obj["handles"]; present {
			parsed, err := ResolveProcessHandlesArgs(rawHandles)
			if err != nil {
				return waitSubscription{}, err
			}
			condition.Handles = parsed
			if trigger == WaitTriggerProcessDone {
				handles = append(handles, parsed...)
			}
		}
		switch trigger {
		case WaitTriggerHTTPReady:
			parsed, err := parseHTTPReadyCondition(obj)
			if err != nil {
				return waitSubscription{}, err
			}
			condition = parsed
		case WaitTriggerPortReady:
			parsed, err := parsePortReadyCondition(obj)
			if err != nil {
				return waitSubscription{}, err
			}
			condition = parsed
		case WaitTriggerTimer, WaitTriggerNextWorkerDone, WaitTriggerAllWorkersIdle, WaitTriggerOverlayPromote,
			WaitTriggerScanDone, WaitTriggerProcessDone:
		}
		conditions = append(conditions, condition)
		triggers = append(triggers, trigger)
	}
	return waitSubscription{
		Conditions: conditions, Triggers: ensureTimerBackstop(triggers),
		ProcessHandles: normalizeProcessHandles(handles), WorkerHandles: workerHandlesFromConditions(conditions),
		ExplicitConditions: true,
	}, nil
}

func validateConditionShape(trigger WaitTrigger, obj map[string]any) error {
	allowed := map[string]bool{"kind": true}
	switch trigger {
	case WaitTriggerProcessDone, WaitTriggerNextWorkerDone:
		allowed["handles"] = true
	case WaitTriggerHTTPReady:
		for _, field := range []string{"url", "method", "status_min", "status_max", "port"} {
			allowed[field] = true
		}
	case WaitTriggerPortReady:
		allowed["host"] = true
		allowed["port"] = true
	case WaitTriggerTimer, WaitTriggerAllWorkersIdle, WaitTriggerOverlayPromote, WaitTriggerScanDone:
	}
	for field := range obj {
		if !allowed[field] {
			return fmt.Errorf("%s does not accept %s", trigger, field)
		}
	}
	return nil
}

func parseHTTPReadyCondition(obj map[string]any) (awaitstore.Condition, error) {
	rawURL, _ := obj["url"].(string)
	declared, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return awaitstore.Condition{}, fmt.Errorf("http_ready: invalid url: %w", err)
	}
	if declared.Fragment != "" {
		return awaitstore.Condition{}, fmt.Errorf("http_ready url cannot contain a fragment")
	}
	if portRaw, ok := obj["port"]; ok {
		portVal := intValue(portRaw, 0)
		if portVal <= 0 || portVal > 65535 {
			return awaitstore.Condition{}, fmt.Errorf("http_ready port must be within 1..65535")
		}
		if declaredPort := declared.Port(); declaredPort != "" {
			if declaredPort != strconv.Itoa(portVal) {
				return awaitstore.Condition{}, fmt.Errorf("http_ready port %d conflicts with url port %s", portVal, declaredPort)
			}
		} else {
			declared.Host = net.JoinHostPort(declared.Hostname(), strconv.Itoa(portVal))
			rawURL = declared.String()
		}
	}
	target, err := outboundhttp.NormalizeURL(rawURL)
	if err != nil {
		return awaitstore.Condition{}, fmt.Errorf("http_ready: %w", err)
	}
	method, _ := obj["method"].(string)
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = "GET"
	}
	if method != "GET" && method != "HEAD" {
		return awaitstore.Condition{}, fmt.Errorf("http_ready method must be GET or HEAD")
	}
	minimum := intValue(obj["status_min"], 200)
	maximum := intValue(obj["status_max"], 399)
	if minimum < 100 || maximum > 599 || minimum > maximum {
		return awaitstore.Condition{}, fmt.Errorf("http_ready status range must be within 100..599")
	}
	return awaitstore.Condition{Kind: "http_ready", URL: target.String(), Method: method, StatusMin: minimum, StatusMax: maximum}, nil
}

func parsePortReadyCondition(obj map[string]any) (awaitstore.Condition, error) {
	host, _ := obj["host"].(string)
	host = strings.TrimSpace(host)
	if host == "" {
		host = "localhost"
	}
	if ip := net.ParseIP(host); ip == nil && strings.ContainsAny(host, "\x00\r\n\t:/") {
		return awaitstore.Condition{}, fmt.Errorf("port_ready host must be a hostname or IP without a port")
	}
	port := intValue(obj["port"], 0)
	if port < 1 || port > 65535 {
		return awaitstore.Condition{}, fmt.Errorf("port_ready requires a port from 1 to 65535")
	}
	return awaitstore.Condition{Kind: "port_ready", Host: host, Port: uint16(port)}, nil
}

func intValue(raw any, fallback int) int {
	switch value := raw.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return fallback
	}
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

func conditionsFromTriggers(triggers []WaitTrigger, processHandles, workerHandles []string) []awaitstore.Condition {
	out := make([]awaitstore.Condition, 0, len(triggers))
	for _, trigger := range triggers {
		if trigger == WaitTriggerTimer {
			continue
		}
		condition := awaitstore.Condition{Kind: string(trigger)}
		switch trigger {
		case WaitTriggerProcessDone:
			condition.Handles = append([]string(nil), processHandles...)
		case WaitTriggerNextWorkerDone:
			condition.Handles = append([]string(nil), workerHandles...)
		case WaitTriggerTimer, WaitTriggerAllWorkersIdle, WaitTriggerOverlayPromote, WaitTriggerScanDone,
			WaitTriggerHTTPReady, WaitTriggerPortReady:
		}
		out = append(out, condition)
	}
	return out
}

func triggersFromConditions(conditions []awaitstore.Condition) ([]WaitTrigger, []string) {
	triggers := make([]WaitTrigger, 0, len(conditions)+1)
	var handles []string
	for _, condition := range conditions {
		triggers = append(triggers, WaitTrigger(condition.Kind))
		if condition.Kind == string(WaitTriggerProcessDone) {
			handles = append(handles, condition.Handles...)
		}
	}
	return ensureTimerBackstop(triggers), normalizeProcessHandles(handles)
}

// workerHandlesFromConditions lists the task ids a next_worker_done wait named.
func workerHandlesFromConditions(conditions []awaitstore.Condition) []string {
	var handles []string
	for _, condition := range conditions {
		if condition.Kind == string(WaitTriggerNextWorkerDone) {
			handles = append(handles, condition.Handles...)
		}
	}
	return normalizeProcessHandles(handles)
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

// RecoverWaitLeases reconstructs timers, subscriptions, and active probes after boot.
// Worker deadlines and runnable transitions remain owned by the worker queue transaction.
func RecoverWaitLeases(ctx context.Context, loop *WaitSubscriptions, store *awaitstore.Store) error {
	if loop == nil || store == nil {
		return nil
	}
	leases, err := store.Active(ctx)
	if err != nil {
		return err
	}
	for _, lease := range leases {
		triggers := []WaitTrigger{WaitTriggerTimer}
		var handles []string
		for _, condition := range lease.Conditions {
			triggers = append(triggers, WaitTrigger(condition.Kind))
			if condition.Kind == string(WaitTriggerProcessDone) {
				handles = append(handles, condition.Handles...)
			}
		}
		if lease.Deadline.IsZero() || strings.TrimSpace(lease.WorkerJobID) != "" {
			triggers = removeWaitTrigger(triggers, WaitTriggerTimer)
		}
		loop.Waits.enterSleep(ctx, lease.SessionID, sleepArm{
			until: lease.Deadline, untilComplete: lease.UntilComplete, reason: lease.Reason,
			triggers: triggers, processHandles: handles, workerHandles: workerHandlesFromConditions(lease.Conditions), mover: SleepMoverHost,
		})
		startConditionMonitor(ctx, loop, store, lease)
	}
	pending, err := store.PendingAgentResumes(ctx)
	if err != nil {
		return err
	}
	for _, lease := range pending {
		loop.Deliveries.rememberWaitWinner(lease.SessionID, lease.ID, lease.Winner)
		loop.Nudges.Nudge(ctx, lease.SessionID, anchor.LoopWake, anchor.LoopWake, lease.ID, anchor.Envelope{})
	}
	return nil
}

// waitAlreadySatisfied returns a satisfied subscription without sleeping.
func waitAlreadySatisfied(status, reason string, triggerNames []string) (string, error) {
	conditions := make([]awaitstore.Condition, 0, len(triggerNames))
	for _, name := range triggerNames {
		if name != string(WaitTriggerTimer) {
			conditions = append(conditions, awaitstore.Condition{Kind: name})
		}
	}
	out, err := surveyjson.Marshal(WaitToolResult{
		Status: status, Reason: reason, Conditions: conditions,
	})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func parseWaitResumeArg(v any) bool {
	resume, _ := v.(bool)
	return resume
}

// WaitCompletionEndsCycle reads the host-stamped lifecycle outcome.
func WaitCompletionEndsCycle(toolName string, completion *api.ToolCompletion) bool {
	return strings.EqualFold(strings.TrimSpace(toolName), "wait") && completion != nil &&
		completion.Operation == "wait" && completion.State == "parked"
}

// AskUserEndsCycle reports a pending successful ask.
func AskUserEndsCycle(toolName, toolContent string, succeeded bool) bool {
	if !succeeded || strings.TrimSpace(strings.ToLower(toolName)) != "ask_user" {
		return false
	}
	return toolResultStatus(toolContent) == "pending"
}

func toolResultStatus(content string) string {
	var result struct {
		Status string `json:"status"`
	}
	// Decode the leading JSON object; host guidance may follow it.
	if err := json.NewDecoder(strings.NewReader(content)).Decode(&result); err != nil {
		return ""
	}
	return strings.TrimSpace(result.Status)
}
