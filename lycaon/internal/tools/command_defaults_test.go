package tools

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolschema"
)

func TestCommandMatcherRejectsUnknownSemanticsEvenAtDeclaredDefaults(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{
		"future_option": map[string]any{"type": "boolean", "default": false},
		"env":           map[string]any{"type": "object", "default": map[string]any{}},
	}}
	for _, tc := range []struct {
		name  string
		extra map[string]any
		match bool
	}{
		{"future default", map[string]any{"future_option": false}, false},
		{"empty environment", map[string]any{"env": map[string]any{}}, true},
		{"future effect", map[string]any{"future_option": true}, false},
		{"environment effect", map[string]any{"env": map[string]any{"GIT_INDEX_FILE": "another-index"}}, false},
		{"unknown false is not a default", map[string]any{"undeclared": false}, false},
		{"empty terminal capture still requests capture", map[string]any{"terminal_capture": map[string]any{}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"command": "git add a.go && git commit -m fix"}
			for key, value := range tc.extra {
				args[key] = value
			}
			normalized := commandArgsWithoutDefaults(args, schema)
			_, match := parseCommandEnvelope(normalized)
			if match != tc.match {
				t.Fatalf("eligible = %v, want %v: %#v", match, tc.match, normalized)
			}
			for key, value := range tc.extra {
				if !reflect.DeepEqual(args[key], value) {
					t.Errorf("matching changed execution argument %s", key)
				}
			}
		})
	}
}

func TestStockCommandDefaultsKeepGitRedirects(t *testing.T) {
	cfg, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	testutil.FailErr(t, "load tool schemas", err)
	for _, tool := range []string{"command", "verify"} {
		meta, ok := cfg.ToolMeta(tool)
		if !ok {
			t.Fatalf("%s schema missing", tool)
		}
		for field, value := range map[string]any{
			"verification": false, "background": false, "append": false,
			"allow_concurrent": false, "socks_proxy": false, "env": map[string]any{},
		} {
			if tool == "verify" && field == "verification" {
				continue
			}
			t.Run(tool+"/"+field, func(t *testing.T) {
				args := map[string]any{"command": "git commit --amend -m fix -- a.go", field: value}
				testutil.FailErr(t, "validate declared default", ValidateToolArgs(meta.ArgsSchema, args))
				envelope, ok := parseCommandEnvelope(commandArgsWithoutDefaults(args, meta.ArgsSchema))
				if !ok {
					t.Fatalf("%s default bypassed redirect", field)
				}
				calls, ok := exactCommandReplacement(t.Context(), envelope.command, t.TempDir(), "")
				if !ok || len(calls) != 1 || calls[0].Tool != "git_commit" || calls[0].Args["amend"] != true {
					t.Fatalf("amend replacement = %#v, ok=%v", calls, ok)
				}
			})
		}
	}
}

func TestChangedSchemaDefaultDoesNotEraseActiveRunnerSemantics(t *testing.T) {
	for name, active := range map[string]any{"background": true, "verification": true, "env": map[string]any{"TOKEN": "fixture"}} {
		args := map[string]any{"command": "curl https://example.test", name: active}
		schema := map[string]any{"properties": map[string]any{name: map[string]any{"default": active}}}
		if _, ok := parseCommandEnvelope(commandArgsWithoutDefaults(args, schema)); ok {
			t.Fatalf("new active default %s was silently removed", name)
		}
	}
}

func TestCommandReplacementChecksDeclaredWorkingDirectory(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	testutil.FailErr(t, "create command cwd", os.Mkdir(sub, 0o755))
	testutil.FailErr(t, "create root file", os.WriteFile(filepath.Join(root, "root-file"), []byte("needle"), 0o600))
	testutil.FailErr(t, "create cwd file", os.WriteFile(filepath.Join(sub, "cwd-file"), []byte("needle"), 0o600))
	testutil.FailErr(t, "create same-name directory", os.Mkdir(filepath.Join(sub, "root-file"), 0o755))
	for _, cwd := range []string{"sub", sub} {
		for _, program := range []string{"cat", "grep needle"} {
			env := commandEnvelope{command: program + " cwd-file", cwd: cwd}
			calls, ok := env.replacements(t.Context(), root, "")
			if !ok || len(calls) != 1 || calls[0].Args["path"] != filepath.ToSlash(filepath.Join(cwd, "cwd-file")) {
				t.Fatalf("cwd %q command %q: calls=%+v ok=%v", cwd, env.command, calls, ok)
			}
			env.command = program + " root-file"
			if calls, ok := env.replacements(t.Context(), root, ""); ok {
				t.Fatalf("cwd directory classified using active-root file: %+v", calls)
			}
		}
	}
}
