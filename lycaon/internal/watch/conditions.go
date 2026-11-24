package watch

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	awaitstore "github.com/lycaon/lycaon/internal/await"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	"github.com/lycaon/lycaon/internal/projectroot"
)

var allWatchConditionKinds = map[string]bool{
	"next_worker_done":        true,
	"all_workers_idle":        true,
	"overlay_promote_pending": true,
	"worker_decision_needed":  true,
	"worker_failed":           true,
	"scan_done":               true,
	"process_done":            true,
	"process_output":          true,
	"http_ready":              true,
	"port_ready":              true,
	"file_change":             true,
	"git_ref":                 true,
	"timer":                   true,
}

var validCalendarWeekdays = map[string]bool{
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true,
	"friday": true, "saturday": true, "sunday": true,
}

func invalidField(field, msg string) error {
	return &ValidationError{Code: "WATCH_CONDITION_INVALID", Field: field, Fields: []string{field}, Message: msg}
}

func invalidDraft(msg string, fields ...string) error {
	primary := "standing"
	if len(fields) > 0 && fields[0] != "" {
		primary = fields[0]
	}
	return &ValidationError{Code: "WATCH_DRAFT_INVALID", Field: primary, Fields: fields, Message: msg}
}

func parseConditionsArg(raw any, now, effectiveExpiry time.Time, deps ToolDeps, profileID, sessionID string, roots []projectroot.RootRef) ([]awaitstore.Condition, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > MaxAlternatives {
		return nil, invalidField("conditions", "conditions must be an array of 1 to 8 objects")
	}
	allowed := allowedConditionsForCall(deps.ProfileConditions, profileID, len(nonOutsideRoots(roots)) == 0 && len(roots) > 0)
	out := make([]awaitstore.Condition, 0, len(items))
	for i, item := range items {
		prefix := fmt.Sprintf("conditions.%d", i)
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, invalidField(prefix, "each condition must be an object")
		}
		kind, ok := obj["kind"].(string)
		kind = strings.TrimSpace(kind)
		if !ok || kind == "" {
			return nil, invalidField(prefix+".kind", "condition kind is required")
		}
		if kind == ConditionWatchMatch || kind == "program_done" || !allWatchConditionKinds[kind] {
			return nil, invalidField(prefix+".kind", fmt.Sprintf("condition kind %q is not valid for watch", kind))
		}
		if allowed != nil && !allowed[kind] {
			return nil, notAllowedReject(kind, profileID, allowed)
		}
		cond, err := parseSingleWatchCondition(prefix, kind, obj, now, effectiveExpiry, deps, sessionID, roots)
		if err != nil {
			return nil, err
		}
		out = append(out, cond)
	}
	return out, nil
}

func allowedConditionsForCall(profiles map[string]map[string]bool, profileID string, rootless bool) map[string]bool {
	if len(profiles) == 0 {
		return nil
	}
	src := profiles[strings.TrimSpace(profileID)]
	out := make(map[string]bool, len(src))
	for k, v := range src {
		if !v {
			continue
		}
		if rootless && (k == "file_change" || k == "git_ref") {
			continue
		}
		out[k] = true
	}
	return out
}

func parseSingleWatchCondition(prefix, kind string, obj map[string]any, now, effectiveExpiry time.Time, deps ToolDeps, sessionID string, roots []projectroot.RootRef) (awaitstore.Condition, error) {
	if err := checkAllowedConditionKeys(prefix, kind, obj); err != nil {
		return awaitstore.Condition{}, err
	}
	switch kind {
	case "all_workers_idle", "overlay_promote_pending", "scan_done":
		return awaitstore.Condition{Kind: kind}, nil
	case "next_worker_done", "worker_decision_needed", "worker_failed":
		handles, err := parseOptionalHandles(prefix+".handles", obj["handles"], true)
		if err != nil {
			return awaitstore.Condition{}, err
		}
		if deps.WorkerKnown != nil {
			for _, h := range handles {
				if !deps.WorkerKnown(sessionID, h) {
					return awaitstore.Condition{}, invalidField(prefix+".handles", fmt.Sprintf("unknown worker %q", h))
				}
			}
		}
		return awaitstore.Condition{Kind: kind, Handles: handles}, nil
	case "process_done":
		handles, err := parseOptionalHandles(prefix+".handles", obj["handles"], true)
		if err != nil {
			return awaitstore.Condition{}, err
		}
		if deps.ProcessKnown != nil {
			for _, h := range handles {
				if !deps.ProcessKnown(sessionID, h) {
					return awaitstore.Condition{}, invalidField(prefix+".handles", fmt.Sprintf("unknown command handle %q", h))
				}
			}
		}
		return awaitstore.Condition{Kind: kind, Handles: handles}, nil
	case "process_output":
		return parseProcessOutputCondition(prefix, obj, deps, sessionID)
	case "http_ready":
		return parseHTTPWatchCondition(prefix, obj)
	case "port_ready":
		return parsePortWatchCondition(prefix, obj)
	case "file_change":
		return parseFileChangeCondition(prefix, obj, roots)
	case "git_ref":
		return parseGitRefCondition(prefix, obj, roots)
	case "timer":
		return parseTimerCondition(prefix, obj, now, effectiveExpiry)
	default:
		return awaitstore.Condition{}, invalidField(prefix+".kind", "unsupported condition kind")
	}
}

func checkAllowedConditionKeys(prefix, kind string, obj map[string]any) error {
	allowed := map[string]bool{"kind": true}
	switch kind {
	case "next_worker_done", "worker_decision_needed", "worker_failed", "process_done":
		allowed["handles"] = true
	case "process_output":
		allowed["handle"] = true
		allowed["cursor"] = true
	case "http_ready":
		for _, k := range []string{"url", "method", "status_min", "status_max", "port"} {
			allowed[k] = true
		}
	case "port_ready":
		allowed["host"] = true
		allowed["port"] = true
	case "file_change":
		for _, k := range []string{"root", "path", "glob", "include_own"} {
			allowed[k] = true
		}
	case "git_ref":
		allowed["root"] = true
		allowed["ref"] = true
	case "timer":
		for _, k := range []string{"delay_ms", "at", "calendar", "time_zone"} {
			allowed[k] = true
		}
	}
	for k := range obj {
		if !allowed[k] {
			return invalidField(prefix+"."+k, fmt.Sprintf("%s does not accept %s", kind, k))
		}
	}
	return nil
}

func parseOptionalHandles(field string, raw any, capAtEight bool) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || (capAtEight && len(items) > MaxAlternatives) {
		return nil, invalidField(field, "handles must be a non-empty array of up to 8 strings")
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		s, ok := item.(string)
		s = strings.TrimSpace(s)
		if !ok || s == "" {
			return nil, invalidField(field, "each handle must be a non-empty string")
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func parseProcessOutputCondition(prefix string, obj map[string]any, deps ToolDeps, sessionID string) (awaitstore.Condition, error) {
	handle, ok := obj["handle"].(string)
	handle = strings.TrimSpace(handle)
	if !ok || handle == "" {
		return awaitstore.Condition{}, invalidField(prefix+".handle", "process_output requires a command handle")
	}
	if deps.ProcessKnown != nil && !deps.ProcessKnown(sessionID, handle) {
		return awaitstore.Condition{}, invalidField(prefix+".handle", fmt.Sprintf("unknown command handle %q", handle))
	}
	cond := awaitstore.Condition{Kind: "process_output", Handle: handle}
	if rawCursor, present := obj["cursor"]; present {
		cursor, ok := strictInt64(rawCursor)
		if !ok || cursor < 0 {
			return awaitstore.Condition{}, invalidField(prefix+".cursor", "cursor must be a non-negative integer")
		}
		cond.Cursor = &cursor
	}
	return cond, nil
}

func parseHTTPWatchCondition(prefix string, obj map[string]any) (awaitstore.Condition, error) {
	rawURL, ok := obj["url"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return awaitstore.Condition{}, invalidField(prefix+".url", "http_ready requires url")
	}
	declared, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || declared.Fragment != "" {
		return awaitstore.Condition{}, invalidField(prefix+".url", "http_ready url is invalid or contains a fragment")
	}
	if portRaw, present := obj["port"]; present {
		portVal, ok := strictInt64(portRaw)
		if !ok || portVal <= 0 || portVal > 65535 {
			return awaitstore.Condition{}, invalidField(prefix+".port", "http_ready port must be within 1..65535")
		}
		if declaredPort := declared.Port(); declaredPort != "" {
			if declaredPort != strconv.FormatInt(portVal, 10) {
				return awaitstore.Condition{}, invalidField(prefix+".port", "http_ready port conflicts with url port")
			}
		} else {
			declared.Host = net.JoinHostPort(declared.Hostname(), strconv.FormatInt(portVal, 10))
			rawURL = declared.String()
		}
	}
	target, err := outboundhttp.NormalizeURL(rawURL)
	if err != nil {
		return awaitstore.Condition{}, invalidField(prefix+".url", err.Error())
	}
	method := "GET"
	if rawMethod, present := obj["method"]; present {
		m, ok := rawMethod.(string)
		m = strings.ToUpper(strings.TrimSpace(m))
		if !ok || (m != "GET" && m != "HEAD") {
			return awaitstore.Condition{}, invalidField(prefix+".method", "http_ready method must be GET or HEAD")
		}
		method = m
	}
	minCode, err := optionalStatusBound(prefix+".status_min", obj, "status_min", 200)
	if err != nil {
		return awaitstore.Condition{}, err
	}
	maxCode, err := optionalStatusBound(prefix+".status_max", obj, "status_max", 399)
	if err != nil {
		return awaitstore.Condition{}, err
	}
	if minCode > maxCode {
		return awaitstore.Condition{}, invalidField(prefix+".status_min", "status_min must not exceed status_max")
	}
	return awaitstore.Condition{Kind: "http_ready", URL: target.String(), Method: method, StatusMin: minCode, StatusMax: maxCode}, nil
}

func optionalStatusBound(field string, obj map[string]any, key string, fallback int) (int, error) {
	raw, present := obj[key]
	if !present {
		return fallback, nil
	}
	val, ok := strictInt64(raw)
	if !ok || val < 100 || val > 599 {
		return 0, invalidField(field, key+" must be an integer in 100..599")
	}
	return int(val), nil
}

func parsePortWatchCondition(prefix string, obj map[string]any) (awaitstore.Condition, error) {
	host := "localhost"
	if rawHost, present := obj["host"]; present {
		h, ok := rawHost.(string)
		h = strings.TrimSpace(h)
		if !ok || h == "" || (net.ParseIP(h) == nil && strings.ContainsAny(h, "\x00\r\n\t:/")) {
			return awaitstore.Condition{}, invalidField(prefix+".host", "port_ready host must be a hostname or IP without a port")
		}
		host = h
	}
	portVal, ok := strictInt64(obj["port"])
	if !ok || portVal < 1 || portVal > 65535 {
		return awaitstore.Condition{}, invalidField(prefix+".port", "port_ready requires a port in 1..65535")
	}
	return awaitstore.Condition{Kind: "port_ready", Host: host, Port: uint16(portVal)}, nil
}

func parseFileChangeCondition(prefix string, obj map[string]any, roots []projectroot.RootRef) (awaitstore.Condition, error) {
	rootLabel, _, err := resolveRootArg(prefix+".root", obj["root"], roots, false)
	if err != nil {
		return awaitstore.Condition{}, err
	}
	relPath, err := cleanRelativeWatchPath(prefix+".path", obj["path"], true)
	if err != nil {
		return awaitstore.Condition{}, err
	}
	cond := awaitstore.Condition{Kind: "file_change", Root: rootLabel, Path: relPath}
	if rawGlob, present := obj["glob"]; present {
		globStr, ok := rawGlob.(string)
		globStr = strings.TrimSpace(globStr)
		if !ok || globStr == "" || utf8.RuneCountInString(globStr) > 256 || strings.HasPrefix(globStr, "/") || strings.Contains(globStr, "..") {
			return awaitstore.Condition{}, invalidField(prefix+".glob", "glob must be a root-relative pattern of at most 256 characters")
		}
		cond.Glob = globStr
	}
	if rawOwn, present := obj["include_own"]; present {
		own, ok := rawOwn.(bool)
		if !ok {
			return awaitstore.Condition{}, invalidField(prefix+".include_own", "include_own must be a boolean")
		}
		cond.IncludeOwn = own
	}
	return cond, nil
}

func parseGitRefCondition(prefix string, obj map[string]any, roots []projectroot.RootRef) (awaitstore.Condition, error) {
	rootLabel, rootPath, err := resolveRootArg(prefix+".root", obj["root"], roots, true)
	if err != nil {
		return awaitstore.Condition{}, err
	}
	if rootPath != "" {
		if _, statErr := os.Stat(filepath.Join(rootPath, ".git")); statErr != nil {
			return awaitstore.Condition{}, invalidField(prefix+".root", fmt.Sprintf("root %q is not a Git repository or worktree", rootLabel))
		}
	}
	ref, ok := obj["ref"].(string)
	ref = strings.TrimSpace(ref)
	if !ok || !isValidFullGitRef(ref) {
		return awaitstore.Condition{}, invalidField(prefix+".ref", "git_ref requires a full ref name beginning with refs/")
	}
	return awaitstore.Condition{Kind: "git_ref", Root: rootLabel, Ref: ref}, nil
}

func isValidFullGitRef(ref string) bool {
	if !strings.HasPrefix(ref, "refs/") || len(ref) <= len("refs/") || strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".lock") {
		return false
	}
	if strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "@{") {
		return false
	}
	return !strings.ContainsAny(ref, " \t\r\n~^:?*[\\")
}

func parseTimerCondition(prefix string, obj map[string]any, now, effectiveExpiry time.Time) (awaitstore.Condition, error) {
	_, hasDelay := obj["delay_ms"]
	_, hasAt := obj["at"]
	_, hasCal := obj["calendar"]
	modes := 0
	for _, set := range []bool{hasDelay, hasAt, hasCal} {
		if set {
			modes++
		}
	}
	if modes != 1 {
		return awaitstore.Condition{}, invalidField(prefix, "timer requires exactly one of delay_ms, at, or calendar")
	}
	if hasDelay {
		if _, hasTZ := obj["time_zone"]; hasTZ {
			return awaitstore.Condition{}, invalidField(prefix+".time_zone", "time_zone is not accepted with delay_ms")
		}
		delay, ok := strictInt64(obj["delay_ms"])
		maxDelay := effectiveExpiry.Sub(now).Milliseconds()
		if !ok || delay < 1000 || delay > maxDelay {
			return awaitstore.Condition{}, invalidField(prefix+".delay_ms", "delay_ms must be between 1000 and remaining time before expires_at")
		}
		return awaitstore.Condition{Kind: "timer", DelayMS: delay}, nil
	}
	tzName, loc, err := resolveTimerLocation(prefix+".time_zone", obj["time_zone"])
	if err != nil {
		return awaitstore.Condition{}, err
	}
	if hasAt {
		atStr, ok := obj["at"].(string)
		atStr = strings.TrimSpace(atStr)
		parsedAt, parseErr := time.ParseInLocation("2006-01-02T15:04", atStr, loc)
		if !ok || parseErr != nil {
			return awaitstore.Condition{}, invalidField(prefix+".at", "timer.at must be local YYYY-MM-DDTHH:MM to the minute")
		}
		if !parsedAt.After(now) || !parsedAt.Before(effectiveExpiry) {
			return awaitstore.Condition{}, invalidField(prefix+".at", "timer.at must be in the future and before effective expires_at")
		}
		return awaitstore.Condition{Kind: "timer", At: atStr, TimeZone: tzName}, nil
	}
	calSpec, err := parseCalendarSpec(prefix+".calendar", obj["calendar"])
	if err != nil {
		return awaitstore.Condition{}, err
	}
	return awaitstore.Condition{Kind: "timer", Calendar: calSpec, TimeZone: tzName}, nil
}

func resolveTimerLocation(field string, raw any) (string, *time.Location, error) {
	if raw == nil {
		name := time.Local.String()
		if name == "" || name == "Local" {
			name = "UTC"
		}
		if loc, lErr := time.LoadLocation(name); lErr == nil {
			return name, loc, nil
		}
		return "UTC", time.UTC, nil
	}
	tz, ok := raw.(string)
	tz = strings.TrimSpace(tz)
	if !ok || tz == "" || strings.EqualFold(tz, "Local") {
		return "", nil, invalidField(field, "time_zone must be a valid IANA time zone name")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return "", nil, invalidField(field, fmt.Sprintf("unknown IANA time_zone %q", tz))
	}
	return tz, loc, nil
}

func parseCalendarSpec(prefix string, raw any) (*awaitstore.CalendarSpec, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, invalidField(prefix, "calendar must be an object with days and times")
	}
	for k := range obj {
		if k != "days" && k != "times" {
			return nil, invalidField(prefix+"."+k, "calendar accepts only days and times")
		}
	}
	days, err := parseCalendarDays(prefix+".days", obj["days"])
	if err != nil {
		return nil, err
	}
	times, err := parseCalendarTimes(prefix+".times", obj["times"])
	if err != nil {
		return nil, err
	}
	return &awaitstore.CalendarSpec{Days: days, Times: times}, nil
}

func parseCalendarDays(field string, raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > 7 {
		return nil, invalidField(field, "days must list 1 to 7 entries")
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		d, ok := item.(string)
		d = strings.ToLower(strings.TrimSpace(d))
		if !ok || d == "" {
			return nil, invalidField(field, "each day must be a string")
		}
		if d == "every_day" || d == "weekdays" {
			if len(items) != 1 {
				return nil, invalidField(field, d+" cannot be combined with other day entries")
			}
			return []string{d}, nil
		}
		if !validCalendarWeekdays[d] {
			return nil, invalidField(field, fmt.Sprintf("invalid weekday %q", d))
		}
		if seen[d] {
			return nil, invalidField(field, fmt.Sprintf("duplicate weekday %q", d))
		}
		seen[d] = true
		out = append(out, d)
	}
	return out, nil
}

func parseCalendarTimes(field string, raw any) ([]string, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 || len(items) > 8 {
		return nil, invalidField(field, "times must list 1 to 8 HH:MM entries")
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		tm, ok := item.(string)
		tm = strings.TrimSpace(tm)
		if !ok || len(tm) != 5 || tm[2] != ':' {
			return nil, invalidField(field, "each time must be HH:MM (00:00..23:59)")
		}
		hh, errH := strconv.Atoi(tm[:2])
		mm, errM := strconv.Atoi(tm[3:])
		if errH != nil || errM != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
			return nil, invalidField(field, "each time must be HH:MM (00:00..23:59)")
		}
		if seen[tm] {
			return nil, invalidField(field, fmt.Sprintf("duplicate time %q", tm))
		}
		seen[tm] = true
		out = append(out, tm)
	}
	sort.Strings(out)
	return out, nil
}

func parseStandingDraftArg(raw any, conditions []awaitstore.Condition, delivery DeliveryMode, explicitDelivery bool, isWorker, projectChat bool, projectID string, deps ToolDeps) (*StandingDraft, error) {
	if raw == nil {
		return nil, nil
	}
	if isWorker || !projectChat {
		return nil, invalidDraft("standing watch drafts are allowed only on coordinator turns in a project chat", "standing")
	}
	if explicitDelivery && delivery == DeliveryWake {
		return nil, invalidDraft("standing watch drafts force delivery=collect and cannot use delivery=wake", "standing", "delivery")
	}
	for i, cond := range conditions {
		if cond.Kind != "timer" && cond.Kind != "git_ref" && cond.Kind != "file_change" {
			return nil, invalidDraft("standing drafts allow only timer, git_ref, or file_change conditions", "standing", fmt.Sprintf("conditions.%d.kind", i))
		}
		if cond.Kind == "file_change" && cond.IncludeOwn {
			return nil, invalidDraft("standing file_change watches cannot set include_own", "standing", fmt.Sprintf("conditions.%d.include_own", i))
		}
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return nil, invalidDraft("standing must be an object", "standing")
	}
	for k := range obj {
		if k != "name" && k != "prompt" && k != "replaces" {
			return nil, invalidDraft("standing accepts only name, prompt, and replaces", "standing."+k)
		}
	}
	name, _ := obj["name"].(string)
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return nil, invalidDraft("standing.name must be 1 to 80 characters", "standing.name")
	}
	prompt, _ := obj["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	if n := utf8.RuneCountInString(prompt); n < 1 || n > 8000 {
		return nil, invalidDraft("standing.prompt must be 1 to 8000 characters", "standing.prompt")
	}
	draft := &StandingDraft{Name: name, Prompt: prompt}
	if rawReplaces, present := obj["replaces"]; present {
		rep, ok := rawReplaces.(string)
		rep = strings.TrimSpace(rep)
		if !ok || rep == "" {
			return nil, invalidDraft("standing.replaces must be a UUID string", "standing.replaces")
		}
		if _, err := uuid.Parse(rep); err != nil {
			return nil, invalidDraft("standing.replaces must be a valid UUID", "standing.replaces")
		}
		if deps.StandingWatchExists == nil || !deps.StandingWatchExists(projectID, rep) {
			return nil, invalidDraft("standing.replaces does not name a standing watch in this project", "standing.replaces")
		}
		draft.Replaces = rep
	}
	return draft, nil
}

func nonOutsideRoots(roots []projectroot.RootRef) []projectroot.RootRef {
	var out []projectroot.RootRef
	for _, r := range roots {
		if !r.Outside {
			out = append(out, r)
		}
	}
	return out
}

func resolveRootArg(field string, raw any, roots []projectroot.RootRef, _ bool) (string, string, error) {
	label, ok := raw.(string)
	label = strings.TrimPrefix(strings.TrimSpace(label), "@")
	if !ok || label == "" {
		return "", "", invalidField(field, "root label is required")
	}
	candidates := nonOutsideRoots(roots)
	if len(candidates) == 0 {
		return label, "", nil
	}
	for _, r := range candidates {
		cand := strings.TrimPrefix(strings.TrimSpace(r.Label), "@")
		if cand == label || strings.TrimSpace(r.ID) == label {
			if cand != "" {
				return cand, r.Path, nil
			}
			return label, r.Path, nil
		}
	}
	return "", "", invalidField(field, fmt.Sprintf("unauthorized or unknown root label %q", label))
}

func cleanRelativeWatchPath(field string, raw any, allowDot bool) (string, error) {
	p, ok := raw.(string)
	p = strings.TrimSpace(p)
	if !ok || p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return "", invalidField(field, "path must be a non-empty root-relative path")
	}
	cleaned := path.Clean(p)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || (!allowDot && cleaned == ".") {
		return "", invalidField(field, "path must stay within the declared root")
	}
	return cleaned, nil
}

func strictInt64(raw any) (int64, bool) {
	switch v := raw.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		if float64(int64(v)) != v {
			return 0, false
		}
		return int64(v), true
	default:
		return 0, false
	}
}
