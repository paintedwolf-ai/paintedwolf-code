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
			if got := Names(tc.text, []string{"read", "write"}); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("names=%v want %v", got, tc.want)
			}
		})
	}
	if got := Names(`{"tool":"read","args":{}}`, nil); got != nil {
		t.Fatalf("unoffered names=%v", got)
	}
}
