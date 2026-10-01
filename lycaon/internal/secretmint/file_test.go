package secretmint

import (
	"strings"
	"testing"
)

func TestInspectComposeEnvAssignmentHits(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("write", map[string]any{
		"path": "compose.yaml",
		"content": `services:
  db:
    environment:
      MYSQL_PASSWORD=password
`,
	})
	if !hasAssignment(hits, "MYSQL_PASSWORD") {
		t.Fatalf("compose env assignment must hit: assignments=%v", assignments(hits))
	}
}

func TestInspectYAMLPasswordScalarHits(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("write", map[string]any{
		"content": "auth:\n  password: hunter2\n",
	})
	if !hasAssignment(hits, "password") {
		t.Fatalf("YAML password scalar must hit: assignments=%v", assignments(hits))
	}
}

func TestInspectJSONPasswordMemberHits(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("write", map[string]any{
		"content": `{"password": "letmein"}`,
	})
	if !hasAssignment(hits, "password") {
		t.Fatalf("JSON password member must hit: assignments=%v", assignments(hits))
	}
}

func TestInspectCommentLineSilent(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("write", map[string]any{
		"content": "# MYSQL_PASSWORD=password\n// password: hunter2\nAPP_ENV=dev\n",
	})
	if len(hits) != 0 {
		t.Fatalf("comment assignment must stay silent: assignments=%v", assignments(hits))
	}
}

func TestInspectReadmeProseSilent(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("write", map[string]any{
		"content": "The default password is hunter2 in the tutorial.\n",
	})
	if len(hits) != 0 {
		t.Fatalf("README prose must stay silent: assignments=%v", assignments(hits))
	}
}

func TestInspectPasswordPolicyKeySilent(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("write", map[string]any{
		"content": "password_policy: strict\n",
	})
	if len(hits) != 0 {
		t.Fatalf("password_policy must not match password: assignments=%v", assignments(hits))
	}
}

func TestInspectPublishesUnlistedLiteral(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("write", map[string]any{
		"content": "MYSQL_PASSWORD=unique-lab-secret-not-listed\n",
	})
	if len(hits) != 1 || hits[0].Measurements().Listed {
		t.Fatalf("unlisted literal must remain observable: %#v", hits)
	}
}

func TestInspectFileScanCap(t *testing.T) {
	ins := testInspector(t)
	prefix := strings.Repeat("x: y\n", 8)
	oversize := strings.Repeat("n", fileScanByteCap) + "\nMYSQL_PASSWORD=password\n"
	hits := ins.Inspect("write", map[string]any{"content": oversize})
	if len(hits) != 0 {
		t.Fatalf("assignment past byte cap must be skipped: assignments=%v", assignments(hits))
	}
	within := prefix + "MYSQL_PASSWORD=password\n"
	hits = ins.Inspect("write", map[string]any{"content": within})
	if !hasAssignment(hits, "MYSQL_PASSWORD") {
		t.Fatalf("assignment within cap must hit: assignments=%v", assignments(hits))
	}
	var b strings.Builder
	for i := 0; i < fileScanLineCap+1; i++ {
		b.WriteString("APP_ENV=dev\n")
	}
	b.WriteString("MYSQL_PASSWORD=password\n")
	hits = ins.Inspect("write", map[string]any{"content": b.String()})
	if len(hits) != 0 {
		t.Fatalf("assignment past line cap must be skipped: assignments=%v", assignments(hits))
	}
}

func TestInspectEditUsesNewStringOnly(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("edit", map[string]any{
		"old_string": "MYSQL_PASSWORD=password",
		"new_string": "APP_ENV=dev",
	})
	if len(hits) != 0 {
		t.Fatalf("old_string must not be inspected: assignments=%v", assignments(hits))
	}
	hits = ins.Inspect("edit", map[string]any{
		"old_string": "APP_ENV=dev",
		"new_string": "MYSQL_PASSWORD=password",
	})
	if !hasAssignment(hits, "MYSQL_PASSWORD") {
		t.Fatalf("new_string mint must hit: assignments=%v", assignments(hits))
	}
}

func TestInspectReplaceLinesUsesNewContentOnly(t *testing.T) {
	ins := testInspector(t)
	hits := ins.Inspect("replace_lines", map[string]any{
		"new_content": "password: hunter2\n",
	})
	if !hasAssignment(hits, "password") {
		t.Fatalf("replace_lines new_content must hit: assignments=%v", assignments(hits))
	}
}

// Each content surface selects its own argument to scan.
func TestInspectCoversEveryContentAuthoringTool(t *testing.T) {
	ins := testInspector(t)
	for tool, arg := range map[string]string{
		"write":         "content",
		"edit":          "new_string",
		"replace_lines": "new_content",
		"code_rewrite":  "rewrite",
	} {
		t.Run(tool, func(t *testing.T) {
			hits := ins.Inspect(tool, map[string]any{
				"path": "conf.yaml",
				arg:    "auth:\n  password: hunter2\n",
			})
			if !hasAssignment(hits, "password") {
				t.Fatalf("%s must scan its %s argument: assignments=%v", tool, arg, assignments(hits))
			}
		})
	}
}

// A tool that authors nothing has no content argument to scan.
func TestInspectSkipsNonAuthoringTools(t *testing.T) {
	ins := testInspector(t)
	for _, tool := range []string{"copy", "move", "mkdir", "read"} {
		if hits := ins.Inspect(tool, map[string]any{"content": "password: hunter2"}); len(hits) != 0 {
			t.Fatalf("%s authors nothing but produced %v", tool, assignments(hits))
		}
	}
}
