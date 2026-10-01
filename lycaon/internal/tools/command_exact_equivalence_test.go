package tools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestExactCommandReplacementBuildsStructuredCalls(t *testing.T) {
	cases := []struct {
		command string
		want    []ReplacementCall
	}{
		{"rm /tmp/stale.txt", []ReplacementCall{{Tool: "delete", Args: map[string]any{"paths": []any{"/tmp/stale.txt"}, "files_only": true}}}},
		{"sleep 1.5", []ReplacementCall{{Tool: "wait", Args: map[string]any{"timeout_ms": 1500, "reason": "waiting for a timer"}}}},
		{"git status", []ReplacementCall{{Tool: "git_status", Args: map[string]any{}}}},
		{"git status --short", []ReplacementCall{{Tool: "git_status", Args: map[string]any{}}}},
		{"git status --porcelain", []ReplacementCall{{Tool: "git_status", Args: map[string]any{}}}},
		{"git status --porcelain=v1 --untracked-files=all", []ReplacementCall{{Tool: "git_status", Args: map[string]any{}}}},
		{"git status --porcelain=v2 -z --branch", []ReplacementCall{{Tool: "git_status", Args: map[string]any{}}}},
		{"git status -sb", []ReplacementCall{{Tool: "git_status", Args: map[string]any{}}}},
		{"git status -suall", []ReplacementCall{{Tool: "git_status", Args: map[string]any{}}}},
		{"git status -s -- src docs/README.md", []ReplacementCall{{Tool: "git_status", Args: map[string]any{"paths": []any{"src", "docs/README.md"}}}}},
		{"git status src", []ReplacementCall{{Tool: "git_status", Args: map[string]any{"paths": []any{"src"}}}}},
		{"git diff --cached -- src/main.go", []ReplacementCall{{Tool: "git_diff", Args: map[string]any{"staged": true, "paths": []any{"src/main.go"}}}}},
		{"git diff --stat", []ReplacementCall{{Tool: "git_diff", Args: map[string]any{"stat": true}}}},
		{"git diff --numstat -- src", []ReplacementCall{{Tool: "git_diff", Args: map[string]any{"stat": true, "paths": []any{"src"}}}}},
		{"git diff main", []ReplacementCall{{Tool: "git_diff", Args: map[string]any{"base_ref": "main"}}}},
		{"git diff HEAD~1 -- src/main.go", []ReplacementCall{{Tool: "git_diff", Args: map[string]any{"base_ref": "HEAD~1", "paths": []any{"src/main.go"}}}}},
		{"git show HEAD:src/main.go", []ReplacementCall{{Tool: "git_show", Args: map[string]any{"ref": "HEAD", "path": "src/main.go"}}}},
		{"git blame src/main.go", []ReplacementCall{{Tool: "git_blame", Args: map[string]any{"path": "src/main.go"}}}},
		{"git branch", []ReplacementCall{{Tool: "git_branches", Args: map[string]any{}}}},
		{"git rev-parse HEAD main", []ReplacementCall{{Tool: "git_ref", Args: map[string]any{"refs": []any{"HEAD", "main"}}}}},
		{"git log -n 20 -- src/main.go", []ReplacementCall{{Tool: "git_log", Args: map[string]any{"limit": 20, "path": "src/main.go"}}}},
		{"git restore --staged --worktree --source=HEAD -- src/main.go", []ReplacementCall{{Tool: "git_restore", Args: map[string]any{
			"staged": true, "worktree": true, "source": "HEAD", "paths": []any{"src/main.go"},
		}}}},
		{"git add src/a.go README.md && git commit -m 'Update docs'", []ReplacementCall{{Tool: "git_commit", Args: map[string]any{
			"message": "Update docs", "paths": []any{"src/a.go", "README.md"},
		}}}},
		{"git commit --amend -m 'Complete change' -- src/a.go new_test.go", []ReplacementCall{{Tool: "git_commit", Args: map[string]any{
			"message": "Complete change", "paths": []any{"src/a.go", "new_test.go"}, "amend": true,
		}}}},
		{"git add -- new_test.go && git commit --amend -m 'Complete change' -- src/a.go new_test.go", []ReplacementCall{{Tool: "git_commit", Args: map[string]any{
			"message": "Complete change", "paths": []any{"src/a.go", "new_test.go"}, "amend": true,
		}}}},
		{"git commit --message 'Complete change' --only --amend -- -dash.txt", []ReplacementCall{{Tool: "git_commit", Args: map[string]any{
			"message": "Complete change", "paths": []any{"-dash.txt"}, "amend": true,
		}}}},
		{"unzip archive.zip -d extracted", []ReplacementCall{{Tool: "extract_archive", Args: map[string]any{"path": "archive.zip", "dest": "extracted"}}}},
		{"diff before.go after.go", []ReplacementCall{{Tool: "diff", Args: map[string]any{"path_a": "before.go", "path_b": "after.go"}}}},
		{"rm -- -stale.go", []ReplacementCall{{Tool: "delete", Args: map[string]any{"paths": []any{"-stale.go"}, "files_only": true}}}},
		{"mkdir -p internal/new/sub", []ReplacementCall{{Tool: "mkdir", Args: map[string]any{"paths": []any{"internal/new/sub"}}}}},
		{"ls -la", []ReplacementCall{{Tool: "list_dir", Args: map[string]any{"path": ".", "include_hidden": true, "max_depth": 1}}}},
		{"git show --stat HEAD", []ReplacementCall{{Tool: "git_show", Args: map[string]any{"stat": true, "ref": "HEAD"}}}},
		{"git show --stat HEAD -- src/main.go", []ReplacementCall{{Tool: "git_show", Args: map[string]any{"stat": true, "ref": "HEAD", "path": "src/main.go"}}}},
		{"git log --all -n 20", []ReplacementCall{{Tool: "git_log", Args: map[string]any{"all": true, "limit": 20}}}},
		{"git status --ignored lycaon.db", []ReplacementCall{{Tool: "git_status", Args: map[string]any{"ignored": true, "paths": []any{"lycaon.db"}}}}},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got, ok := exactCommandReplacement(t.Context(), tc.command, t.TempDir(), "")
			if !ok || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("replacement = %#v ok=%v, want %#v", got, ok, tc.want)
			}
		})
	}
}

func TestExactCommandReplacementBuildsHTTPCalls(t *testing.T) {
	cases := []struct {
		command string
		want    []ReplacementCall
	}{
		{"curl https://example.test/data -o /tmp/data.json", []ReplacementCall{{Tool: "http_request", Args: map[string]any{"url": "https://example.test/data", "method": "GET", "response_path": "/tmp/data.json"}}}},
		{"curl --data-binary @/tmp/body.json https://example.test/data", []ReplacementCall{{Tool: "http_request", Args: map[string]any{"url": "https://example.test/data", "method": "POST", "body_path": "/tmp/body.json", "headers": []any{map[string]any{"name": "Content-Type", "value": "application/x-www-form-urlencoded"}}}}}},
		{"curl -sS -H 'X-Test: yes' https://example.test/v1", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/v1", "method": "GET", "headers": []any{map[string]any{"name": "X-Test", "value": "yes"}},
		}}}},
		{"curl -X POST -H 'Content-Type: application/json' -d '{\"ok\":true}' https://api.example.test/v1", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://api.example.test/v1", "method": "POST", "headers": []any{map[string]any{"name": "Content-Type", "value": "application/json"}},
			"body_text": "{\"ok\":true}",
		}}}},
		{"curl -sS -o /dev/null -w '%{http_code}' https://example.test/health", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/health", "method": "GET", "response_body": "discard",
		}}}},
		{"curl --max-time 1.5 https://example.test/health", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/health", "method": "GET", "timeout_ms": 1500,
		}}}},
		{"curl -d 'a=1' https://example.test/form", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/form", "method": "POST", "body_text": "a=1",
			"headers": []any{map[string]any{"name": "Content-Type", "value": "application/x-www-form-urlencoded"}},
		}}}},
		{"curl --data a=1 -d b=two --url https://example.test/form", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/form", "method": "POST", "body_text": "a=1&b=two",
			"headers": []any{map[string]any{"name": "Content-Type", "value": "application/x-www-form-urlencoded"}},
		}}}},
		{"curl --data-binary @payload.json https://example.test/upload", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/upload", "method": "POST", "body_path": "payload.json",
			"headers": []any{map[string]any{"name": "Content-Type", "value": "application/x-www-form-urlencoded"}},
		}}}},
		{"curl --json '{\"ok\":true}' https://api.example.test/v1", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://api.example.test/v1", "method": "POST", "body_text": "{\"ok\":true}",
			"headers": []any{
				map[string]any{"name": "Content-Type", "value": "application/json"},
				map[string]any{"name": "Accept", "value": "application/json"},
			},
		}}}},
		{"curl -sSL https://example.test/latest", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/latest", "method": "GET", "redirects": "safe",
		}}}},
		{"curl -sS -i -v https://example.test/v1", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/v1", "method": "GET",
		}}}},
		{"curl --unix-socket /var/run/docker.sock http://localhost/v1.41/info", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "http://localhost/v1.41/info", "method": "GET", "unix_socket": "/var/run/docker.sock",
		}}}},
		{"curl -sS -A agent/1.0 -e https://example.test/ https://example.test/v1", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/v1", "method": "GET", "headers": []any{
				map[string]any{"name": "User-Agent", "value": "agent/1.0"},
				map[string]any{"name": "Referer", "value": "https://example.test/"},
			},
		}}}},
		{"curl -m 2 https://example.test/health", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/health", "method": "GET", "timeout_ms": 2000,
		}}}},
		{"curl -sS -o /dev/null -w 'status=%{http_code}\\n' https://example.test/health", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/health", "method": "GET", "response_body": "discard",
		}}}},
		{"curl -sS https://example.test/tree.json -o tmp/tree.json", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/tree.json", "method": "GET", "response_path": "tmp/tree.json",
		}}}},
		{"curl -sS -O https://example.test/assets/logo.svg", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/assets/logo.svg", "method": "GET", "response_path": "logo.svg",
		}}}},
		{"curl -sS http://localhost:5555/api/v1/health", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "http://localhost:5555/api/v1/health", "method": "GET",
			"capability_request": map[string]any{"loopback_connect": map[string]any{"ports": []any{5555}}},
		}}}},
		{"curl -sS -o /dev/null -w %{http_code} http://127.0.0.1:5555/; echo; curl -sS -o /dev/null -w %{http_code} http://127.0.0.1:5555/api", []ReplacementCall{
			{Tool: "http_request", Args: map[string]any{
				"url": "http://127.0.0.1:5555/", "method": "GET", "response_body": "discard",
				"capability_request": map[string]any{"loopback_connect": map[string]any{"ports": []any{5555}}},
			}},
			{Tool: "http_request", Args: map[string]any{
				"url": "http://127.0.0.1:5555/api", "method": "GET", "response_body": "discard",
				"capability_request": map[string]any{"loopback_connect": map[string]any{"ports": []any{5555}}},
			}},
		}},
		{"curl -sS -c jar/registry.txt -X POST -d user=admin http://localhost:5555/api/v1/user/login", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "http://localhost:5555/api/v1/user/login", "method": "POST", "body_text": "user=admin", "cookie_jar": "registry",
			"headers":            []any{map[string]any{"name": "Content-Type", "value": "application/x-www-form-urlencoded"}},
			"capability_request": map[string]any{"loopback_connect": map[string]any{"ports": []any{5555}}},
		}}}},
		{"curl -sS -b jar/registry.txt http://localhost:5555/api/v1/user", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "http://localhost:5555/api/v1/user", "method": "GET", "cookie_jar": "registry",
			"capability_request": map[string]any{"loopback_connect": map[string]any{"ports": []any{5555}}},
		}}}},
		{"curl -sS -b 'session=abc' https://example.test/me", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/me", "method": "GET", "headers": []any{map[string]any{"name": "Cookie", "value": "session=abc"}},
		}}}},
		{"curl -sS -u admin:secret https://example.test/api", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/api", "method": "GET",
			"auth": map[string]any{"scheme": "basic", "username": "admin", "password": "secret"},
		}}}},
		{"curl -sS -F version=1.2.3 -F 'package=@dist/app.crate;type=application/gzip' https://example.test/upload", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/upload", "method": "POST", "form": []any{
				map[string]any{"name": "version", "value": "1.2.3"},
				map[string]any{"name": "package", "path": "dist/app.crate", "content_type": "application/gzip"},
			},
		}}}},
		{"curl -sS --data-urlencode 'q=a b&c' --data-urlencode plain https://example.test/search", []ReplacementCall{{Tool: "http_request", Args: map[string]any{
			"url": "https://example.test/search", "method": "POST", "body_text": "q=a+b%26c&plain",
			"headers": []any{map[string]any{"name": "Content-Type", "value": "application/x-www-form-urlencoded"}},
		}}}},
		{"curl -sS https://example.test/a && curl -sS https://example.test/b", []ReplacementCall{
			{Tool: "http_request", Args: map[string]any{"url": "https://example.test/a", "method": "GET"}},
			{Tool: "http_request", Args: map[string]any{"url": "https://example.test/b", "method": "GET"}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got, ok := exactCommandReplacement(t.Context(), tc.command, t.TempDir(), "")
			if !ok || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("replacement = %#v ok=%v, want %#v", got, ok, tc.want)
			}
		})
	}
}

func TestExactCommandReplacementBuildsSearchCalls(t *testing.T) {
	cases := []struct {
		command string
		want    []ReplacementCall
	}{
		{"rg -g '*.go' needle src", []ReplacementCall{{Tool: "grep", Args: map[string]any{"pattern": "needle", "path": "src", "path_glob": "*.go", "structural": false, "include_hidden": false}}}},
		{"grep -r -i needle src", []ReplacementCall{{Tool: "grep", Args: map[string]any{"pattern": "needle", "path": "src", "case_insensitive": true, "structural": false, "include_hidden": true}}}},
		{"find src -maxdepth 3 -name '*.go' -type f", []ReplacementCall{{Tool: "find", Args: map[string]any{
			"path": "src", "max_depth": 3, "name_glob": "*.go", "type": "file",
		}}}},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got, ok := exactCommandReplacement(t.Context(), tc.command, t.TempDir(), "")
			if !ok || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("replacement = %#v ok=%v, want %#v", got, ok, tc.want)
			}
		})
	}
}

func TestExactReplacementRequiresCallableNativeHandler(t *testing.T) {
	executor := &DefaultToolExecutor{}
	if reject := executor.exactCommandReplacementReject(
		context.Background(), "command", "implement", map[string]any{"command": "sleep 2"}, ToolContext{},
	); reject != nil {
		t.Fatalf("unregistered replacement blocked command: %+v", reject)
	}
}

func TestExactCommandReplacementRejectsInexactForms(t *testing.T) {
	commands := []string{
		"sleep 0.5",
		"sleep 1; curl https://example.test",
		"sleep 2 && curl -o /dev/null http://localhost:8080/health",
		"curl example.test",
		"curl ftp://example.test/file",
		"curl https://example.test/{one,two}",
		"curl -H 'Host: other.example.test' https://example.test",
		"curl -X get https://example.test",
		"curl -X GET -d payload https://example.test",
		"curl -I -X POST https://example.test",
		"curl --max-time 0.5 https://example.test",
		"curl --max-time 1.0005 https://example.test",
		"curl -L -X POST -d payload https://example.test",
		"curl -L -H 'Authorization: Bearer token' https://example.test",
		"curl -k https://example.test",
		"curl -u user https://example.test",
		"curl -c a.txt -b b.txt https://example.test",
		"curl -F 'notes=<notes.txt' https://example.test",
		"curl -F 'file=@dist/app.crate;encoder=gzip' https://example.test",
		"curl -o ../out.json https://example.test",
		"curl -o - https://example.test",
		"curl -O https://example.test/",
		"curl -w '%{time_total}' https://example.test",
		"curl --connect-timeout 5 https://example.test",
		"curl https://example.test/a || curl https://example.test/b",
		"curl https://example.test/a; ls",
		"echo start; echo end",
		"curl -H 'X-Remove:' https://example.test",
		"curl -sSf https://example.test/v1",
		"curl --fail https://example.test/v1",
		"curl --fail-with-body -o out.json https://example.test/v1",
		"curl -sS https://example.test/v1 > out.json",
		"curl -sS https://example.test/v1 2> err.log",
		"ls > listing.txt",
		"cat notes.txt > copy.txt",
		"wc -l < notes.txt",
		"git status 2>&1",
		"curl -I -d payload https://example.test",
		"rg -g",
		"rg needle --glob",
		"grep needle file.txt",
		"grep --hidden -r needle .",
		"find . -maxdepth 9 -name '*.go'",
		"tree -L 9 src",
		"rm -rf build",
		"mkdir build",
		"chown current:current scripts/run.sh",
		"chown owner scripts/run.sh",
		"cp -R source destination",
		"git branch -a",
		"git diff main..dev",
		"git diff -M",
		"git diff --no-index /dev/null a.go",
		"git status -uno",
		"git status --untracked-files=no",
		"git status --ignored",
		"git status -v",
		"git status --show-stash",
		"git status -- ../outside",
		"git restore",
		"git add -A && git commit -m all",
		"git commit --amend -m unscoped",
		"git commit --amend --no-edit -- src/a.go",
		"git commit --amend --reset-author -m changed -- src/a.go",
		"git commit --amend --no-verify -m changed -- src/a.go",
		"git commit --amend -m first -m second -- src/a.go",
		"git commit --amend -m outside -- ../outside.txt",
		"git commit --amend -m missing --",
		"git add -- other.go && git commit --amend -m changed -- src/a.go",
		"git add -- src/a.go && git commit --amend -m unscoped",
		"unzip archive.zip",
		"cp ../outside.txt local.txt",
		"git add ../outside.txt && git commit -m outside",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			if got, ok := exactCommandReplacement(t.Context(), command, t.TempDir(), ""); ok {
				t.Fatalf("inexact command redirected to %#v", got)
			}
		})
	}
}

func TestExactReplacementRejectCarriesValidatedRetryData(t *testing.T) {
	calls := []ReplacementCall{{Tool: "grep", Args: map[string]any{"pattern": "needle"}}}
	reject := exactReplacementReject("rg needle", "implement", calls)
	if reject == nil || reject.Code != "USE_GREP_REGEX" {
		t.Fatalf("reject = %+v", reject)
	}
	if !reflect.DeepEqual(reject.Data["replacement_calls"], calls) || reject.Data["suggested_tool"] != "grep" ||
		reject.Data["example"] == "" || reject.Data["args_hint"] == "" {
		t.Fatalf("reject data = %+v", reject.Data)
	}
}

func TestParseCommandEnvelopeRefusesFieldsWithNoNativeShape(t *testing.T) {
	for _, args := range []map[string]any{
		{"command": "curl https://example.test", "env": map[string]any{"TOKEN": "value"}},
		{"command": "curl https://example.test", "stdout_to": "out.txt"},
		{"command": "curl https://example.test", "background": true},
		{"command": "curl https://example.test", "terminal_capture": map[string]any{}},
		{"pipeline": "curl https://example.test | jq ."},
		{"command": "curl https://example.test", "timeout_ms": "soon"},
		{"command": "curl https://example.test", "cwd": 7},
		{"command": "git add a.go && git commit -m fix", "verification": true},
		{"command": "git add a.go && git commit -m fix", "verification": "false"},
		{"command": "   "},
	} {
		if _, ok := parseCommandEnvelope(args); ok {
			t.Fatalf("envelope with runner-only semantics was eligible: %#v", args)
		}
	}
	env, ok := parseCommandEnvelope(map[string]any{
		"command": "curl https://example.test", "cwd": "src", "timeout_ms": float64(30000),
		"capability_request": map[string]any{"host_resources": []any{"docker"}},
	})
	if !ok || env.cwd != "src" || env.timeoutMS != 30000 || env.capability == nil {
		t.Fatalf("envelope = %#v ok=%v", env, ok)
	}
}

func TestNonVerificationCommitRetainsNativeReplacement(t *testing.T) {
	for _, command := range []string{
		"git add -- a.go && git commit -m fix",
		"git commit --amend -m fix -- a.go",
	} {
		args := map[string]any{"command": command, "verification": false}
		schema := map[string]any{"properties": map[string]any{
			"verification": map[string]any{"type": "boolean", "default": false},
		}}
		envelope, ok := parseCommandEnvelope(commandArgsWithoutDefaults(args, schema))
		if !ok {
			t.Fatalf("verification:false suppressed replacement for %q", command)
		}
		calls, ok := exactCommandReplacement(t.Context(), envelope.command, t.TempDir(), "")
		if !ok {
			t.Fatalf("no replacement for %q", command)
		}
		carried, ok := carryCommandEnvelope(envelope, calls)
		if !ok || len(carried) != 1 || carried[0].Tool != "git_commit" {
			t.Fatalf("replacement = %#v, ok=%v", carried, ok)
		}
		if _, copied := carried[0].Args["verification"]; copied {
			t.Fatal("runner evidence metadata leaked into git_commit arguments")
		}
	}
}

func TestCarryCommandEnvelopeTimeout(t *testing.T) {
	cases := []struct {
		name     string
		envelope int
		call     ReplacementCall
		want     any
	}{
		{"http takes the runner deadline", 30000, ReplacementCall{Tool: "http_request", Args: map[string]any{"url": "https://example.test"}}, 30000},
		{"http keeps its tighter own deadline", 30000, ReplacementCall{Tool: "http_request", Args: map[string]any{"url": "https://example.test", "timeout_ms": 1500}}, 1500},
		{"wait is cut to the runner deadline", 1000, ReplacementCall{Tool: "wait", Args: map[string]any{"timeout_ms": 5000}}, 1000},
		{"in-process tool accepts runner deadline", 30000, ReplacementCall{Tool: "git_status", Args: map[string]any{"paths": []any{"a.go"}}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, ok := carryCommandEnvelope(commandEnvelope{timeoutMS: tc.envelope}, []ReplacementCall{tc.call})
			if !ok || !reflect.DeepEqual(calls[0].Args["timeout_ms"], tc.want) {
				t.Fatalf("timeout_ms = %#v ok=%v, want %#v", calls[0].Args["timeout_ms"], ok, tc.want)
			}
		})
	}
}

func TestCarryCommandEnvelopeCwd(t *testing.T) {
	cases := []struct {
		name string
		cwd  string
		call ReplacementCall
		want map[string]any
	}{
		{"absolute cwd", "/tmp/work", ReplacementCall{Tool: "read", Args: map[string]any{"path": "note.txt"}}, map[string]any{"path": "/tmp/work/note.txt"}},
		{"absolute path unchanged", "src", ReplacementCall{Tool: "read", Args: map[string]any{"path": "/tmp/note.txt"}}, map[string]any{"path": "/tmp/note.txt"}},
		{"read joins", "src", ReplacementCall{Tool: "read", Args: map[string]any{"path": "main.go"}}, map[string]any{"path": "src/main.go"}},
		{"list_dir default resolves to cwd", "src/", ReplacementCall{Tool: "list_dir", Args: map[string]any{"path": ".", "max_depth": 1}}, map[string]any{"path": "src", "max_depth": 1}},
		{"grep without path searches cwd", "src", ReplacementCall{Tool: "grep", Args: map[string]any{"pattern": "x"}}, map[string]any{"pattern": "x", "path": "src"}},
		{"http body and response paths join", "svc", ReplacementCall{Tool: "http_request", Args: map[string]any{"url": "https://example.test", "body_path": "req.json", "response_path": "out/res.json"}},
			map[string]any{"url": "https://example.test", "body_path": "svc/req.json", "response_path": "svc/out/res.json"}},
		{"copy pairs join", "a", ReplacementCall{Tool: "copy", Args: map[string]any{"copies": []any{map[string]any{"from": "x", "to": "y"}}}},
			map[string]any{"copies": []any{map[string]any{"from": "a/x", "to": "a/y"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, ok := carryCommandEnvelope(commandEnvelope{cwd: tc.cwd}, []ReplacementCall{tc.call})
			if !ok || !reflect.DeepEqual(calls[0].Args, tc.want) {
				t.Fatalf("args = %#v ok=%v, want %#v", calls[0].Args, ok, tc.want)
			}
		})
	}
	for _, cwd := range []string{"@other/src", "../sibling"} {
		if _, ok := carryCommandEnvelope(commandEnvelope{cwd: cwd}, []ReplacementCall{{Tool: "read", Args: map[string]any{"path": "a"}}}); ok {
			t.Fatalf("cwd %q outside the primary root was carried", cwd)
		}
	}
	for _, tool := range []string{"git_status", "git_ref", "git_branches", "git_log", "git_show", "git_commit", "git_checkout", "git_stash_list", "git_compare"} {
		if _, ok := carryCommandEnvelope(commandEnvelope{cwd: "/tmp/other-repository"}, []ReplacementCall{{Tool: tool, Args: map[string]any{}}}); ok {
			t.Fatalf("%s absolute cwd selected an unrepresented repository", tool)
		}
	}
}

func TestSingleFileGrepReplacementPreservesAbsolutePath(t *testing.T) {
	project, scratch := t.TempDir(), t.TempDir()
	file := filepath.Join(scratch, "page.html")
	if err := os.WriteFile(file, []byte("<title>Ready</title>"), 0o600); err != nil {
		t.Fatalf("create HTML fixture: %v", err)
	}
	call, ok := grepReplacement("grep", []string{"-n", "<title>", file}, project)
	if !ok || call.Tool != "grep" || call.Args["path"] != file || call.Args["pattern"] != "<title>" {
		t.Fatalf("single-file grep lost its native equivalent: %+v, matched=%v", call, ok)
	}
	for _, args := range [][]string{{"-o", "title", file}, {"title", scratch}, {"title", file + ".missing"}, {`\(title\)`, file}} {
		if call, ok := grepReplacement("grep", args, project); ok {
			t.Fatalf("unsupported grep %q redirected to %+v", args, call)
		}
	}
}

func TestGrepReplacementRejectsDifferentGrammars(t *testing.T) {
	for _, tt := range []struct {
		program string
		args    []string
	}{
		{"rg", []string{"-r", "replacement", "pattern"}},
		{"rg", []string{"-R", "pattern"}},
		{"grep", []string{"-R", "pattern", "src"}},
		{"rg", []string{"-g", "*.go", "-g", "*.ts", "pattern", "src"}},
		{"rg", []string{" pattern ", "src"}},
		{"grep", []string{"-r", "", "src"}},
		{"grep", []string{"-r", "a+b", "src"}},
		{"grep", []string{"-r", "a|b", "src"}},
		{"grep", []string{"-r", `(a)`, "src"}},
		{"grep", []string{"-r", `a\{2\}`, "src"}},
		{"grep", []string{"-r", "a\nb", "src"}},
	} {
		if call, ok := grepReplacement(tt.program, tt.args, t.TempDir()); ok {
			t.Fatalf("%s %q changed semantics: %+v", tt.program, tt.args, call)
		}
	}
}

func TestGrepReplacementPreservesSearchMode(t *testing.T) {
	for _, tt := range []struct {
		program string
		args    []string
		hidden  bool
	}{
		{"rg", []string{"$NAME", "src"}, false},
		{"rg", []string{"--hidden", "$NAME", "src"}, true},
		{"grep", []string{"-r", "$NAME", "src"}, true},
	} {
		call, ok := grepReplacement(tt.program, tt.args, t.TempDir())
		if !ok || call.Args["structural"] != false || call.Args["include_hidden"] != tt.hidden {
			t.Fatalf("%s %q changed search mode: %+v, matched=%v", tt.program, tt.args, call, ok)
		}
	}
}

func TestCarryCommandEnvelopeCapability(t *testing.T) {
	loopback := map[string]any{"ports": []any{8080}}
	http := func() ReplacementCall {
		return ReplacementCall{Tool: "http_request", Args: map[string]any{"url": "http://localhost:8080/health", "method": "GET"}}
	}
	calls, ok := carryCommandEnvelope(commandEnvelope{capability: map[string]any{"loopback_connect": loopback}}, []ReplacementCall{http()})
	if !ok || !reflect.DeepEqual(calls[0].Args["capability_request"], map[string]any{"loopback_connect": loopback}) {
		t.Fatalf("loopback capability calls = %#v ok=%v", calls, ok)
	}
	if _, ok := carryCommandEnvelope(commandEnvelope{capability: map[string]any{"host_resources": []any{"docker"}}}, []ReplacementCall{http()}); ok {
		t.Fatal("process-only authority was silently dropped")
	}
	if _, ok := carryCommandEnvelope(commandEnvelope{capability: map[string]any{"direct_ip": true}}, []ReplacementCall{http()}); ok {
		t.Fatal("direct_ip contradicts a mediated replacement and must refuse")
	}
	if _, ok := carryCommandEnvelope(commandEnvelope{capability: map[string]any{"loopback_connect": loopback}}, []ReplacementCall{{
		Tool: "wait", Args: map[string]any{"timeout_ms": 1000},
	}}); ok {
		t.Fatal("unrelated capability was attached to timer wait")
	}
}

// Existing paths take precedence over refs for bare diff operands.
func TestGitDiffReplacementResolvesBareOperandsByExistence(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, ok := exactCommandReplacement(t.Context(), "git diff src/main.go docs", dir, "")
	want := []ReplacementCall{{Tool: "git_diff", Args: map[string]any{"paths": []any{"src/main.go", "docs"}}}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("existing operands: got %#v ok=%v want %#v", got, ok, want)
	}
	got, ok = exactCommandReplacement(t.Context(), "git diff main src/main.go", dir, "")
	want = []ReplacementCall{{Tool: "git_diff", Args: map[string]any{"base_ref": "main", "paths": []any{"src/main.go"}}}}
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("ref then path: got %#v ok=%v want %#v", got, ok, want)
	}
	if got, ok := exactCommandReplacement(t.Context(), "git diff ../outside.go", dir, ""); ok {
		t.Fatalf("parent traversal must stay a command, got %#v", got)
	}
}

func TestCommandEnvelopeNeverDropsDeclaredExecutionConstraints(t *testing.T) {
	for _, capability := range []map[string]any{
		{"future_capability": false},
		{"loopback_connect": map[string]any{"ports": []any{8080}}, "host_resources": []any{"docker"}},
		{"direct_ip": false},
	} {
		if _, ok := carryCommandEnvelope(commandEnvelope{capability: capability}, []ReplacementCall{{Tool: "http_request", Args: map[string]any{"url": "http://localhost:8080"}}}); ok {
			t.Fatalf("unrepresented capability accepted: %v", capability)
		}
	}
	for _, tc := range []struct {
		deadline int
		calls    []ReplacementCall
	}{
		{7200000, []ReplacementCall{{Tool: "http_request", Args: map[string]any{}}}},
		{1000, []ReplacementCall{{Tool: "process_list", Args: map[string]any{}}}},
		{1000, []ReplacementCall{{Tool: "http_request", Args: map[string]any{}}, {Tool: "read", Args: map[string]any{"path": "file"}}}},
	} {
		if _, ok := carryCommandEnvelope(commandEnvelope{timeoutMS: tc.deadline}, tc.calls); ok {
			t.Fatalf("unrepresented total deadline %d accepted for %+v", tc.deadline, tc.calls)
		}
	}
}

func TestReplacementDeclinesTrimmedFilenameIdentity(t *testing.T) {
	for _, command := range []string{"rm ' notes.txt '", "ls ' directory '", "cp ' source ' destination", "curl -o ' response.json ' https://example.test"} {
		if got, ok := exactCommandReplacement(t.Context(), command, t.TempDir(), ""); ok {
			t.Fatalf("%q changed filename during redirect: %+v", command, got)
		}
	}
}

func TestNativeGitReplacementCannotChangeRepositoryThroughCwd(t *testing.T) {
	for _, command := range []string{"git status", "git diff", "git log", "git show HEAD:a.go", "git blame a.go"} {
		for _, cwd := range []string{"nested-repository", "/tmp/another-repository"} {
			env := commandEnvelope{command: command, cwd: cwd}
			if calls, ok := env.replacements(t.Context(), t.TempDir(), ""); ok {
				t.Fatalf("%s with cwd %s lost repository identity: %v", command, cwd, calls)
			}
		}
	}
}

func TestExactReplacementDeclinesPatternsTheShellWouldExpand(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "seed log", os.WriteFile(filepath.Join(dir, "a.log"), nil, 0o644))
	if got, ok := exactCommandReplacement(t.Context(), "rm *.log", dir, ""); ok {
		t.Fatalf("a matching glob translated to %#v", got)
	}
	got, ok := exactCommandReplacement(t.Context(), "find . -name '*.log'", dir, "")
	if !ok || got[0].Args["name_glob"] != "*.log" {
		t.Fatalf("a quoted pattern lost its translation: %#v ok=%v", got, ok)
	}
	got, ok = exactCommandReplacement(t.Context(), "curl -sS https://example.test/api?q=1", dir, "")
	if !ok || got[0].Args["url"] != "https://example.test/api?q=1" {
		t.Fatalf("an unmatched pattern is literal and still translates: %#v ok=%v", got, ok)
	}
}
