package actionenvelope

import (
	"reflect"
	"strings"
	"testing"
)

func TestLeadingActions(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"dollar wrapper", `${"tool_calls":[{"name":"read","arguments":{}}]}`, []string{"read"}},
		{"tag", `<tool_call>{"name":"read","arguments":{}}</tool_call>`, []string{"read"}},
		{"marker", `[TOOL_CALLS][{"name":"read","arguments":{}}]`, []string{"read"}},
		{"fenced tag", "```\n<tool_call>{\"name\":\"read\",\"arguments\":{}}</tool_call>\n```", nil},
		{"observed", `{"tool_name":"read","tool_args":{"path":"x"}}`, []string{"read"}},
		{"trailing explanation", ` {"name":"read","arguments":{}} Now I will inspect it.`, []string{"read"}},
		{"parameters", `{"name":"read","parameters":"{}","id":"1"}`, []string{"read"}},
		{"api args", `{"name":"read","args":null}`, []string{"read"}},
		{"tool args", `{"tool":"read","args":"malformed"}`, []string{"read"}},
		{"native wrapper", `{"tool_calls":[{"id":"1","type":"function","function":{"name":"read","arguments":"{}"}}]}`, []string{"read"}},
		{"batch", `[{"tool_name":"read","tool_args":{}},{"tool":"write","args":{}},{"name":"read","arguments":{}}]`, []string{"read", "write"}},
		{"one offered", `[{"tool":"unknown","args":{}},{"tool":"read","args":{}}]`, []string{"read"}},
		{"fenced", "```json\n{\"tool_name\":\"read\",\"tool_args\":{}}\n```", nil},
		{"explained", `The log contained {"tool_name":"read","tool_args":{}}`, nil},
		{"unknown", `{"tool_name":"missing","tool_args":{}}`, nil},
		{"ordinary data", `{"name":"read","description":"a tool"}`, nil},
		{"nested data", `{"data":{"tool":"read","args":{}}}`, nil},
		{"argument data", `{"tool":"unknown","args":{"tool":"read","args":{}}}`, nil},
		{"case sensitive keys", `{"TOOL_NAME":"read","TOOL_ARGS":{}}`, nil},
		{"case sensitive name", `{"tool":"Read","args":{}}`, nil},
		{"truncated", `{"tool_name":"read","tool_args":`, nil},
		{"scalar", `"read"`, nil},
		{"empty", "", nil},
		{"bounded", `{"tool":"read","args":"` + strings.Repeat("x", 65536) + `"}`, nil},
		{"xml function tag", "<tool_call>\n<function=write>\n<parameter=path>\nindex.html\n</parameter>\n</function>\n</tool_call>", []string{"write"}},
		{"prose before xml function tag", "Great — we have the full picture. Let's create the files now.\n\n<tool_call>\n<function=write>\n<parameter=content>\n<!DOCTYPE html>\n</parameter>\n<parameter=path>\nindex.html\n</parameter>\n</function>\n</tool_call>", []string{"write"}},
		{"prose before json tool_call tag", "Let me read the file first.\n\n<tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"main.go\"}}</tool_call>", []string{"read"}},
		{"prose before mistral marker", "Let me read the file first.\n[TOOL_CALLS][{\"name\":\"read\",\"arguments\":{\"path\":\"main.go\"}}]", []string{"read"}},
		{"unclosed xml tool_call tag", "Creating the file now.\n<tool_call>\n<function=write>\n<parameter=path>\nindex.html\n</parameter>", []string{"write"}},
		{"deferred tool in explicit tag", `<tool_call>{"name":"edit","arguments":{"path":"main.go"}}</tool_call>`, []string{"edit"}},
		{"deferred tool in xml tag", "I'll update the file now.\n<tool_call>\n<function=edit>\n<parameter=path>\nmain.go\n</parameter>\n</function>\n</tool_call>", []string{"edit"}},
		{"fenced xml tag", "```\n<tool_call>\n<function=write>\n<parameter=path>\nindex.html\n</parameter>\n</function>\n</tool_call>\n```", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(tc.text, []Tool{{Name: "read"}, {Name: "write"}}, nil)
			if !reflect.DeepEqual(got.Tools, tc.want) || got.Fenced {
				t.Fatalf("observation=%+v want tools %v", got, tc.want)
			}
		})
	}
	if got := Detect(`{"tool":"read","args":{}}`, nil, nil); len(got.Tools) != 0 {
		t.Fatalf("unoffered names=%v", got.Tools)
	}
}

func TestToolNamedFences(t *testing.T) {
	command := Tool{Name: "command", ArgsSchema: map[string]any{
		"properties": map[string]any{"command": map[string]any{}, "timeout_ms": map[string]any{}},
		"required":   []any{"command"},
	}}
	wait := Tool{Name: "wait", ArgsSchema: map[string]any{"properties": map[string]any{"seconds": map[string]any{}}}}
	bare := Tool{Name: "recall"}
	offered := []Tool{command, wait, bare}
	unoffered := []string{"task", "write"}
	call := `{"command": "find . -maxdepth 2 -type d"}`
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"offered arguments", "```command\n" + call + "\n```", []string{"command"}},
		{"prose around", "I will start by exploring.\n\n```command\n" + call + "\n```\nThen I will report.", []string{"command"}},
		{"tilde fence", "~~~command\n" + call + "\n~~~", []string{"command"}},
		{"longer fence", "````command\n" + call + "\n````", []string{"command"}},
		{"indented fence", "1. Explore\n    ```command\n    " + call + "\n    ```", []string{"command"}},
		{"info attributes", "```command title=x\n" + call + "\n```", []string{"command"}},
		{"unclosed fence", "```command\n" + call, []string{"command"}},
		{"argument batch", "```command\n[" + call + "," + call + "]\n```", []string{"command"}},
		{"several fences", "```command\n" + call + "\n```\n```wait\n{\"seconds\": 1}\n```", []string{"command", "wait"}},
		{"empty arguments without required", "```wait\n{}\n```", []string{"wait"}},
		{"offered without declared properties", "```recall\n{\"query\": \"x\"}\n```", []string{"recall"}},
		{"unoffered arguments", "```task\n{\"prompt\": \"x\"}\n```", []string{"task"}},
		{"wrapper in tool fence", "```command\n{\"name\":\"write\",\"arguments\":{}}\n```", []string{"write"}},
		{"empty arguments with required", "```command\n{}\n```", nil},
		{"undeclared key", "```command\n{\"cmd\": \"ls\"}\n```", nil},
		{"batch with undeclared key", "```command\n[" + call + ",{\"cmd\": \"ls\"}]\n```", nil},
		{"empty batch", "```command\n[]\n```", nil},
		{"transcript", "```command\n$ ls\nREADME.md\n```", nil},
		{"trailing text after arguments", "```command\n" + call + "\nREADME.md\n```", nil},
		{"unoffered empty arguments", "```task\n{}\n```", nil},
		{"offered empty without properties", "```recall\n{}\n```", nil},
		{"oversized body", "```command\n{\"command\": \"" + strings.Repeat("x", 65536) + "\"}\n```", nil},
		{"neutral json", "```json\n" + call + "\n```", nil},
		{"neutral yaml", "```yaml\ncommand: ls\n```", nil},
		{"untagged", "```\n" + call + "\n```", nil},
		{"unknown name", "```deploy\n" + call + "\n```", nil},
		{"case sensitive name", "```Command\n" + call + "\n```", nil},
		{"inline code span", "Use ```command``` like " + call, nil},
		{"shorter closing run", "````command\n" + call + "\n```\nstill inside", nil},
		{"tool fence inside neutral fence", "````markdown\n```command\n" + call + "\n```\n````", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(tc.text, offered, unoffered)
			if !reflect.DeepEqual(got.Tools, tc.want) || got.Fenced != (len(tc.want) > 0) {
				t.Fatalf("observation=%+v want tools %v", got, tc.want)
			}
		})
	}
}

func TestFenceInsideToolCallPayloadIsPayload(t *testing.T) {
	text := "<tool_call>\n<function=write>\n<parameter=content>\n```command\n{\"command\": \"ls\"}\n```\n</parameter>\n</function>\n</tool_call>"
	got := Detect(text, []Tool{{Name: "command"}}, []string{"write"})
	if !reflect.DeepEqual(got.Tools, []string{"write"}) || got.Fenced {
		t.Fatalf("observation=%+v", got)
	}
}
