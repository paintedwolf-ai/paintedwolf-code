package portablepython

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func py11FixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("testdata", "py11", name)
	b, err := os.ReadFile(p)
	contractcheck.FailErr(t, "read py11 fixture "+name, err)
	return b
}

func copyPY11Fixture(t *testing.T, wsDir, name string) {
	t.Helper()
	dst := filepath.Join(wsDir, name)
	contractcheck.FailErr(t, "mkdir py11 fixture parent", os.MkdirAll(filepath.Dir(dst), 0o755))
	contractcheck.FailErr(t, "write py11 fixture "+name, os.WriteFile(dst, py11FixtureBytes(t, name), 0o644))
}

func newPY11Service(t *testing.T, receiptDir string) *portablepython.Service {
	t.Helper()
	requireStrict := os.Getenv("PW_REQUIRE_PY11") == "1" || os.Getenv("PW_REQUIRE_NATIVE_QUAL") == "1"
	if requireStrict {
		if strings.TrimSpace(os.Getenv(portablepython.EnvImageDir)) != "" {
			t.Fatalf("%s must be UNSET during PY11 qualification", portablepython.EnvImageDir)
		}
		if strings.TrimSpace(os.Getenv(pythonruntime.EnvEngineDir)) != "" {
			t.Fatalf("%s must be UNSET during PY11 qualification", pythonruntime.EnvEngineDir)
		}
	}
	if root := strings.TrimSpace(os.Getenv("PW_QUAL_ENGINE_ROOT")); root != "" && strings.TrimSpace(os.Getenv("LYCAON_ENGINE_ROOT")) == "" {
		t.Setenv("LYCAON_ENGINE_ROOT", root)
	}
	cfg := portablepython.ServiceConfig{
		ScratchRoot: t.TempDir(),
	}
	if receiptDir != "" {
		sink, err := portablepython.NewFileReceiptStore(receiptDir)
		contractcheck.FailErr(t, "create PY11 FileReceiptStore", err)
		cfg.ReceiptSink = sink
	}
	svc, err := portablepython.NewService(context.Background(), cfg)
	if err != nil {
		if requireStrict {
			t.Fatalf("failed to initialize PY11 Portable Python service: %v", err)
		}
		t.Skipf("no staged Portable Python service available (set LYCAON_ENGINE_ROOT): %v", err)
	}
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	info := svc.RuntimeInfo()
	if info.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("PY11 default RuntimeInfo.Backend=%q want %q on %s/%s", info.Backend, pythonruntime.BackendNativeAOT, runtime.GOOS, runtime.GOARCH)
	}
	if info.NativeEngine == nil || info.NativeEngine.EngineTag == "" || info.NativeEngine.ABIVersion == 0 {
		t.Fatalf("PY11 RuntimeInfo.NativeEngine missing verified identity: %+v", info.NativeEngine)
	}
	return svc
}

func runPY11Code(t *testing.T, svc *portablepython.Service, wsDir, execID, code string) *portablepython.ToolResponse {
	t.Helper()
	roots := []pythondocuments.HostRootConfig{{ID: "ws", Dir: wsDir}}
	// Use unchanged default production limits (zero ResourceLimits -> Service defaults).
	resp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
		ExecutionID:  execID,
		CallID:       execID + "-call",
		SessionID:    "py11-workflows",
		Actor:        "coordinator",
		View:         "primary",
		Surface:      "implement_investigate",
		RawArgs:      map[string]any{"code": code},
		Roots:        roots,
		MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, roots),
	})
	contractcheck.FailErr(t, "execute "+execID, err)
	if !resp.CleanCompletion || resp.ExitCode != 0 {
		t.Fatalf("%s failed: exit=%d clean=%v stop=%+v diag=%v usage=%+v stdout=%q stderr=%q",
			execID, resp.ExitCode, resp.CleanCompletion, resp.Stop, resp.RuntimeDiagnostics, resp.Usage, resp.Stdout, resp.Stderr)
	}
	if resp.Backend != pythonruntime.BackendNativeAOT {
		t.Fatalf("%s ran on backend=%q want %q", execID, resp.Backend, pythonruntime.BackendNativeAOT)
	}
	return resp
}

func maybeArchivePY11Artifact(name string, data []byte) {
	if dir := strings.TrimSpace(os.Getenv("PW_PY11_ARTIFACT_DIR")); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, name), data, 0o644)
	}
}

// TestPY11_PurePackageWorkflows exercises all 34 pure-Python PY11 additions
// (plus existing ruamel.yaml strict duplicate-key detection) across all 8
// issue families with independent Go/host oracles under default production limits.
func TestPY11_PurePackageWorkflows(t *testing.T) {
	svc := newPY11Service(t, t.TempDir())

	// Group 1: Repository and dependency analysis (pathspec, networkx, unidiff)
	t.Run("01_repo_and_dependency_analysis", func(t *testing.T) {
		ws := t.TempDir()
		copyPY11Fixture(t, ws, "gitignore_rules.txt")
		copyPY11Fixture(t, ws, "dep_graph.json")
		copyPY11Fixture(t, ws, "sample.diff")

		// Build synthetic git repository in ws/repo and compute the independent
		// host git check-ignore oracle for the documented traversal policy.
		repoDir := filepath.Join(ws, "repo")
		contractcheck.FailErr(t, "mkdir synthetic repo", os.MkdirAll(repoDir, 0o755))
		contractcheck.FailErr(t, "write repo .gitignore", os.WriteFile(filepath.Join(repoDir, ".gitignore"), py11FixtureBytes(t, "gitignore_rules.txt"), 0o644))

		candidates := []string{
			"root_only.log",
			"sub/root_only.log",
			"build/output/a.tmp",
			"build/other/a.tmp",
			"old.bak",
			"keep.bak",
			"sub/old.bak",
			"sub/keep.bak",
			"cache/item.dat",
			"src/main.py",
		}
		for _, rel := range candidates {
			full := filepath.Join(repoDir, filepath.FromSlash(rel))
			contractcheck.FailErr(t, "mkdir candidate parent", os.MkdirAll(filepath.Dir(full), 0o755))
			contractcheck.FailErr(t, "write candidate "+rel, os.WriteFile(full, []byte("x\n"), 0o644))
		}

		gitInit := exec.Command("git", "init", "-q")
		gitInit.Dir = repoDir
		if out, err := gitInit.CombinedOutput(); err != nil {
			t.Fatalf("git init failed: %v (%s)", err, string(out))
		}

		gitIgnored := make(map[string]bool, len(candidates))
		for _, rel := range candidates {
			cmd := exec.Command("git", "check-ignore", "-q", "--", rel)
			cmd.Dir = repoDir
			err := cmd.Run()
			if err == nil {
				gitIgnored[rel] = true
			} else if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
				gitIgnored[rel] = false
			} else {
				t.Fatalf("git check-ignore %q unexpected error: %v", rel, err)
			}
		}

		resp := runPY11Code(t, svc, ws, "py11-group1-repo-deps", `
import json
from pathlib import Path
import networkx as nx
import pathspec
import unidiff

# 1. pathspec GitIgnoreSpec
rules = Path("gitignore_rules.txt").read_text(encoding="utf-8").splitlines()
spec = pathspec.GitIgnoreSpec.from_lines(rules)
candidates = [
    "root_only.log",
    "sub/root_only.log",
    "build/output/a.tmp",
    "build/other/a.tmp",
    "old.bak",
    "keep.bak",
    "sub/old.bak",
    "sub/keep.bak",
    "cache/item.dat",
    "src/main.py",
]
pathspec_ignored = {p: bool(spec.match_file(p)) for p in candidates}

# 2. networkx cycle and reverse dependents (no optional graph engines)
graph_data = json.loads(Path("dep_graph.json").read_text(encoding="utf-8"))
g = nx.DiGraph()
g.add_edges_from(graph_data["edges"])
cycles = [sorted(c) for c in nx.simple_cycles(g)]
assert len(cycles) == 1, f"expected 1 cycle, got {cycles}"
cycle_nodes = set(cycles[0])
reverse_dependents = set()
for node in cycle_nodes:
    reverse_dependents.update(nx.ancestors(g, node))
reverse_dependents -= cycle_nodes

# 3. unidiff hunk coordinates and no-final-newline marker
patch = unidiff.PatchSet(Path("sample.diff").read_text(encoding="utf-8"))
assert len(patch) == 1
pf = patch[0]
hunks = []
for h in pf:
    has_no_newline = any(line.line_type == "\\" for line in h)
    hunks.append({
        "source_start": h.source_start,
        "source_length": h.source_length,
        "target_start": h.target_start,
        "target_length": h.target_length,
        "added": h.added,
        "removed": h.removed,
        "has_no_newline": has_no_newline,
    })

out = {
    "pathspec_ignored": pathspec_ignored,
    "cycle": cycles[0],
    "reverse_dependents": sorted(reverse_dependents),
    "diff_path": pf.path,
    "hunks": hunks,
}
Path("group1_out.json").write_text(json.dumps(out, sort_keys=True), encoding="utf-8")
print("GROUP1_OK")
`)
		if !strings.Contains(resp.Stdout, "GROUP1_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		raw, err := os.ReadFile(filepath.Join(ws, "group1_out.json"))
		contractcheck.FailErr(t, "read group1_out.json", err)
		var out struct {
			PathspecIgnored   map[string]bool `json:"pathspec_ignored"`
			Cycle             []string        `json:"cycle"`
			ReverseDependents []string        `json:"reverse_dependents"`
			DiffPath          string          `json:"diff_path"`
			Hunks             []struct {
				SourceStart  int  `json:"source_start"`
				SourceLength int  `json:"source_length"`
				TargetStart  int  `json:"target_start"`
				TargetLength int  `json:"target_length"`
				Added        int  `json:"added"`
				Removed      int  `json:"removed"`
				HasNoNewline bool `json:"has_no_newline"`
			} `json:"hunks"`
		}
		contractcheck.FailErr(t, "unmarshal group1_out.json", json.Unmarshal(raw, &out))

		// Compare pathspec against host git check-ignore oracle on every path.
		for _, rel := range candidates {
			got, ok := out.PathspecIgnored[rel]
			if !ok {
				t.Fatalf("pathspec output missing candidate %q", rel)
			}
			if got != gitIgnored[rel] {
				t.Fatalf("pathspec vs git mismatch for %q: pathspec=%v git=%v", rel, got, gitIgnored[rel])
			}
		}
		if !gitIgnored["root_only.log"] || gitIgnored["sub/root_only.log"] {
			t.Fatalf("anchored /root_only.log oracle sanity failed: %+v", gitIgnored)
		}
		if !gitIgnored["old.bak"] || gitIgnored["keep.bak"] || gitIgnored["sub/keep.bak"] {
			t.Fatalf("negation !keep.bak oracle sanity failed: %+v", gitIgnored)
		}
		if !gitIgnored["build/output/a.tmp"] || gitIgnored["build/other/a.tmp"] {
			t.Fatalf("nested build/output/*.tmp oracle sanity failed: %+v", gitIgnored)
		}

		// Verify networkx cycle & reverse dependents.
		if strings.Join(out.Cycle, ",") != "auth,session" {
			t.Fatalf("unexpected cycle: %v", out.Cycle)
		}
		if strings.Join(out.ReverseDependents, ",") != "api,app,cli,worker" {
			t.Fatalf("unexpected reverse dependents: %v", out.ReverseDependents)
		}

		// Verify unidiff hunks & no-newline marker.
		if out.DiffPath != "src/config.py" || len(out.Hunks) != 2 {
			t.Fatalf("unexpected diff summary: path=%q hunks=%+v", out.DiffPath, out.Hunks)
		}
		h0, h1 := out.Hunks[0], out.Hunks[1]
		if h0.SourceStart != 1 || h0.SourceLength != 4 || h0.TargetStart != 1 || h0.TargetLength != 5 || h0.Added != 2 || h0.Removed != 1 || h0.HasNoNewline {
			t.Fatalf("unexpected hunk[0]: %+v", h0)
		}
		if h1.SourceStart != 10 || h1.SourceLength != 3 || h1.TargetStart != 11 || h1.TargetLength != 3 || h1.Added != 1 || h1.Removed != 1 || !h1.HasNoNewline {
			t.Fatalf("unexpected hunk[1]: %+v", h1)
		}
	})

	// Group 2: Config, schedules and tokens (existing ruamel.yaml, croniter, PyJWT, python-dotenv)
	t.Run("02_config_schedules_and_tokens", func(t *testing.T) {
		ws := t.TempDir()
		copyPY11Fixture(t, ws, "dup_keys.yaml")
		copyPY11Fixture(t, ws, "valid_config.yaml")
		copyPY11Fixture(t, ws, "sample.env")

		const syntheticSecret = "SYNTHETIC_HS256_KEY_PY11_NON_PRODUCTION_ONLY_9f2c8b1e"

		resp := runPY11Code(t, svc, ws, "py11-group2-config-tokens", `
import io
import json
from datetime import datetime, timezone
from pathlib import Path
from croniter import croniter
import dotenv
import jwt
from ruamel.yaml import YAML
from ruamel.yaml.constructor import DuplicateKeyError

# 1. Strict ruamel.yaml duplicate nested key detection + valid control
yaml = YAML(typ="safe")
yaml.allow_duplicate_keys = False
dup_err_info = None
try:
    yaml.load(Path("dup_keys.yaml").read_text(encoding="utf-8"))
except DuplicateKeyError as exc:
    dup_err_info = {
        "context_line": (exc.context_mark.line + 1) if exc.context_mark else None,
        "problem_line": (exc.problem_mark.line + 1) if exc.problem_mark else None,
        "message": str(exc),
    }
assert dup_err_info is not None, "expected DuplicateKeyError on dup_keys.yaml"

valid_data = yaml.load(Path("valid_config.yaml").read_text(encoding="utf-8"))
assert valid_data["service"]["limits"]["timeout_ms"] == 500

# 2. croniter next 3 instants from fixed aware datetime
base_dt = datetime(2026, 3, 6, 9, 0, 0, tzinfo=timezone.utc)  # Friday
it = croniter("15 9 * * MON-FRI", base_dt)
next_three = [it.get_next(datetime).isoformat() for _ in range(3)]

# 3. PyJWT HS256 sign/verify with explicit algorithm + tampered signature rejection
secret = "`+syntheticSecret+`"
payload = {"sub": "svc-worker", "role": "reader", "iat": 1772787600}
token = jwt.encode(payload, secret, algorithm="HS256")
decoded = jwt.decode(token, secret, algorithms=["HS256"])
assert decoded == payload

parts = token.split(".")
assert len(parts) == 3
sig = parts[2]
tampered_sig = ("A" if sig[0] != "A" else "B") + sig[1:]
tampered_token = ".".join([parts[0], parts[1], tampered_sig])
tampered_rejected = False
try:
    jwt.decode(tampered_token, secret, algorithms=["HS256"])
except jwt.InvalidSignatureError:
    tampered_rejected = True
assert tampered_rejected, "expected tampered JWT signature to be rejected"

# 4. python-dotenv multiline .env from supplied stream
env_stream = io.StringIO(Path("sample.env").read_text(encoding="utf-8"))
env_values = dict(dotenv.dotenv_values(stream=env_stream))

out = {
    "dup_err": dup_err_info,
    "valid_timeout_ms": valid_data["service"]["limits"]["timeout_ms"],
    "next_three": next_three,
    "jwt_sub": decoded["sub"],
    "jwt_role": decoded["role"],
    "jwt_tampered_rejected": tampered_rejected,
    "env_values": env_values,
}
Path("group2_out.json").write_text(json.dumps(out, sort_keys=True), encoding="utf-8")
print("GROUP2_OK")
`)
		if !strings.Contains(resp.Stdout, "GROUP2_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		raw, err := os.ReadFile(filepath.Join(ws, "group2_out.json"))
		contractcheck.FailErr(t, "read group2_out.json", err)

		// Zero unintended secret disclosure across stdout, stderr, and output artifact.
		for label, text := range map[string]string{
			"stdout":          resp.Stdout,
			"stderr":          resp.Stderr,
			"group2_out.json": string(raw),
		} {
			if strings.Contains(text, syntheticSecret) {
				t.Fatalf("unintended secret disclosure detected in %s", label)
			}
		}

		var out struct {
			DupErr struct {
				ContextLine int    `json:"context_line"`
				ProblemLine int    `json:"problem_line"`
				Message     string `json:"message"`
			} `json:"dup_err"`
			ValidTimeoutMS      int               `json:"valid_timeout_ms"`
			NextThree           []string          `json:"next_three"`
			JWTSub              string            `json:"jwt_sub"`
			JWTRole             string            `json:"jwt_role"`
			JWTTamperedRejected bool              `json:"jwt_tampered_rejected"`
			EnvValues           map[string]string `json:"env_values"`
		}
		contractcheck.FailErr(t, "unmarshal group2_out.json", json.Unmarshal(raw, &out))

		if out.DupErr.ProblemLine != 7 || !strings.Contains(out.DupErr.Message, "timeout_ms") {
			t.Fatalf("expected nested duplicate key timeout_ms on line 7, got %+v", out.DupErr)
		}
		if out.ValidTimeoutMS != 500 {
			t.Fatalf("valid_config.yaml control failed: %d", out.ValidTimeoutMS)
		}
		wantCron := []string{
			"2026-03-06T09:15:00+00:00",
			"2026-03-09T09:15:00+00:00",
			"2026-03-10T09:15:00+00:00",
		}
		if strings.Join(out.NextThree, ",") != strings.Join(wantCron, ",") {
			t.Fatalf("croniter mismatch: got %v want %v", out.NextThree, wantCron)
		}
		if out.JWTSub != "svc-worker" || out.JWTRole != "reader" || !out.JWTTamperedRejected {
			t.Fatalf("PyJWT check failed: %+v", out)
		}
		if out.EnvValues["APP_NAME"] != "portable-python" ||
			out.EnvValues["API_PORT"] != "8443" ||
			out.EnvValues["MULTILINE_BANNER"] != "line one\nline two\nline three" ||
			out.EnvValues["QUOTED_COLON"] != "alpha:beta=gamma" ||
			out.EnvValues["EMPTY_VAL"] != "" {
			t.Fatalf("python-dotenv mismatch: %+v", out.EnvValues)
		}
	})

	// Group 3: Structured text and queries (ftfy, sqlparse, xmltodict, jmespath, toml, tomli)
	t.Run("03_structured_text_and_queries", func(t *testing.T) {
		ws := t.TempDir()
		copyPY11Fixture(t, ws, "mojibake.txt")
		copyPY11Fixture(t, ws, "clean.txt")
		copyPY11Fixture(t, ws, "queries.sql")
		copyPY11Fixture(t, ws, "catalog.xml")
		copyPY11Fixture(t, ws, "inventory.json")
		copyPY11Fixture(t, ws, "project.toml")

		resp := runPY11Code(t, svc, ws, "py11-group3-structured-text", `
import json
import tomllib
from pathlib import Path
import ftfy
import jmespath
import sqlparse
import toml
import tomli
import xmltodict

# 1. ftfy mojibake repair + unchanged clean control
mojibake = Path("mojibake.txt").read_text(encoding="utf-8")
clean = Path("clean.txt").read_text(encoding="utf-8")
fixed = ftfy.fix_text(mojibake)
clean_after = ftfy.fix_text(clean)
assert fixed == clean, f"ftfy repair mismatch: {fixed!r} != {clean!r}"
assert clean_after == clean, f"ftfy modified clean text: {clean_after!r}"

# 2. sqlparse split with quoted semicolons
stmts = [s.strip() for s in sqlparse.split(Path("queries.sql").read_text(encoding="utf-8")) if s.strip()]

# 3. xmltodict attributes and repeated elements
doc = xmltodict.parse(Path("catalog.xml").read_text(encoding="utf-8"))
cat = doc["catalog"]

# 4. jmespath nested JSON list projection
inv = json.loads(Path("inventory.json").read_text(encoding="utf-8"))
projected = jmespath.search("items[?active && category == 'compute'].{sku: sku, qty: qty}", inv)

# 5. toml + tomli compatibility imports compared with stdlib tomllib
toml_text = Path("project.toml").read_text(encoding="utf-8")
parsed_toml = toml.loads(toml_text)
parsed_tomli = tomli.loads(toml_text)
parsed_stdlib = tomllib.loads(toml_text)
assert parsed_toml == parsed_tomli == parsed_stdlib

out = {
    "ftfy_fixed": fixed,
    "ftfy_clean_unchanged": clean_after == clean,
    "sql_statements": stmts,
    "xml_version": cat["@version"],
    "xml_region": cat["@region"],
    "xml_books": cat["book"],
    "jmespath_projected": projected,
    "toml_data": parsed_toml,
}
Path("group3_out.json").write_text(json.dumps(out, sort_keys=True), encoding="utf-8")
print("GROUP3_OK")
`)
		if !strings.Contains(resp.Stdout, "GROUP3_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		raw, err := os.ReadFile(filepath.Join(ws, "group3_out.json"))
		contractcheck.FailErr(t, "read group3_out.json", err)
		var out struct {
			FtfyFixed          string   `json:"ftfy_fixed"`
			FtfyCleanUnchanged bool     `json:"ftfy_clean_unchanged"`
			SQLStatements      []string `json:"sql_statements"`
			XMLVersion         string   `json:"xml_version"`
			XMLRegion          string   `json:"xml_region"`
			XMLBooks           []struct {
				ID    string `json:"@id"`
				Lang  string `json:"@lang"`
				Title string `json:"title"`
				Tag   any    `json:"tag"`
			} `json:"xml_books"`
			JMESPathProjected []struct {
				SKU string `json:"sku"`
				Qty int    `json:"qty"`
			} `json:"jmespath_projected"`
			TOMLData struct {
				Project struct {
					Name    string `json:"name"`
					Version string `json:"version"`
					Enabled bool   `json:"enabled"`
					Ports   []int  `json:"ports"`
					Owner   struct {
						Team   string `json:"team"`
						Oncall string `json:"oncall"`
					} `json:"owner"`
				} `json:"project"`
			} `json:"toml_data"`
		}
		contractcheck.FailErr(t, "unmarshal group3_out.json", json.Unmarshal(raw, &out))

		wantClean := string(py11FixtureBytes(t, "clean.txt"))
		if out.FtfyFixed != wantClean || !out.FtfyCleanUnchanged {
			t.Fatalf("ftfy oracle mismatch: got %q want %q unchanged=%v", out.FtfyFixed, wantClean, out.FtfyCleanUnchanged)
		}
		if len(out.SQLStatements) != 3 ||
			!strings.Contains(out.SQLStatements[0], "'semi;colon'") ||
			!strings.Contains(out.SQLStatements[0], "'a;b'") ||
			!strings.Contains(out.SQLStatements[1], "'done;verified'") {
			t.Fatalf("sqlparse split mismatch: %+v", out.SQLStatements)
		}
		if out.XMLVersion != "2.1" || out.XMLRegion != "us-west" || len(out.XMLBooks) != 2 {
			t.Fatalf("xmltodict mismatch: version=%q region=%q books=%+v", out.XMLVersion, out.XMLRegion, out.XMLBooks)
		}
		tags0, ok := out.XMLBooks[0].Tag.([]any)
		if !ok || len(tags0) != 2 || tags0[0] != "systems" || tags0[1] != "sandbox" {
			t.Fatalf("xmltodict repeated <tag> mismatch: %#v", out.XMLBooks[0].Tag)
		}
		if len(out.JMESPathProjected) != 2 ||
			out.JMESPathProjected[0].SKU != "A100" || out.JMESPathProjected[0].Qty != 12 ||
			out.JMESPathProjected[1].SKU != "C300" || out.JMESPathProjected[1].Qty != 7 {
			t.Fatalf("jmespath projection mismatch: %+v", out.JMESPathProjected)
		}
		p := out.TOMLData.Project
		if p.Name != "portable-demo" || p.Version != "1.4.2" || !p.Enabled || len(p.Ports) != 2 || p.Ports[0] != 8080 || p.Ports[1] != 8443 || p.Owner.Team != "runtime" {
			t.Fatalf("toml/tomli mismatch: %+v", p)
		}
	})

	// Group 4: Tests and synthetic data (pytest-mock, freezegun, Faker)
	t.Run("04_tests_and_synthetic_data", func(t *testing.T) {
		ws := t.TempDir()

		resp := runPY11Code(t, svc, ws, "py11-group4-tests-synthetic", `
import json
from datetime import datetime, timezone
from pathlib import Path
from faker import Faker
from freezegun import freeze_time
import pytest

# 1. pytest-mock explicitly loaded in a helper-owned temp fixture (autoload stays disabled)
test_file = Path("test_py11_mock_helper.py")
test_file.write_text(
    "import os\n"
    "def fetch_value(key):\n"
    "    return f'live:{key}'\n\n"
    "def test_explicit_pytest_mock(mocker):\n"
    "    spy = mocker.patch(__name__ + '.fetch_value', return_value='mocked:alpha')\n"
    "    assert fetch_value('alpha') == 'mocked:alpha'\n"
    "    spy.assert_called_once_with('alpha')\n",
    encoding="utf-8",
)
rc = int(pytest.main([
    "-q",
    "-p", "no:cacheprovider",
    "-p", "no:faulthandler",
    "-p", "pytest_mock",
    "--capture=sys",
    "--rootdir=.",
    str(test_file),
]))
assert rc == 0, f"pytest with explicit pytest_mock failed: rc={rc}"

# 2. freezegun freeze within context + restoration check
before = datetime.now(timezone.utc)
with freeze_time("2025-07-04 12:30:00"):
    frozen = datetime.now(timezone.utc)
after = datetime.now(timezone.utc)
assert frozen == datetime(2025, 7, 4, 12, 30, 0, tzinfo=timezone.utc)
assert after >= before and after.year >= 2026

# 3. Faker seeded repeatability + locale data availability
f1 = Faker("en_US")
f1.seed_instance(20261127)
sample1 = {"name": f1.name(), "email": f1.email(), "city": f1.city()}

f2 = Faker("en_US")
f2.seed_instance(20261127)
sample2 = {"name": f2.name(), "email": f2.email(), "city": f2.city()}
assert sample1 == sample2

f_fr = Faker("fr_FR")
f_fr.seed_instance(42)
f_ja = Faker("ja_JP")
f_ja.seed_instance(42)
locales = {
    "fr_FR": {"name": f_fr.name(), "city": f_fr.city()},
    "ja_JP": {"name": f_ja.name(), "city": f_ja.city()},
}

out = {
    "pytest_rc": rc,
    "frozen_iso": frozen.isoformat(),
    "restored_year": after.year,
    "faker_repeat": sample1 == sample2,
    "faker_sample": sample1,
    "faker_locales": locales,
}
Path("group4_out.json").write_text(json.dumps(out, sort_keys=True), encoding="utf-8")
print("GROUP4_OK")
`)
		if !strings.Contains(resp.Stdout, "GROUP4_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		raw, err := os.ReadFile(filepath.Join(ws, "group4_out.json"))
		contractcheck.FailErr(t, "read group4_out.json", err)
		var out struct {
			PytestRC     int                          `json:"pytest_rc"`
			FrozenISO    string                       `json:"frozen_iso"`
			RestoredYear int                          `json:"restored_year"`
			FakerRepeat  bool                         `json:"faker_repeat"`
			FakerSample  map[string]string            `json:"faker_sample"`
			FakerLocales map[string]map[string]string `json:"faker_locales"`
		}
		contractcheck.FailErr(t, "unmarshal group4_out.json", json.Unmarshal(raw, &out))

		if out.PytestRC != 0 {
			t.Fatalf("pytest-mock rc=%d want 0", out.PytestRC)
		}
		if out.FrozenISO != "2025-07-04T12:30:00+00:00" || out.RestoredYear < 2026 {
			t.Fatalf("freezegun mismatch: frozen=%q restored_year=%d", out.FrozenISO, out.RestoredYear)
		}
		if !out.FakerRepeat || out.FakerSample["name"] == "" || out.FakerSample["email"] == "" {
			t.Fatalf("Faker repeatability failed: %+v", out)
		}
		if out.FakerLocales["fr_FR"]["name"] == "" || out.FakerLocales["ja_JP"]["name"] == "" {
			t.Fatalf("Faker locale data missing: %+v", out.FakerLocales)
		}
	})

	// Group 5: Markup, office files and identification (Markdown, markdownify, html5lib, xlrd, filetype)
	t.Run("05_markup_office_and_identification", func(t *testing.T) {
		ws := t.TempDir()
		copyPY11Fixture(t, ws, "doc.md")
		copyPY11Fixture(t, ws, "fragment.html")
		copyPY11Fixture(t, ws, "malformed.html")
		copyPY11Fixture(t, ws, "legacy.xls")
		copyPY11Fixture(t, ws, "wrong_suffix.txt")

		resp := runPY11Code(t, svc, ws, "py11-group5-markup-office-id", `
import json
from pathlib import Path
from bs4 import BeautifulSoup
import filetype
import markdown
import markdownify
import xlrd

# 1. Markdown -> HTML
rendered_html = markdown.markdown(Path("doc.md").read_text(encoding="utf-8"))
Path("rendered.html").write_text(rendered_html, encoding="utf-8")

# 2. markdownify HTML -> Markdown
converted_md = markdownify.markdownify(Path("fragment.html").read_text(encoding="utf-8"), heading_style="ATX")

# 3. html5lib via BeautifulSoup on malformed HTML
soup = BeautifulSoup(Path("malformed.html").read_text(encoding="utf-8"), "html5lib")
paragraphs = [p.get_text(" ", strip=True) for p in soup.find_all("p")]
spans = [s.get_text(strip=True) for s in soup.find_all("span")]

# 4. xlrd reading legacy binary BIFF8 .xls
wb = xlrd.open_workbook("legacy.xls")
sheet = wb.sheet_by_name("Quarterly")
xls_rows = [[sheet.cell_value(r, c) for c in range(sheet.ncols)] for r in range(sheet.nrows)]

# 5. filetype magic-byte PNG identification despite .txt suffix
kind = filetype.guess("wrong_suffix.txt")
assert kind is not None

out = {
    "converted_md": converted_md,
    "html5lib_paragraphs": paragraphs,
    "html5lib_spans": spans,
    "xls_sheets": wb.sheet_names(),
    "xls_rows": xls_rows,
    "filetype_ext": kind.extension,
    "filetype_mime": kind.mime,
}
Path("group5_out.json").write_text(json.dumps(out, sort_keys=True), encoding="utf-8")
print("GROUP5_OK")
`)
		if !strings.Contains(resp.Stdout, "GROUP5_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		// Independent Go XML parse of rendered.html from Markdown.
		htmlBytes, err := os.ReadFile(filepath.Join(ws, "rendered.html"))
		contractcheck.FailErr(t, "read rendered.html", err)
		wrapped := append([]byte("<root>"), append(htmlBytes, []byte("</root>")...)...)
		dec := xml.NewDecoder(bytes.NewReader(wrapped))
		var seenH1, seenStrong, seenCode bool
		var listItems []string
		var curTag string
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			contractcheck.FailErr(t, "parse rendered.html XML", err)
			switch v := tok.(type) {
			case xml.StartElement:
				curTag = v.Name.Local
			case xml.EndElement:
				curTag = ""
			case xml.CharData:
				txt := strings.TrimSpace(string(v))
				if txt == "" {
					continue
				}
				switch curTag {
				case "h1":
					if txt == "Release Summary" {
						seenH1 = true
					}
				case "strong":
					if txt == "deterministic" {
						seenStrong = true
					}
				case "code":
					if txt == "offline" {
						seenCode = true
					}
				case "li":
					listItems = append(listItems, txt)
				}
			}
		}
		if !seenH1 || !seenStrong || !seenCode || strings.Join(listItems, ",") != "Item one,Item two" {
			t.Fatalf("Markdown HTML oracle failed: h1=%v strong=%v code=%v li=%v raw=%q",
				seenH1, seenStrong, seenCode, listItems, string(htmlBytes))
		}

		raw, err := os.ReadFile(filepath.Join(ws, "group5_out.json"))
		contractcheck.FailErr(t, "read group5_out.json", err)
		var out struct {
			ConvertedMD        string   `json:"converted_md"`
			HTML5LibParagraphs []string `json:"html5lib_paragraphs"`
			HTML5LibSpans      []string `json:"html5lib_spans"`
			XLSSheets          []string `json:"xls_sheets"`
			XLSRows            [][]any  `json:"xls_rows"`
			FiletypeExt        string   `json:"filetype_ext"`
			FiletypeMIME       string   `json:"filetype_mime"`
		}
		contractcheck.FailErr(t, "unmarshal group5_out.json", json.Unmarshal(raw, &out))

		if !strings.Contains(out.ConvertedMD, "## Migration Notes") ||
			!strings.Contains(out.ConvertedMD, "**strict**") ||
			(!strings.Contains(out.ConvertedMD, "*explicit*") && !strings.Contains(out.ConvertedMD, "_explicit_")) ||
			!strings.Contains(out.ConvertedMD, "First step") ||
			!strings.Contains(out.ConvertedMD, "Second step") {
			t.Fatalf("markdownify output mismatch: %q", out.ConvertedMD)
		}
		if len(out.HTML5LibParagraphs) != 2 ||
			!strings.Contains(out.HTML5LibParagraphs[0], "Unclosed paragraph") ||
			!strings.Contains(out.HTML5LibParagraphs[0], "nested text") ||
			out.HTML5LibParagraphs[1] != "Trailing paragraph" ||
			len(out.HTML5LibSpans) != 1 || out.HTML5LibSpans[0] != "nested text" {
			t.Fatalf("html5lib recovery mismatch: p=%v span=%v", out.HTML5LibParagraphs, out.HTML5LibSpans)
		}
		if len(out.XLSSheets) != 1 || out.XLSSheets[0] != "Quarterly" || len(out.XLSRows) != 3 {
			t.Fatalf("xlrd structure mismatch: sheets=%v rows=%v", out.XLSSheets, out.XLSRows)
		}
		if out.XLSRows[0][0] != "Region" || out.XLSRows[0][1] != "Revenue" ||
			out.XLSRows[1][0] != "North" || out.XLSRows[1][1] != 1250.5 ||
			out.XLSRows[2][0] != "South" || out.XLSRows[2][1] != 2499.25 {
			t.Fatalf("xlrd cell values mismatch: %#v", out.XLSRows)
		}
		if out.FiletypeExt != "png" || out.FiletypeMIME != "image/png" {
			t.Fatalf("filetype PNG magic-byte detection failed: ext=%q mime=%q", out.FiletypeExt, out.FiletypeMIME)
		}
	})

	// Group 6: Calendars, tables and charts (pytz, icalendar, tabulate, pygal)
	t.Run("06_calendars_tables_and_charts", func(t *testing.T) {
		ws := t.TempDir()
		copyPY11Fixture(t, ws, "event.ics")

		resp := runPY11Code(t, svc, ws, "py11-group6-cal-tables-charts", `
import json
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo
import icalendar
import pygal
import pytz
from tabulate import tabulate

# 1. pytz.localize across spring-forward and fall-back clock changes,
# cross-checked against stdlib zoneinfo at the same tzdata revision.
tz_pytz = pytz.timezone("America/New_York")
tz_zi = ZoneInfo("America/New_York")

pre_spring = tz_pytz.localize(datetime(2026, 3, 8, 1, 30))
post_spring = tz_pytz.localize(datetime(2026, 3, 8, 3, 30))
fall_dst = tz_pytz.localize(datetime(2026, 11, 1, 1, 30), is_dst=True)
fall_std = tz_pytz.localize(datetime(2026, 11, 1, 1, 30), is_dst=False)

zi_pre_spring = datetime(2026, 3, 8, 1, 30, tzinfo=tz_zi)
zi_post_spring = datetime(2026, 3, 8, 3, 30, tzinfo=tz_zi)
zi_fall_dst = datetime(2026, 11, 1, 1, 30, fold=0, tzinfo=tz_zi)
zi_fall_std = datetime(2026, 11, 1, 1, 30, fold=1, tzinfo=tz_zi)

assert pre_spring.utcoffset() == zi_pre_spring.utcoffset()
assert post_spring.utcoffset() == zi_post_spring.utcoffset()
assert fall_dst.utcoffset() == zi_fall_dst.utcoffset()
assert fall_std.utcoffset() == zi_fall_std.utcoffset()

# 2. icalendar round-trip with TZID and RRULE
cal = icalendar.Calendar.from_ical(Path("event.ics").read_bytes())
roundtrip_bytes = cal.to_ical()
cal2 = icalendar.Calendar.from_ical(roundtrip_bytes)
events = [c for c in cal2.walk() if c.name == "VEVENT"]
assert len(events) == 1
ev = events[0]
rrule = ev.get("RRULE")

# 3. tabulate Markdown table
table_md = tabulate(
    [["North", 1250], ["South", 2499]],
    headers=["Region", "Revenue"],
    tablefmt="github",
)
Path("table.md").write_text(table_md + "\n", encoding="utf-8")

# 4. pygal standalone SVG chart (no external JS/CDN resources required)
chart = pygal.Bar(title="PY11 Throughput", js=[])
chart.x_labels = ["Q1", "Q2", "Q3"]
chart.add("North", [12, 18, 24])
chart.add("South", [9, 15, 21])
svg_text = chart.render(is_unicode=True)
Path("chart.svg").write_text(svg_text, encoding="utf-8")

out = {
    "pytz_offsets": {
        "pre_spring": pre_spring.strftime("%z"),
        "post_spring": post_spring.strftime("%z"),
        "fall_dst": fall_dst.strftime("%z"),
        "fall_std": fall_std.strftime("%z"),
    },
    "ics_summary": str(ev.get("SUMMARY")),
    "ics_tzid": str(ev.get("DTSTART").params.get("TZID")),
    "ics_rrule_freq": [str(x) for x in rrule.get("FREQ", [])],
    "ics_rrule_count": [int(x) for x in rrule.get("COUNT", [])],
    "ics_rrule_byday": [str(x) for x in rrule.get("BYDAY", [])],
}
Path("group6_out.json").write_text(json.dumps(out, sort_keys=True), encoding="utf-8")
print("GROUP6_OK")
`)
		if !strings.Contains(resp.Stdout, "GROUP6_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		raw, err := os.ReadFile(filepath.Join(ws, "group6_out.json"))
		contractcheck.FailErr(t, "read group6_out.json", err)
		var out struct {
			PytzOffsets   map[string]string `json:"pytz_offsets"`
			ICSSummary    string            `json:"ics_summary"`
			ICSTZID       string            `json:"ics_tzid"`
			ICSRRuleFreq  []string          `json:"ics_rrule_freq"`
			ICSRRuleCount []int             `json:"ics_rrule_count"`
			ICSRRuleByDay []string          `json:"ics_rrule_byday"`
		}
		contractcheck.FailErr(t, "unmarshal group6_out.json", json.Unmarshal(raw, &out))

		if out.PytzOffsets["pre_spring"] != "-0500" ||
			out.PytzOffsets["post_spring"] != "-0400" ||
			out.PytzOffsets["fall_dst"] != "-0400" ||
			out.PytzOffsets["fall_std"] != "-0500" {
			t.Fatalf("pytz DST offsets mismatch: %+v", out.PytzOffsets)
		}
		if out.ICSSummary != "DST Boundary Review" ||
			out.ICSTZID != "America/New_York" ||
			strings.Join(out.ICSRRuleFreq, ",") != "WEEKLY" ||
			len(out.ICSRRuleCount) != 1 || out.ICSRRuleCount[0] != 4 ||
			strings.Join(out.ICSRRuleByDay, ",") != "SU" {
			t.Fatalf("icalendar round-trip mismatch: %+v", out)
		}

		// Independent Go check of Markdown table cells from tabulate.
		tableBytes, err := os.ReadFile(filepath.Join(ws, "table.md"))
		contractcheck.FailErr(t, "read table.md", err)
		var tableCells [][]string
		for _, line := range strings.Split(strings.TrimSpace(string(tableBytes)), "\n") {
			trimmed := strings.Trim(strings.TrimSpace(line), "|")
			parts := strings.Split(trimmed, "|")
			var row []string
			for _, p := range parts {
				row = append(row, strings.TrimSpace(p))
			}
			tableCells = append(tableCells, row)
		}
		if len(tableCells) != 4 ||
			strings.Join(tableCells[0], ",") != "Region,Revenue" ||
			strings.Join(tableCells[2], ",") != "North,1250" ||
			strings.Join(tableCells[3], ",") != "South,2499" {
			t.Fatalf("tabulate Markdown table mismatch: %#v\nraw:\n%s", tableCells, string(tableBytes))
		}

		// Independent Go XML inspection of pygal SVG: labels, series, and zero external resources.
		svgBytes, err := os.ReadFile(filepath.Join(ws, "chart.svg"))
		contractcheck.FailErr(t, "read chart.svg", err)
		maybeArchivePY11Artifact("pygal_chart.svg", svgBytes)

		dec := xml.NewDecoder(bytes.NewReader(svgBytes))
		var rootName string
		var textContent []string
		for {
			tok, err := dec.Token()
			if err == io.EOF {
				break
			}
			contractcheck.FailErr(t, "parse chart.svg as XML", err)
			switch v := tok.(type) {
			case xml.StartElement:
				if rootName == "" {
					rootName = v.Name.Local
				}
				for _, attr := range v.Attr {
					// XML namespaces (xmlns / xmlns:*) are identifiers, not fetched resources.
					if attr.Name.Space == "xmlns" || attr.Name.Local == "xmlns" {
						continue
					}
					val := strings.ToLower(strings.TrimSpace(attr.Value))
					if strings.HasPrefix(val, "http://") || strings.HasPrefix(val, "https://") || strings.HasPrefix(val, "//") {
						t.Fatalf("pygal SVG contains external resource reference on <%s %s=%q>", v.Name.Local, attr.Name.Local, attr.Value)
					}
				}
			case xml.CharData:
				txt := strings.TrimSpace(string(v))
				if txt != "" {
					textContent = append(textContent, txt)
				}
			}
		}
		if rootName != "svg" {
			t.Fatalf("pygal root element=%q want svg", rootName)
		}
		joinedSVGText := strings.Join(textContent, "\n")
		for _, want := range []string{"PY11 Throughput", "Q1", "Q2", "Q3", "North", "South"} {
			if !strings.Contains(joinedSVGText, want) {
				t.Fatalf("pygal SVG missing expected label/series %q", want)
			}
		}
	})

	// Group 7: Common script imports (tqdm, colorama, termcolor, rich, click)
	t.Run("07_common_script_imports", func(t *testing.T) {
		ws := t.TempDir()

		resp := runPY11Code(t, svc, ws, "py11-group7-common-imports", `
import io
import json
from pathlib import Path
import click
from click.testing import CliRunner
import colorama
from colorama import Fore, Style
from rich.console import Console
from rich.table import Table
from termcolor import colored
from tqdm import tqdm

# 1. tqdm disabled progress iterator (no terminal progress transcript)
items = list(tqdm([10, 20, 30, 40], disable=True))
assert items == [10, 20, 30, 40]

# 2. colorama + termcolor string helpers
colorama_str = Fore.GREEN + "ok" + Style.RESET_ALL
termcolor_str = colored("ready", "cyan", force_color=True)

# 3. rich Table written to StringIO
buf = io.StringIO()
console = Console(file=buf, force_terminal=False, width=80)
tbl = Table(title="PY11 Checks")
tbl.add_column("Package")
tbl.add_column("Status")
tbl.add_row("rich", "pass")
tbl.add_row("click", "pass")
console.print(tbl)
rich_output = buf.getvalue()

# 4. click command with supplied args (noninteractive, exit status + output)
@click.command()
@click.option("--env", required=True)
@click.option("--count", type=int, default=1)
def deploy_check(env, count):
    click.echo(f"env={env} count={count}")

runner = CliRunner()
res = runner.invoke(deploy_check, ["--env", "staging", "--count", "3"])
assert res.exit_code == 0, f"click exit={res.exit_code} output={res.output!r}"

out = {
    "tqdm_items": items,
    "colorama_has_ansi": "\x1b[" in colorama_str and "ok" in colorama_str,
    "termcolor_has_ansi": "\x1b[" in termcolor_str and "ready" in termcolor_str,
    "rich_output": rich_output,
    "click_exit": res.exit_code,
    "click_output": res.output,
}
Path("group7_out.json").write_text(json.dumps(out, sort_keys=True), encoding="utf-8")
print("GROUP7_OK")
`)
		if !strings.Contains(resp.Stdout, "GROUP7_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}
		if strings.Contains(resp.Stderr, "%|") {
			t.Fatalf("disabled tqdm unexpectedly emitted progress transcript to stderr: %q", resp.Stderr)
		}

		raw, err := os.ReadFile(filepath.Join(ws, "group7_out.json"))
		contractcheck.FailErr(t, "read group7_out.json", err)
		var out struct {
			TqdmItems        []int  `json:"tqdm_items"`
			ColoramaHasANSI  bool   `json:"colorama_has_ansi"`
			TermcolorHasANSI bool   `json:"termcolor_has_ansi"`
			RichOutput       string `json:"rich_output"`
			ClickExit        int    `json:"click_exit"`
			ClickOutput      string `json:"click_output"`
		}
		contractcheck.FailErr(t, "unmarshal group7_out.json", json.Unmarshal(raw, &out))

		if len(out.TqdmItems) != 4 || out.TqdmItems[0] != 10 || out.TqdmItems[3] != 40 {
			t.Fatalf("tqdm items mismatch: %v", out.TqdmItems)
		}
		if !out.ColoramaHasANSI || !out.TermcolorHasANSI {
			t.Fatalf("colorama/termcolor ANSI check failed: %+v", out)
		}
		for _, want := range []string{"PY11 Checks", "Package", "Status", "rich", "click", "pass"} {
			if !strings.Contains(out.RichOutput, want) {
				t.Fatalf("rich table output missing %q:\n%s", want, out.RichOutput)
			}
		}
		if out.ClickExit != 0 || out.ClickOutput != "env=staging count=3\n" {
			t.Fatalf("click mismatch: exit=%d output=%q", out.ClickExit, out.ClickOutput)
		}
	})

	// Group 8: Language and ecosystem data (univers, jproperties, license-expression, graphql-core, tinycss2)
	t.Run("08_ecosystem_data_packages", func(t *testing.T) {
		ws := t.TempDir()
		copyPY11Fixture(t, ws, "sample.properties")
		copyPY11Fixture(t, ws, "schema.graphql")
		copyPY11Fixture(t, ws, "operation.graphql")
		copyPY11Fixture(t, ws, "sample.css")
		copyPY11Fixture(t, ws, "malformed.css")

		resp := runPY11Code(t, svc, ws, "py11-group8-ecosystem-data", `
import json
from pathlib import Path
from graphql import build_schema, parse as gql_parse, validate as gql_validate
from jproperties import Properties
from license_expression import ExpressionError, get_spdx_licensing
import tinycss2
from univers.version_range import GemVersionRange, MavenVersionRange, NpmVersionRange
from univers.versions import MavenVersion, RubygemsVersion, SemverVersion

# 1. univers: npm caret (incl. 0.x), Maven intervals, RubyGems pessimistic bounds
# Distinguish native manifest syntax from normalized vers syntax.
def eval_range(rng_cls, ver_cls, native_expr, cases):
    rng = rng_cls.from_native(native_expr)
    return {
        "native": native_expr,
        "vers": str(rng),
        "results": {v: (ver_cls(v) in rng) for v in cases},
    }

univers_out = {
    "npm_caret_1x": eval_range(
        NpmVersionRange, SemverVersion, "^1.2.3",
        ["1.2.3", "1.9.0", "1.2.2", "2.0.0", "1.2.3-alpha.1"],
    ),
    "npm_caret_0x": eval_range(
        NpmVersionRange, SemverVersion, "^0.2.3",
        ["0.2.3", "0.2.9", "0.2.2", "0.3.0", "0.2.3-rc.1"],
    ),
    "npm_caret_00x": eval_range(
        NpmVersionRange, SemverVersion, "^0.0.3",
        ["0.0.3", "0.0.2", "0.0.4", "0.0.3-alpha.1"],
    ),
    "maven_half_open": eval_range(
        MavenVersionRange, MavenVersion, "[1.0,2.0)",
        ["1.0", "1.5", "2.0", "0.9", "1.0-alpha-1"],
    ),
    "maven_open_closed": eval_range(
        MavenVersionRange, MavenVersion, "(1.0,2.0]",
        ["1.0", "1.5", "2.0", "2.1"],
    ),
    "gem_pessimistic_2": eval_range(
        GemVersionRange, RubygemsVersion, "~> 2.1",
        ["2.1.0", "2.9.4", "2.0.9", "3.0.0", "2.1.0.pre"],
    ),
    "gem_pessimistic_3": eval_range(
        GemVersionRange, RubygemsVersion, "~> 2.1.3",
        ["2.1.3", "2.1.9", "2.1.2", "2.2.0"],
    ),
}

# 2. jproperties: ISO-8859-1 Java Properties.load(InputStream) semantics
props = Properties()
with open("sample.properties", "rb") as f:
    props.load(f, "iso-8859-1")
jprops_out = {k: v.data for k, v in props.items()}

# 3. license-expression: bundled SPDX symbol table, AND/OR/WITH, unknown ID vs syntax error
licensing = get_spdx_licensing()
expr_str = "(MIT OR Apache-2.0) AND GPL-2.0-only WITH Classpath-exception-2.0"
parsed_expr = licensing.parse(expr_str, validate=True)
valid_check = licensing.validate(expr_str)
unknown_check = licensing.validate("MIT AND NotARealLicense-9.9")
syntax_err_msg = ""
try:
    licensing.parse("MIT AND OR Apache-2.0")
except ExpressionError as exc:
    syntax_err_msg = str(exc)

license_out = {
    "canonical": str(parsed_expr),
    "keys": [s.key for s in licensing.license_symbols(parsed_expr, unique=True, decompose=True)],
    "valid_errors": list(valid_check.errors),
    "unknown_symbols": list(unknown_check.unknown_symbols),
    "unknown_errors": list(unknown_check.errors),
    "syntax_error": syntax_err_msg,
}

# 4. graphql-core: local schema + operation with fragment, variables, valid & misspelled field
schema = build_schema(Path("schema.graphql").read_text(encoding="utf-8"))
doc = gql_parse(Path("operation.graphql").read_text(encoding="utf-8"))
selected_fields = []
for defn in doc.definitions:
    if hasattr(defn, "selection_set") and defn.selection_set:
        stack = list(defn.selection_set.selections)
        while stack:
            sel = stack.pop(0)
            if sel.kind == "field":
                selected_fields.append(sel.name.value)
                if sel.selection_set:
                    stack.extend(sel.selection_set.selections)
            elif sel.kind == "fragment_spread":
                selected_fields.append("..." + sel.name.value)

gql_errors = gql_validate(schema, doc)
gql_err_dicts = [
    {
        "message": e.message,
        "locations": [{"line": loc.line, "column": loc.column} for loc in (e.locations or [])],
    }
    for e in gql_errors
]
valid_op = gql_parse(
    "query GetProject($projectId: ID!) { project(id: $projectId) { ...ProjectSummary } } "
    "fragment ProjectSummary on Project { id name }"
)
valid_gql_errors = gql_validate(schema, valid_op)

# 5. tinycss2: at-rule, custom property with quoted semicolon, nested rule, source locations, malformed error token
rules = tinycss2.parse_stylesheet(Path("sample.css").read_text(encoding="utf-8"), skip_whitespace=True)
at_rule = rules[0]
assert at_rule.type == "at-rule" and at_rule.lower_at_keyword == "media"
nested_rules = tinycss2.parse_rule_list(at_rule.content, skip_whitespace=True)
card_rule = nested_rules[0]
assert card_rule.type == "qualified-rule"
decls = [
    d for d in tinycss2.parse_declaration_list(card_rule.content, skip_whitespace=True)
    if d.type == "declaration"
]
decl_map = {}
for d in decls:
    non_ws = [t for t in d.value if t.type != "whitespace"]
    decl_map[d.name] = {
        "line": d.source_line,
        "column": d.source_column,
        "token_types": [t.type for t in non_ws],
        "serialized": tinycss2.serialize(non_ws).strip(),
    }

bad_rules = tinycss2.parse_stylesheet(Path("malformed.css").read_text(encoding="utf-8"), skip_whitespace=True)
bad_tokens = []
for r in bad_rules:
    if r.type == "error":
        bad_tokens.append(r.kind)
    elif hasattr(r, "content") and r.content:
        for tok in r.content:
            if tok.type == "error":
                bad_tokens.append(tok.kind)

out = {
    "univers": univers_out,
    "jproperties": jprops_out,
    "license": license_out,
    "graphql": {
        "selected_fields": selected_fields,
        "errors": gql_err_dicts,
        "valid_error_count": len(valid_gql_errors),
    },
    "tinycss2": {
        "at_keyword": at_rule.lower_at_keyword,
        "at_line": at_rule.source_line,
        "card_line": card_rule.source_line,
        "decls": decl_map,
        "malformed_error_kinds": bad_tokens,
    },
}
Path("group8_out.json").write_text(json.dumps(out, sort_keys=True), encoding="utf-8")
print("GROUP8_OK")
`)
		if !strings.Contains(resp.Stdout, "GROUP8_OK") {
			t.Fatalf("unexpected stdout: %q", resp.Stdout)
		}

		raw, err := os.ReadFile(filepath.Join(ws, "group8_out.json"))
		contractcheck.FailErr(t, "read group8_out.json", err)
		var out struct {
			Univers map[string]struct {
				Native  string          `json:"native"`
				Vers    string          `json:"vers"`
				Results map[string]bool `json:"results"`
			} `json:"univers"`
			JProperties map[string]string `json:"jproperties"`
			License     struct {
				Canonical      string   `json:"canonical"`
				Keys           []string `json:"keys"`
				ValidErrors    []string `json:"valid_errors"`
				UnknownSymbols []string `json:"unknown_symbols"`
				UnknownErrors  []string `json:"unknown_errors"`
				SyntaxError    string   `json:"syntax_error"`
			} `json:"license"`
			GraphQL struct {
				SelectedFields []string `json:"selected_fields"`
				Errors         []struct {
					Message   string `json:"message"`
					Locations []struct {
						Line   int `json:"line"`
						Column int `json:"column"`
					} `json:"locations"`
				} `json:"errors"`
				ValidErrorCount int `json:"valid_error_count"`
			} `json:"graphql"`
			TinyCSS2 struct {
				AtKeyword string `json:"at_keyword"`
				AtLine    int    `json:"at_line"`
				CardLine  int    `json:"card_line"`
				Decls     map[string]struct {
					Line       int      `json:"line"`
					Column     int      `json:"column"`
					TokenTypes []string `json:"token_types"`
					Serialized string   `json:"serialized"`
				} `json:"decls"`
				MalformedErrorKinds []string `json:"malformed_error_kinds"`
			} `json:"tinycss2"`
		}
		contractcheck.FailErr(t, "unmarshal group8_out.json", json.Unmarshal(raw, &out))

		// 1. Verify univers against frozen registry-native oracles + vers syntax distinction.
		universOracles := map[string]struct {
			versPrefix string
			expected   map[string]bool
		}{
			"npm_caret_1x": {
				versPrefix: "vers:npm/",
				expected: map[string]bool{
					"1.2.3":         true,
					"1.9.0":         true,
					"1.2.2":         false,
					"2.0.0":         false,
					"1.2.3-alpha.1": false,
				},
			},
			"npm_caret_0x": {
				versPrefix: "vers:npm/",
				expected: map[string]bool{
					"0.2.3":      true,
					"0.2.9":      true,
					"0.2.2":      false,
					"0.3.0":      false,
					"0.2.3-rc.1": false,
				},
			},
			"npm_caret_00x": {
				versPrefix: "vers:npm/",
				expected: map[string]bool{
					"0.0.3":         true,
					"0.0.2":         false,
					"0.0.4":         false,
					"0.0.3-alpha.1": false,
				},
			},
			"maven_half_open": {
				versPrefix: "vers:maven/",
				expected: map[string]bool{
					"1.0":         true,
					"1.5":         true,
					"2.0":         false,
					"0.9":         false,
					"1.0-alpha-1": false,
				},
			},
			"maven_open_closed": {
				versPrefix: "vers:maven/",
				expected: map[string]bool{
					"1.0": false,
					"1.5": true,
					"2.0": true,
					"2.1": false,
				},
			},
			"gem_pessimistic_2": {
				versPrefix: "vers:gem/",
				expected: map[string]bool{
					"2.1.0":     true,
					"2.9.4":     true,
					"2.0.9":     false,
					"3.0.0":     false,
					"2.1.0.pre": false,
				},
			},
			"gem_pessimistic_3": {
				versPrefix: "vers:gem/",
				expected: map[string]bool{
					"2.1.3": true,
					"2.1.9": true,
					"2.1.2": false,
					"2.2.0": false,
				},
			},
		}
		for name, oracle := range universOracles {
			got, ok := out.Univers[name]
			if !ok {
				t.Fatalf("univers missing case %q", name)
			}
			if !strings.HasPrefix(got.Vers, oracle.versPrefix) || got.Vers == got.Native {
				t.Fatalf("univers %s failed to distinguish native=%q from vers=%q", name, got.Native, got.Vers)
			}
			for ver, want := range oracle.expected {
				if got.Results[ver] != want {
					t.Fatalf("univers %s (%s -> %s) version %q: got %v want %v", name, got.Native, got.Vers, ver, got.Results[ver], want)
				}
			}
		}

		// 2. Verify jproperties against java.util.Properties.load(InputStream) golden.
		wantProps := map[string]string{
			"escaped:key=name": "value:with=separators",
			"continued.value":  "first part second part third part",
			"unicode.greeting": "Café ☃",
			"duplicate.key":    "second_value",
			"colon.sep":        "alpha",
		}
		if len(out.JProperties) != len(wantProps) {
			t.Fatalf("jproperties key count mismatch: got %+v want %+v", out.JProperties, wantProps)
		}
		for k, wantV := range wantProps {
			if out.JProperties[k] != wantV {
				t.Fatalf("jproperties[%q]: got %q want %q", k, out.JProperties[k], wantV)
			}
		}

		// 3. Verify license-expression canonical form, unknown ID vs syntax error distinction.
		if len(out.License.ValidErrors) != 0 {
			t.Fatalf("valid SPDX expression produced errors: %v", out.License.ValidErrors)
		}
		if out.License.Canonical != "(MIT OR Apache-2.0) AND GPL-2.0-only WITH Classpath-exception-2.0" {
			t.Fatalf("unexpected SPDX canonical expression: %q", out.License.Canonical)
		}
		gotKeys := append([]string(nil), out.License.Keys...)
		sort.Strings(gotKeys)
		wantKeys := []string{"Apache-2.0", "Classpath-exception-2.0", "GPL-2.0-only", "MIT"}
		if strings.Join(gotKeys, "|") != strings.Join(wantKeys, "|") {
			t.Fatalf("SPDX license keys mismatch: got %v want %v", gotKeys, wantKeys)
		}
		if len(out.License.UnknownSymbols) != 1 || out.License.UnknownSymbols[0] != "NotARealLicense-9.9" {
			t.Fatalf("SPDX unknown symbol check failed: %+v", out.License)
		}
		if out.License.SyntaxError == "" {
			t.Fatalf("expected syntax error on invalid SPDX expression 'MIT AND OR Apache-2.0'")
		}

		// 4. Verify graphql-core selected fields and exact validation error location.
		gotFields := strings.Join(out.GraphQL.SelectedFields, ",")
		for _, wantField := range []string{"project", "...ProjectSummary", "nonExistentField", "id", "name"} {
			if !strings.Contains(gotFields, wantField) {
				t.Fatalf("graphql selected fields %q missing %q", gotFields, wantField)
			}
		}
		if out.GraphQL.ValidErrorCount != 0 {
			t.Fatalf("valid GraphQL operation failed validation with %d errors", out.GraphQL.ValidErrorCount)
		}
		if len(out.GraphQL.Errors) != 1 ||
			!strings.Contains(out.GraphQL.Errors[0].Message, "nonExistentField") ||
			len(out.GraphQL.Errors[0].Locations) != 1 ||
			out.GraphQL.Errors[0].Locations[0].Line != 4 ||
			out.GraphQL.Errors[0].Locations[0].Column != 5 {
			t.Fatalf("graphql validation error/location mismatch: %+v", out.GraphQL.Errors)
		}

		// 5. Verify tinycss2 at-rule, quoted-semicolon custom property, source locations, and error token.
		if out.TinyCSS2.AtKeyword != "media" || out.TinyCSS2.AtLine != 1 || out.TinyCSS2.CardLine != 2 {
			t.Fatalf("tinycss2 rule/location mismatch: %+v", out.TinyCSS2)
		}
		banner, ok := out.TinyCSS2.Decls["--banner-text"]
		if !ok || banner.Line != 3 || banner.Serialized != `"alpha; beta"` || strings.Join(banner.TokenTypes, ",") != "string" {
			t.Fatalf("tinycss2 custom property quoted-semicolon mismatch: %+v", banner)
		}
		colorDecl, ok := out.TinyCSS2.Decls["color"]
		if !ok || colorDecl.Line != 4 || colorDecl.Serialized != "#112233" {
			t.Fatalf("tinycss2 color declaration mismatch: %+v", colorDecl)
		}
		if len(out.TinyCSS2.MalformedErrorKinds) == 0 {
			t.Fatalf("tinycss2 failed to emit error token on malformed.css")
		}
	})
}
