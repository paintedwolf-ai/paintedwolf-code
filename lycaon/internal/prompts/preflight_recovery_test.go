package prompts

import "testing"

func TestPreflightKeepsStaticAdviceAndDefersRuntimeTemplates(t *testing.T) {
	hints := map[string]hintCodeRow{}
	for code, body := range map[string]string{
		"STATIC":  "Put environment variables in env.",
		"VALUE":   "Use {{ suggested_tool }} next.",
		"BRANCH":  "{% if capture %}Run one program.{% else %}Use a pipeline.{% endif %}",
		"COMMENT": "{# internal note #}Run one program.",
	} {
		hints[code] = hintCodeRow{
			Emit: "guard:command_surface", Category: "recoverable",
			Tools: []string{"command"}, Instead: body,
		}
	}
	rows := recoverableRejectCodes(hints, []string{"command"})
	if len(rows) != 1 || rows[0].Code != "STATIC" || rows[0].BranchInstruction != hints["STATIC"].Instead {
		t.Fatalf("preflight advice = %#v, want only the static instruction", rows)
	}
	if hints["BRANCH"].Instead == "" || hints["VALUE"].Instead == "" {
		t.Fatal("preflight projection changed the runtime recovery source")
	}
}
