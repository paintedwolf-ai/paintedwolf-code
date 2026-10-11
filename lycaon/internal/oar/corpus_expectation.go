package oar

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/internal/oarcore"
)

func compareCorpusExpectation(expected map[string]any, res *PipelineResult, store *CounterStore, sessionID string, occ corpusOcc) string {
	if expected == nil {
		return ""
	}
	if want, present := expected["skipped_transforms"]; present {
		got := res.SkippedTransforms
		if got == nil {
			got = []oarcore.SkippedTransform{}
		}
		if !jsonDeepEqual(got, want) {
			return fmt.Sprintf("skipped_transforms = %v, want %v", got, want)
		}
	}
	gotDecision, gotCode, gotRule := "none", "", ""
	var gotCopy map[string]string
	var gotAdvisories []Advisory
	if res != nil && res.Decision != nil {
		gotDecision = string(res.Decision.Effect)
		gotCode = res.Decision.Code
		gotRule = res.Decision.Rule
		gotCopy = res.Decision.Copy
		gotAdvisories = res.Decision.Advisories
	}
	if want, ok := expected["decision"]; ok && strings.TrimSpace(fmt.Sprint(want)) != "" {
		if fmt.Sprint(want) != gotDecision {
			return fmt.Sprintf("decision = %s, want %s", gotDecision, want)
		}
	}
	if want, ok := expected["code"]; ok && strings.TrimSpace(fmt.Sprint(want)) != "" {
		if fmt.Sprint(want) != gotCode {
			return fmt.Sprintf("code = %q, want %q", gotCode, want)
		}
	}
	if want, ok := expected["rule"]; ok && strings.TrimSpace(fmt.Sprint(want)) != "" {
		if fmt.Sprint(want) != gotRule {
			return fmt.Sprintf("rule = %q, want %q", gotRule, want)
		}
	}
	if want, ok := expected["applied"]; ok {
		var got []string
		if res != nil {
			for _, e := range res.Trace.Entries {
				got = append(got, e.Rule+":"+string(e.Outcome))
			}
		}
		if strings.Join(got, ",") != strings.Join(asStringList(want), ",") {
			return fmt.Sprintf("applied = %v, want %v", got, asStringList(want))
		}
	}
	if want, ok := expected["on_fire"]; ok {
		var got []string
		if res != nil {
			for _, a := range res.AppliedOnFire {
				got = append(got, string(a))
			}
		}
		if strings.Join(got, ",") != strings.Join(asStringList(want), ",") {
			return fmt.Sprintf("on_fire = %v, want %v", got, asStringList(want))
		}
	}
	if want, ok := expected["events"]; ok {
		got := make([]map[string]any, 0)
		if res != nil {
			for _, event := range res.PublishedEvents {
				got = append(got, map[string]any{
					"rule": event.Rule, "anchor": event.Anchor, "effect": string(event.Effect),
				})
			}
		}
		if !jsonDeepEqual(got, want) {
			return fmt.Sprintf("events = %v, want %v", got, want)
		}
	}
	if want, ok := expected["transforms"]; ok {
		got := reportTransforms(nil)
		if res != nil {
			got = reportTransforms(res.Transforms)
		}
		if !jsonDeepEqual(got, want) {
			left, err := json.Marshal(got)
			if err != nil {
				return fmt.Sprintf("marshal actual transforms: %v", err)
			}
			right, err := json.Marshal(want)
			if err != nil {
				return fmt.Sprintf("marshal expected transforms: %v", err)
			}
			return fmt.Sprintf("transforms = %s, want %s", left, right)
		}
	}
	if want, ok := expected["content"]; ok {
		if occ.Content == nil {
			return "asserts expected.content without supplying input.content"
		}
		got := ""
		if res != nil {
			got = res.Content
		}
		if string([]rune(got)) != string([]rune(fmt.Sprint(want))) {
			return fmt.Sprintf("content = %q, want %q", got, want)
		}
	}
	if want, ok := expected["copy"]; ok {
		wantMap, ok := want.(map[string]any)
		if !ok {
			return "expected.copy is not an object"
		}
		if gotCopy == nil {
			gotCopy = map[string]string{}
		}
		for key, value := range wantMap {
			if gotCopy[key] != fmt.Sprint(value) {
				return fmt.Sprintf("copy.%s = %q, want %q", key, gotCopy[key], value)
			}
		}
	}
	if want, ok := expected["advisories"]; ok {
		if detail := compareAdvisories(gotAdvisories, want); detail != "" {
			return detail
		}
	}
	if want, ok := expected["counters"]; ok {
		wantMap, ok := want.(map[string]any)
		if !ok {
			return "expected.counters is not an object"
		}
		got := store.Report(sessionID)
		for key, wanted := range wantMap {
			wantedObj, ok := wanted.(map[string]any)
			if !ok {
				return fmt.Sprintf("expected.counters[%s] is not an object", key)
			}
			for name, value := range wantedObj {
				wantN := jsonvalue.Int(value)
				have := 0
				if row, ok := got[key]; ok {
					have = row[name]
				}
				if have != wantN {
					return fmt.Sprintf("counter %s/%s = %d, want %d", key, name, have, wantN)
				}
			}
		}
	}
	if want, ok := expected["error"]; ok && strings.TrimSpace(fmt.Sprint(want)) != "" {
		return fmt.Sprintf("expected load/config error %q, got success", want)
	}
	return ""
}

// compareAdvisories is [OAR-CONF-37]: length and order, and only the members
// the fixture named on each item.
func compareAdvisories(got []Advisory, want any) string {
	wantList, ok := want.([]any)
	if !ok {
		return "expected.advisories is not a list"
	}
	if got == nil {
		got = []Advisory{}
	}
	if len(got) != len(wantList) {
		return fmt.Sprintf("advisories length = %d, want %d", len(got), len(wantList))
	}
	for i, raw := range wantList {
		w, ok := raw.(map[string]any)
		if !ok {
			return fmt.Sprintf("expected.advisories[%d] is not an object", i)
		}
		g := got[i]
		if wantCode, present := w["code"]; present && g.Code != fmt.Sprint(wantCode) {
			return fmt.Sprintf("advisories[%d].code = %q, want %q", i, g.Code, wantCode)
		}
		if wantRule, present := w["rule"]; present && g.Rule != fmt.Sprint(wantRule) {
			return fmt.Sprintf("advisories[%d].rule = %q, want %q", i, g.Rule, wantRule)
		}
		if wantCopy, present := w["copy"]; present {
			wantMap, ok := wantCopy.(map[string]any)
			if !ok {
				return fmt.Sprintf("expected.advisories[%d].copy is not an object", i)
			}
			gotCopy := g.Copy
			if gotCopy == nil {
				gotCopy = map[string]string{}
			}
			for key, value := range wantMap {
				if gotCopy[key] != fmt.Sprint(value) {
					return fmt.Sprintf("advisories[%d].copy.%s = %q, want %q", i, key, gotCopy[key], value)
				}
			}
		}
	}
	return ""
}

func matchExpectedError(expected map[string]any, err error) bool {
	if expected == nil || err == nil {
		return false
	}
	want, ok := expected["error"].(string)
	if !ok || strings.TrimSpace(want) == "" {
		return false
	}
	return strings.Contains(err.Error(), want) || err.Error() == want
}

func asStringList(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}

func jsonDeepEqual(a, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	var la, lb any
	if json.Unmarshal(left, &la) != nil || json.Unmarshal(right, &lb) != nil {
		return false
	}
	return reflect.DeepEqual(la, lb)
}
