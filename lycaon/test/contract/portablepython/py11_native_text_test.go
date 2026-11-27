package portablepython

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythoncatalog"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func py11NativeFixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "py11_native", name))
	contractcheck.FailErr(t, "read py11_native fixture "+name, err)
	return b
}

func TestPY11_NativeTextAndHCL(t *testing.T) {
	receiptDir := strings.TrimSpace(os.Getenv("PW_PY11_NATIVE_RECEIPT_DIR"))
	if receiptDir == "" {
		receiptDir = t.TempDir()
	}
	svc := newPY11Service(t, receiptDir)

	t.Run("engine_pairing_and_targeted_discovery", func(t *testing.T) {
		info := svc.RuntimeInfo()
		if info.NativeEngine == nil || info.NativeEngine.EngineTag != "pw-cpython-wasi-engine-v0.3.0" {
			t.Fatalf("expected v0.3.0 native engine tag, got %+v", info.NativeEngine)
		}
		m := pythoncatalog.DefaultLaunchManifest()
		if got := pythoncatalog.RenderTargetedHelp(m, "Unicode text boundaries"); !strings.Contains(got, "regex") {
			t.Fatalf("targeted help for 'Unicode text boundaries' missing regex:\n%s", got)
		}
		if got := pythoncatalog.RenderTargetedHelp(m, "read Terraform blocks"); !strings.Contains(got, "hcl2") {
			t.Fatalf("targeted help for 'read Terraform blocks' missing hcl2:\n%s", got)
		}
	})

	t.Run("regex_unicode_graphemes_properties_overlapping_and_timeout", func(t *testing.T) {
		ws := t.TempDir()
		resp := runPY11Code(t, svc, ws, "py11-native-regex-semantics", `
import json, regex

sample = "e\u0301\U0001f469\u200d\U0001f4bbZ"
clusters = [m.group(0) for m in regex.finditer(r"\X", sample)]
cluster_spans = [list(m.span()) for m in regex.finditer(r"\X", sample)]
assert clusters == ["e\u0301", "\U0001f469\u200d\U0001f4bb", "Z"], clusters
assert cluster_spans == [[0, 2], [2, 5], [5, 6]], cluster_spans

script_text = "abc \u03b1\u03b2\u03b3 123 \u03a9"
greek = [(m.group(0), list(m.span())) for m in regex.finditer(r"\p{Script=Greek}+", script_text)]
assert greek == [("\u03b1\u03b2\u03b3", [4, 7]), ("\u03a9", [12, 13])], greek

upper = [(m.group(0), list(m.span())) for m in regex.finditer(r"\p{Lu}", "a\u0394b\u00c9")]
assert upper == [("\u0394", [1, 2]), ("\u00c9", [3, 4])], upper

ov_spans = [list(m.span()) for m in regex.finditer(r"aba", "ababa", overlapped=True)]
assert ov_spans == [[0, 3], [2, 5]], ov_spans

timed_out = False
try:
    regex.match(r"(a+)+b", "a" * 28 + "c", timeout=0.05)
except TimeoutError:
    timed_out = True
assert timed_out, "expected regex per-operation timeout on bounded backtracking input"

print(json.dumps({
    "regex_version": regex.__version__,
    "cluster_spans": cluster_spans,
    "greek": greek,
    "ov_spans": ov_spans,
    "per_op_timeout": timed_out,
}))
`)
		var out map[string]any
		contractcheck.FailErr(t, "parse regex semantics JSON", json.Unmarshal([]byte(resp.Stdout), &out))
		if out["regex_version"] != "2.5.148" && out["regex_version"] != "2024.11.6" {
			t.Logf("regex.__version__ = %v", out["regex_version"])
		}
		if out["per_op_timeout"] != true {
			t.Fatalf("expected per_op_timeout=true, got %+v", out)
		}
	})

	t.Run("regex_host_cancellation_no_partial_publication_and_recovery", func(t *testing.T) {
		ws := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		resp, err := svc.ExecuteTool(ctx, portablepython.ToolRequest{
			ExecutionID:  "py11-native-regex-cancel",
			CallID:       "py11-native-regex-cancel-call",
			SessionID:    "py11-native",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs: map[string]any{"code": `
import regex
with open("partial_cancel.txt", "w", encoding="utf-8") as fh:
    fh.write("must-not-publish")
regex.match(r"(a+)+b", "a" * 34 + "c")
`},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		})
		if err == nil && (resp.CleanCompletion || resp.ExitCode == 0) {
			t.Fatalf("expected host cancellation stop during native regex matching, got clean completion: %+v", resp)
		}
		if _, statErr := os.Stat(filepath.Join(ws, "partial_cancel.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("partial_cancel.txt must not be published after host cancellation (statErr=%v)", statErr)
		}

		// Subsequent fresh-guest invocation must succeed cleanly.
		recovery := runPY11Code(t, svc, ws, "py11-native-regex-cancel-recovery", `
import regex
assert regex.findall(r"\X", "a\u0301b") == ["a\u0301", "b"]
with open("recovered_after_cancel.txt", "w", encoding="utf-8") as fh:
    fh.write("ok")
print("RECOVERY_OK")
`)
		if !strings.Contains(recovery.Stdout, "RECOVERY_OK") {
			t.Fatalf("unexpected recovery stdout: %q", recovery.Stdout)
		}
		if b, err := os.ReadFile(filepath.Join(ws, "recovered_after_cancel.txt")); err != nil || string(b) != "ok" {
			t.Fatalf("recovery write failed: err=%v body=%q", err, string(b))
		}
	})

	t.Run("regex_fuel_exhaustion_no_partial_publication_and_recovery", func(t *testing.T) {
		ws := t.TempDir()
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
		resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py11-native-regex-fuel",
			CallID:       "py11-native-regex-fuel-call",
			SessionID:    "py11-native",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs: map[string]any{"code": `
import regex
with open("partial_fuel.txt", "w", encoding="utf-8") as fh:
    fh.write("must-not-publish")
regex.match(r"(a+)+b", "a" * 34 + "c")
`},
			Roots:        roots,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
			Limits: pythonruntime.ResourceLimits{
				MaxGuestWork: 35_000_000,
			},
		})
		contractcheck.FailErr(t, "execute fuel-bounded regex", err)
		if resp.CleanCompletion || resp.Stop == nil || resp.Stop.Code != pythonruntime.StopWorkLimitExceeded {
			t.Fatalf("expected fuel exhaustion stop, got clean=%v exit=%d stop=%+v stderr=%q",
				resp.CleanCompletion, resp.ExitCode, resp.Stop, resp.Stderr)
		}
		if _, statErr := os.Stat(filepath.Join(ws, "partial_fuel.txt")); !os.IsNotExist(statErr) {
			t.Fatalf("partial_fuel.txt must not be published after fuel exhaustion (statErr=%v)", statErr)
		}

		// Subsequent fresh-guest invocation must succeed cleanly.
		recovery := runPY11Code(t, svc, ws, "py11-native-regex-fuel-recovery", `
import regex
assert regex.findall(r"\p{Nd}+", "v12-34") == ["12", "34"]
print("FUEL_RECOVERY_OK")
`)
		if !strings.Contains(recovery.Stdout, "FUEL_RECOVERY_OK") {
			t.Fatalf("unexpected fuel recovery stdout: %q", recovery.Stdout)
		}
	})

	t.Run("hcl2_terraform_blocks_malformed_and_unsaved_editor_revision", func(t *testing.T) {
		ws := t.TempDir()
		contractcheck.FailErr(t, "write main.tf", os.WriteFile(filepath.Join(ws, "main.tf"), py11NativeFixtureBytes(t, "main.tf"), 0o644))
		contractcheck.FailErr(t, "write malformed.tf", os.WriteFile(filepath.Join(ws, "malformed.tf"), py11NativeFixtureBytes(t, "malformed.tf"), 0o644))

		resp := runPY11Code(t, svc, ws, "py11-native-hcl2-parse", `
import json, hcl2

with open("main.tf", "r", encoding="utf-8") as fh:
    doc = hcl2.load(fh)

assert doc["variable"] == [{"env": {"type": "${string}", "default": "staging"}}], doc["variable"]
resources = doc["resource"]
assert len(resources) == 2, resources
logs = resources[0]["aws_s3_bucket"]["logs"]
arts = resources[1]["aws_s3_bucket"]["artifacts"]
assert logs["bucket"] == "pw-${var.env}-logs", logs
assert logs["tags"] == {"owner": "platform", "tier": "audit"}, logs
assert arts["bucket"] == "pw-${var.env}-artifacts", arts
assert arts["tags"] == {"owner": "build", "tier": "durable"}, arts
assert doc["output"] == [{"logs_bucket_ref": {"value": "${aws_s3_bucket.logs.bucket}"}}], doc["output"]

malformed_failed = False
try:
    with open("malformed.tf", "r", encoding="utf-8") as fh:
        hcl2.load(fh)
except Exception as exc:
    malformed_failed = type(exc).__name__
assert malformed_failed, "expected malformed.tf to raise parse error"

print(json.dumps({
    "resource_count": len(resources),
    "logs_bucket_expr": logs["bucket"],
    "malformed_error": malformed_failed,
}))
`)
		if !strings.Contains(resp.Stdout, "pw-${var.env}-logs") {
			t.Fatalf("unexpected hcl2 stdout: %q", resp.Stdout)
		}

		// Verify active unsaved editor revision is read through workspace adapter.
		draftTF := []byte(`variable "env" {
  default = "prod-unsaved"
}

resource "aws_s3_bucket" "logs" {
  bucket = "pw-${var.env}-logs"
  tags = {
    owner = "editor-draft"
  }
}
`)
		editor := &qualEditorBridge{
			drafts: map[string]pythondocuments.EditorDraft{
				"main.tf": {
					DocumentID: "doc-main-tf",
					Revision:   "rev-draft-1",
					Bytes:      draftTF,
					BaseSHA256: pythondocuments.SHA256Hex(draftTF),
					Dirty:      true,
				},
			},
		}
		roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: ws}}
		draftResp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
			ExecutionID:  "py11-native-hcl2-editor-draft",
			CallID:       "py11-native-hcl2-editor-draft-call",
			SessionID:    "py11-native",
			Actor:        "coordinator",
			View:         "primary",
			Surface:      "implement_investigate",
			RawArgs: map[string]any{"code": `
import hcl2
with open("main.tf", "r", encoding="utf-8") as fh:
    doc = hcl2.load(fh)
assert doc["variable"][0]["env"]["default"] == "prod-unsaved", doc
assert doc["resource"][0]["aws_s3_bucket"]["logs"]["tags"]["owner"] == "editor-draft", doc
print("HCL2_EDITOR_DRAFT_OK")
`},
			Roots:        roots,
			Editor:       editor,
			MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
		})
		contractcheck.FailErr(t, "execute hcl2 unsaved editor draft", err)
		if !draftResp.CleanCompletion || draftResp.ExitCode != 0 || !strings.Contains(draftResp.Stdout, "HCL2_EDITOR_DRAFT_OK") {
			t.Fatalf("hcl2 editor draft failed: exit=%d stdout=%q stderr=%q", draftResp.ExitCode, draftResp.Stdout, draftResp.Stderr)
		}
	})
}
