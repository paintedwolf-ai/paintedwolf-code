package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// commandArgsWithoutDefaults omits known neutral runner defaults from the matching copy.
func commandArgsWithoutDefaults(args, schema map[string]any) map[string]any {
	properties, _ := schema["properties"].(map[string]any)
	out := maps.Clone(args)
	for name, value := range args {
		switch name {
		case "env", "verification", "background", "append", "allow_concurrent", "socks_proxy":
		default:
			continue
		}
		property, _ := properties[name].(map[string]any)
		fallback, declared := property["default"]
		if !declared {
			continue
		}
		actualJSON, actualErr := json.Marshal(value)
		defaultJSON, defaultErr := json.Marshal(fallback)
		neutralJSON := []byte("false")
		if name == "env" {
			neutralJSON = []byte("{}")
		}
		if actualErr == nil && defaultErr == nil && bytes.Equal(actualJSON, defaultJSON) && bytes.Equal(actualJSON, neutralJSON) {
			delete(out, name)
		}
	}
	return out
}

// Deadline bounds mirror the http_request and wait argument schemas.
const (
	httpRequestMinTimeoutMS = 1000
	httpRequestMaxTimeoutMS = 300000
	waitMinTimeoutMS        = 1000
	waitMaxTimeoutMS        = 1800000
)

// commandEnvelope holds runner options that affect native-call equivalence.
type commandEnvelope struct {
	command string
	cwd     string
	// timeoutMS is the runner deadline; zero means the call declared none.
	timeoutMS int
	// capability is the raw capability_request object, nil when absent.
	capability map[string]any
}

// parseCommandEnvelope rejects remaining options with no native representation.
func parseCommandEnvelope(args map[string]any) (commandEnvelope, bool) {
	command, ok := args["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return commandEnvelope{}, false
	}
	env := commandEnvelope{command: command}
	for name, value := range args {
		switch name {
		case "command":
		case "cwd":
			cwd, ok := value.(string)
			if !ok {
				return commandEnvelope{}, false
			}
			env.cwd = strings.TrimSpace(cwd)
		case "timeout_ms":
			timeout, ok := integerArg(value)
			if !ok || timeout <= 0 {
				return commandEnvelope{}, false
			}
			env.timeoutMS = timeout
		case "capability_request":
			capability, ok := value.(map[string]any)
			if !ok {
				return commandEnvelope{}, false
			}
			env.capability = capability
		default:
			return commandEnvelope{}, false
		}
	}
	return env, true
}

// replacements checks filesystem operands in the declared working directory.
func (env commandEnvelope) replacements(ctx context.Context, activeDir, sessionScratch string) ([]ReplacementCall, bool) {
	if env.cwd != "" && !filepath.IsAbs(env.cwd) && !projectRelativePath(env.cwd) {
		return nil, false
	}
	calls, ok := exactCommandReplacement(ctx, env.command, joinCommandCwd(activeDir, env.cwd), sessionScratch)
	if !ok {
		return nil, false
	}
	return carryCommandEnvelope(env, calls)
}

// carryCommandEnvelope transfers runner options or rejects an inexact translation.
func carryCommandEnvelope(env commandEnvelope, calls []ReplacementCall) ([]ReplacementCall, bool) {
	if !carryTimeout(env, calls) {
		return nil, false
	}
	for i := range calls {
		call := calls[i]
		if !carryCapability(env, call) || !carryCwd(env, call) {
			return nil, false
		}
	}
	return calls, true
}

// carryCapability accepts only authority represented by the receiving HTTP tool.
func carryCapability(env commandEnvelope, call ReplacementCall) bool {
	if len(env.capability) == 0 {
		return true
	}
	if call.Tool != "http_request" {
		return false
	}
	for name := range env.capability {
		if name != "loopback_connect" {
			return false
		}
	}
	call.Args["capability_request"] = maps.Clone(env.capability)
	return true
}

// carryTimeout applies a runner deadline to a matching replacement call.
func carryTimeout(env commandEnvelope, calls []ReplacementCall) bool {
	if env.timeoutMS <= 0 {
		return true
	}
	if len(calls) != 1 {
		return false
	}
	call := calls[0]
	if contract, ok := toolcontract.Lookup(call.Tool); ok && contract.BoundedInProcess {
		return true
	}
	minMS, maxMS := deadlineBounds(call.Tool)
	if minMS == 0 {
		return false
	}
	deadline, ok := boundedDeadline(call.Args["timeout_ms"], env.timeoutMS, minMS, maxMS)
	if !ok {
		return false
	}
	call.Args["timeout_ms"] = deadline
	return true
}

// deadlineBounds returns the schema bounds of a native tool that takes a
// deadline, and zero for one that does not.
func deadlineBounds(tool string) (minMS, maxMS int) {
	switch tool {
	case "http_request":
		return httpRequestMinTimeoutMS, httpRequestMaxTimeoutMS
	case "wait":
		return waitMinTimeoutMS, waitMaxTimeoutMS
	default:
		return 0, 0
	}
}

// boundedDeadline keeps the tighter declared bound without widening or shortening it.
func boundedDeadline(existing any, envelopeMS, minMS, maxMS int) (int, bool) {
	deadline := envelopeMS
	if own, ok := integerArg(existing); ok && own > 0 && own < deadline {
		deadline = own
	}
	if deadline < minMS {
		return 0, false
	}
	if deadline > maxMS {
		return 0, false
	}
	return deadline, true
}

// carryCwd resolves paths under the runner cwd; root-qualified paths require separate resolution.
func carryCwd(env commandEnvelope, call ReplacementCall) bool {
	cwd := strings.TrimSpace(env.cwd)
	if cwd == "" || cwd == "." {
		return true
	}
	if filepath.IsAbs(cwd) && replacementPathArgs[call.Tool].projectOnly {
		return false
	}
	if !filepath.IsAbs(cwd) && !projectRelativePath(cwd) {
		return false
	}
	switch call.Tool {
	case "grep":
		if _, ok := call.Args["path"]; !ok {
			call.Args["path"] = cwd
			return true
		}
	case "git_status", "git_diff", "git_log", "git_show", "git_blame", "git_checkout", "git_merge", "git_stash", "git_stash_list", "git_compare":
		return false // The changed cwd can select a different repository.

	}
	rewriteReplacementPaths(call, func(p string) string { return joinCommandCwd(cwd, p) })
	return true
}

// loopbackCapabilityFor requests access to the port named by a loopback URL.
func loopbackCapabilityFor(target string) (map[string]any, bool) {
	u, err := outboundhttp.NormalizeURL(target)
	if err != nil {
		return nil, false
	}
	if !egress.SyntacticLoopback(u.Hostname()) {
		return nil, false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, false
	}
	return map[string]any{"loopback_connect": map[string]any{"ports": []any{n}}}, true
}

func integerArg(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	default:
		return 0, false
	}
}
