#!/usr/bin/env python3
"""Synthetic fixture project and loopback HTTP server for live command harness."""

from __future__ import annotations

import http.server
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import sys

TD = "TO" + "DO"

APP_LOG_LINES = [
    "2026-10-21T08:00:00Z INFO timeout talking to cache",
    "2026-10-21T08:00:01Z WARN request served",
    "2026-10-21T08:00:02Z INFO connect db",
    "2026-10-21T08:00:03Z WARN connect db",
    "2026-10-21T08:00:04Z INFO retrying request",
    "2026-10-21T08:00:05Z INFO retrying request",
    "2026-10-21T08:00:06Z INFO connect db",
    "2026-10-21T08:00:07Z INFO disk nearly full",
    "2026-10-21T08:00:08Z WARN connect db",
    "2026-10-21T08:00:09Z INFO connect db",
    "2026-10-21T08:00:10Z WARN disk nearly full",
    "2026-10-21T08:00:11Z INFO retrying request",
    "2026-10-21T08:00:12Z INFO timeout talking to cache",
    "2026-10-21T08:00:13Z ERROR request served",
    "2026-10-21T08:00:14Z ERROR connect db",
    "2026-10-21T08:00:15Z ERROR retrying request",
    "2026-10-21T08:00:16Z WARN connect db",
    "2026-10-21T08:00:17Z INFO connect db",
    "2026-10-21T08:00:18Z WARN timeout talking to cache",
    "2026-10-21T08:00:19Z INFO disk nearly full",
    "2026-10-21T08:00:20Z INFO retrying request",
    "2026-10-21T08:00:21Z INFO retrying request",
    "2026-10-21T08:00:22Z INFO retrying request",
    "2026-10-21T08:00:23Z ERROR timeout talking to cache",
    "2026-10-21T08:00:24Z INFO retrying request",
    "2026-10-21T08:00:25Z ERROR request served",
    "2026-10-21T08:00:26Z INFO user login",
    "2026-10-21T08:00:27Z INFO retrying request",
    "2026-10-21T08:00:28Z INFO retrying request",
    "2026-10-21T08:00:29Z INFO retrying request",
    "2026-10-21T08:00:30Z INFO disk nearly full",
    "2026-10-21T08:00:31Z ERROR retrying request",
    "2026-10-21T08:00:32Z WARN user login",
    "2026-10-21T08:00:33Z WARN retrying request",
    "2026-10-21T08:00:34Z WARN user login",
    "2026-10-21T08:00:35Z INFO timeout talking to cache",
    "2026-10-21T08:00:36Z INFO request served",
    "2026-10-21T08:00:37Z INFO connect db",
    "2026-10-21T08:00:38Z ERROR user login",
    "2026-10-21T08:00:39Z WARN disk nearly full",
    "2026-10-21T08:00:40Z INFO request served",
    "2026-10-21T08:00:41Z WARN user login",
    "2026-10-21T08:00:42Z ERROR connect db",
    "2026-10-21T08:00:43Z INFO retrying request",
    "2026-10-21T08:00:44Z WARN timeout talking to cache",
    "2026-10-21T08:00:45Z INFO timeout talking to cache",
    "2026-10-21T08:00:46Z WARN disk nearly full",
    "2026-10-21T08:00:47Z INFO request served",
    "2026-10-21T08:00:48Z INFO retrying request",
    "2026-10-21T08:00:49Z ERROR user login",
    "2026-10-21T08:00:50Z INFO request served",
    "2026-10-21T08:00:51Z INFO retrying request",
    "2026-10-21T08:00:52Z WARN retrying request",
    "2026-10-21T08:00:53Z WARN connect db",
    "2026-10-21T08:00:54Z INFO user login",
    "2026-10-21T08:00:55Z WARN request served",
    "2026-10-21T08:00:56Z ERROR connect db",
    "2026-10-21T08:00:57Z INFO request served",
    "2026-10-21T08:00:58Z INFO request served",
    "2026-10-21T08:00:59Z ERROR request served",
    "2026-10-21T08:01:00Z WARN user login",
    "2026-10-21T08:01:01Z WARN request served",
    "2026-10-21T08:01:02Z INFO connect db",
    "2026-10-21T08:01:03Z WARN user login",
    "2026-10-21T08:01:04Z INFO retrying request",
    "2026-10-21T08:01:05Z INFO disk nearly full",
    "2026-10-21T08:01:06Z INFO timeout talking to cache",
    "2026-10-21T08:01:07Z INFO timeout talking to cache",
    "2026-10-21T08:01:08Z INFO disk nearly full",
    "2026-10-21T08:01:09Z WARN disk nearly full",
    "2026-10-21T08:01:10Z INFO timeout talking to cache",
    "2026-10-21T08:01:11Z WARN disk nearly full",
    "2026-10-21T08:01:12Z WARN user login",
    "2026-10-21T08:01:13Z INFO disk nearly full",
    "2026-10-21T08:01:14Z WARN user login",
    "2026-10-21T08:01:15Z WARN user login",
    "2026-10-21T08:01:16Z ERROR disk nearly full",
    "2026-10-21T08:01:17Z INFO timeout talking to cache",
    "2026-10-21T08:01:18Z INFO timeout talking to cache",
    "2026-10-21T08:01:19Z INFO timeout talking to cache",
    "2026-10-21T08:01:20Z ERROR timeout talking to cache",
    "2026-10-21T08:01:21Z INFO disk nearly full",
    "2026-10-21T08:01:22Z ERROR timeout talking to cache",
    "2026-10-21T08:01:23Z INFO user login",
    "2026-10-21T08:01:24Z INFO timeout talking to cache",
    "2026-10-21T08:01:25Z WARN retrying request",
    "2026-10-21T08:01:26Z INFO retrying request",
    "2026-10-21T08:01:27Z ERROR user login",
    "2026-10-21T08:01:28Z INFO request served",
    "2026-10-21T08:01:29Z WARN retrying request",
    "2026-10-21T08:01:30Z ERROR request served",
    "2026-10-21T08:01:31Z INFO disk nearly full",
    "2026-10-21T08:01:32Z ERROR retrying request",
    "2026-10-21T08:01:33Z WARN disk nearly full",
    "2026-10-21T08:01:34Z WARN disk nearly full",
    "2026-10-21T08:01:35Z INFO disk nearly full",
    "2026-10-21T08:01:36Z ERROR disk nearly full",
    "2026-10-21T08:01:37Z INFO timeout talking to cache",
    "2026-10-21T08:01:38Z INFO timeout talking to cache",
    "2026-10-21T08:01:39Z WARN timeout talking to cache",
    "2026-10-21T08:01:40Z INFO user login",
    "2026-10-21T08:01:41Z ERROR connect db",
    "2026-10-21T08:01:42Z INFO connect db",
    "2026-10-21T08:01:43Z ERROR timeout talking to cache",
    "2026-10-21T08:01:44Z WARN connect db",
    "2026-10-21T08:01:45Z INFO retrying request",
    "2026-10-21T08:01:46Z INFO connect db",
    "2026-10-21T08:01:47Z INFO retrying request",
    "2026-10-21T08:01:48Z WARN timeout talking to cache",
    "2026-10-21T08:01:49Z ERROR user login",
    "2026-10-21T08:01:50Z INFO retrying request",
    "2026-10-21T08:01:51Z INFO disk nearly full",
    "2026-10-21T08:01:52Z INFO connect db",
    "2026-10-21T08:01:53Z WARN disk nearly full",
    "2026-10-21T08:01:54Z WARN disk nearly full",
    "2026-10-21T08:01:55Z INFO connect db",
    "2026-10-21T08:01:56Z INFO connect db",
    "2026-10-21T08:01:57Z INFO request served",
    "2026-10-21T08:01:58Z INFO disk nearly full",
    "2026-10-21T08:01:59Z INFO retrying request",
]


def build_fixture_template(dest: Path) -> Path:
    """Create the canonical synthetic Go project fixture with git history and dirty README."""
    if dest.exists():
        shutil.rmtree(dest)
    dest.mkdir(parents=True, exist_ok=True)
    files = {
        "README.txt": (
            "Sanity project for the portable command environment live check.\n"
            "It has a passing package (internal/calc) and a failing one (internal/broken).\n"
        ),
        "go.mod": "module example.com/sanity\n\ngo 1.22\n",
        "main.go": (
            "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/sanity/internal/calc\"\n)\n\n"
            f"// {TD}: accept numbers from flags\nfunc main() {{\n\tfmt.Println(calc.Add(2, 3))\n}}\n"
        ),
        "config.json": '{\n  "name": "sanity",\n  "timeout": 30,\n  "verbose": false,\n  "x": 42\n}\n',
        "cmd/tool/tool.go": (
            "package main\n\nimport (\n\t\"encoding/json\"\n\t\"fmt\"\n\t\"os\"\n)\n\n"
            "type config struct {\n\tName    string `json:\"name\"`\n\tTimeout int    `json:\"timeout\"`\n"
            "\tVerbose bool   `json:\"verbose\"`\n}\n\nfunc main() {\n\tdata, err := os.ReadFile(\"config.json\")\n"
            "\tif err != nil {\n\t\tfmt.Fprintln(os.Stderr, err)\n\t\tos.Exit(1)\n\t}\n\tvar c config\n"
            "\tif err := json.Unmarshal(data, &c); err != nil {\n\t\tfmt.Fprintln(os.Stderr, err)\n\t\tos.Exit(1)\n\t}\n"
            f"\t// {TD}: validate timeout range\n\tfmt.Printf(\"%s %d %v\\n\", c.Name, c.Timeout, c.Verbose)\n}}\n"
        ),
        "internal/calc/calc.go": (
            "package calc\n\n// Add returns a+b.\nfunc Add(a, b int) int { return a + b }\n\n"
            f"// Mul returns a*b.\n// {TD}: handle overflow\nfunc Mul(a, b int) int {{ return a * b }}\n\n"
            "// Sub returns a-b.\nfunc Sub(a, b int) int { return a - b }\n"
        ),
        "internal/calc/calc_test.go": (
            "package calc\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n"
            "\t\tt.Fatal(\"Add\")\n\t}\n}\n\nfunc TestMul(t *testing.T) {\n\tif Mul(4, 5) != 20 {\n"
            "\t\tt.Fatal(\"Mul\")\n\t}\n}\n"
        ),
        "internal/broken/broken.go": (
            f"package broken\n\n// Double should return twice n.\n// {TD}: fix the off-by-one bug\n"
            "func Double(n int) int { return n*2 + 1 }\n"
        ),
        "internal/broken/broken_test.go": (
            "package broken\n\nimport \"testing\"\n\nfunc TestDouble(t *testing.T) {\n"
            "\tif got := Double(4); got != 8 {\n\t\tt.Fatalf(\"Double(4) = %d, want 8\", got)\n\t}\n}\n\n"
            "func TestDoubleZero(t *testing.T) {\n\tif got := Double(0); got != 0 {\n"
            "\t\tt.Fatalf(\"Double(0) = %d, want 0\", got)\n\t}\n}\n"
        ),
        "docs/nested/list.txt": "alpha\nbeta\ngamma\n",
        "docs/nested/deeper/note.txt": "deep note one\ndeep note two\n",
        "docs/nested/deeper/legacy.js": f"export function x() {{}}\n// {TD}: remove legacy shim\n",
        "data/big.bin": "x" * 300000,
        "data/medium.dat": "y" * 40000,
        "data/small.dat": "z" * 100,
        "logs/app.log": "\n".join(APP_LOG_LINES) + "\n",
    }
    for rel, content in files.items():
        path = dest / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)
    git_env = {
        **os.environ,
        "GIT_AUTHOR_NAME": "Tester",
        "GIT_AUTHOR_EMAIL": "t@example.invalid",
        "GIT_COMMITTER_NAME": "Tester",
        "GIT_COMMITTER_EMAIL": "t@example.invalid",
    }
    subprocess.run(["git", "init", "-b", "main"], cwd=dest, env=git_env, check=True, capture_output=True)
    subprocess.run(["git", "add", "."], cwd=dest, env=git_env, check=True, capture_output=True)
    subprocess.run(["git", "commit", "-m", "initial"], cwd=dest, env=git_env, check=True, capture_output=True)
    (dest / "README.txt").write_text(files["README.txt"] + "edited\n")
    return dest


def clone_fixture(template_dir: Path, target_dir: Path) -> Path:
    """Create a clean copy of the fixture template for a single task attempt."""
    if not template_dir.exists():
        build_fixture_template(template_dir)
    if target_dir.exists():
        shutil.rmtree(target_dir)
    target_dir.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(template_dir, target_dir, symlinks=True)
    return target_dir


class LoopbackHandler(http.server.BaseHTTPRequestHandler):
    """Minimal loopback HTTP handler for the curl live task."""

    def do_GET(self):
        if self.path == "/status.json":
            body = json.dumps({"service": "sanity", "healthy": True, "version": "2.4.1"}).encode()
            ctype = "application/json"
        elif self.path == "/":
            body = b"hello from the loopback server\n"
            ctype = "text/plain"
        else:
            self.send_error(404)
            return
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


def serve_httpd(port: int):
    http.server.HTTPServer(("127.0.0.1", port), LoopbackHandler).serve_forever()


def probe_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


if __name__ == "__main__":
    if len(sys.argv) >= 3 and sys.argv[1] == "httpd":
        serve_httpd(int(sys.argv[2]))
    elif len(sys.argv) >= 3 and sys.argv[1] == "init-fixture":
        build_fixture_template(Path(sys.argv[2]))
