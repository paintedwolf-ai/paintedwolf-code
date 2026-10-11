package loopwake

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/outboundhttp"
)

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
