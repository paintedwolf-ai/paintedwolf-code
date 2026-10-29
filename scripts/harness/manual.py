#!/usr/bin/env python3
"""Supply real assistant tool calls to an isolated manual-model harness."""

import argparse
import json
import os
from pathlib import Path
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid


class Client:
    def __init__(self, url, token):
        parsed = urllib.parse.urlsplit(url)
        if parsed.scheme != "http" or parsed.hostname not in ("127.0.0.1", "localhost", "::1") or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in ("", "/"):
            raise ValueError("URL must be an HTTP loopback origin for an isolated harness")
        self.url = url.rstrip("/")
        self.token = token

    def request(self, path, payload=None):
        data = None if payload is None else json.dumps(payload).encode()
        req = urllib.request.Request(self.url + path, data=data, headers={
            "Authorization": "Bearer " + self.token, "Content-Type": "application/json",
        })
        try:
            with urllib.request.urlopen(req, timeout=40) as response:
                return json.load(response)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"{req.get_method()} {path}: HTTP {error.code}: {error.read().decode(errors='replace')}") from error

    def pending(self, session, wait=30000):
        query = urllib.parse.urlencode({"session_id": session, "wait": wait})
        return self.request("/harness/llm/pending?" + query)

    def respond(self, pending, **response):
        return self.request("/harness/llm/respond", {"id": pending["id"], **response})

    def abort(self, session):
        return self.request(f"/v1/sessions/{session}/abort", {})


def open_session(client, session=None, project=None):
    if session:
        uuid.UUID(session)
        current = client.request(f"/v1/sessions/{session}")
        if current["status"] != "idle":
            raise ValueError("selected session must be idle")
        return current
    if project:
        uuid.UUID(project)
    else:
        project = client.request("/v1/projects", {"name": "Manual command testing", "draft": True})["id"]
    current = client.request("/v1/sessions", {"project_id": project, "posture": "build"})
    while current["status"] == "preparing":
        time.sleep(0.25)
        current = client.request("/v1/sessions/" + current["id"])
    if current["status"] != "idle":
        raise RuntimeError("session preparation failed: " + json.dumps(current))
    return current


def format_json(value):
    return json.dumps(value, ensure_ascii=False, indent=2)


class Driver:
    def __init__(self, client, session, output=print):
        self.client = client
        self.session = session
        self.output = output
        self.last = None

    def next_request(self):
        while True:
            pending = self.client.pending(self.session)
            if pending["pending"]:
                self.last = pending
                return pending
            state = self.client.request("/v1/sessions/" + self.session)
            if state["status"] in ("idle", "error") and not state.get("prompt_pending"):
                raise RuntimeError("turn ended before a model request; inspect the chat transcript")
            self.output("Waiting for the host. Resolve any approval or question in Den; Ctrl-C stops this session.")

    def invoke(self, name, args):
        # Disable auto-reply before submitting the ordinary user prompt.
        self.client.request("/harness/llm/auto", {"enabled": False})
        try:
            self.client.request(f"/v1/sessions/{self.session}/prompts", {
                "operation_id": str(uuid.uuid4()), "text": "Manually test the next assistant tool call.",
            })
            request = self.next_request()
            self.client.respond(request, tool_calls=[{"id": str(uuid.uuid4()), "name": name, "args": args}])
            reply = self.next_request()
            self.output(format_json(reply["messages"]))
            self.client.respond(reply, content="Manual tool test finished.")
        except BaseException:
            self.client.abort(self.session)
            raise
        finally:
            self.client.request("/harness/llm/auto", {"enabled": True})
            # Closing the response is asynchronous; leave no pending turn between inputs.
            while True:
                state = self.client.request("/v1/sessions/" + self.session)
                if state["status"] in ("idle", "error"):
                    break
                time.sleep(0.1)


HELP = """Enter a command program to run it as the assistant's command tool call.
:begin              multiline program; finish with :end (:cancel discards)
:call NAME JSON     another offered tool, such as progress_update or request_tools
:prompt             full last model request, including prompts and schemas
:tools              offered schemas from that request
:help               this help
:quit               leave the idle session; keep its transcript
Approvals and questions stay in Den. No command runs in this terminal's shell."""


def repl(driver, read=input):
    print(HELP)
    while True:
        try:
            line = read("command> ")
        except (EOFError, KeyboardInterrupt):
            return
        if not line.strip():
            continue
        if line == ":quit":
            return
        if line in (":prompt", ":tools"):
            print(format_json(driver.last if line == ":prompt" else (driver.last or {}).get("tool_schemas", [])))
            continue
        if line == ":help":
            print(HELP)
            continue
        name, args = "command", {"command": line}
        try:
            if line == ":begin":
                lines = []
                while True:
                    item = read("... ")
                    if item in (":end", ":cancel"):
                        break
                    lines.append(item)
                if item == ":cancel":
                    continue
                args = {"command": "\n".join(lines)}
            elif line.startswith(":call "):
                name, raw = line[6:].split(None, 1)
                args = json.loads(raw)
                if not isinstance(args, dict):
                    raise ValueError("tool arguments must be a JSON object")
            elif line.startswith(":"):
                raise ValueError("unknown REPL directive; use :help")
            driver.invoke(name, args)
        except (ValueError, RuntimeError, urllib.error.URLError) as error:
            print("Error:", error, file=sys.stderr)
        except (EOFError, KeyboardInterrupt):
            print("\nStopped this manual input.")


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default=os.environ.get("LYCAON_E2E_API_URL"), required=not os.environ.get("LYCAON_E2E_API_URL"))
    parser.add_argument("--token-file", type=Path, help="file containing the harness API token (otherwise LYCAON_E2E_TOKEN)")
    selection = parser.add_mutually_exclusive_group()
    selection.add_argument("--session", help="idle session UUID to reuse")
    selection.add_argument("--project", help="project UUID for a new build session")
    args = parser.parse_args(argv)
    token = args.token_file.read_text().strip() if args.token_file else os.environ.get("LYCAON_E2E_TOKEN", "e2e-playwright-token")
    client = Client(args.url, token)
    # Prove the manual-only endpoint exists before creating any project.
    client.pending(args.session or "manual-driver-preflight", wait=0)
    session = open_session(client, args.session, args.project)
    print("Project:", session["project_id"], "\nSession:", session["id"], "\nWorkspace:", session.get("workspace_path", "(see project roots in Den)"))
    print("Open this project/chat in the harness Den window to answer approvals.")
    repl(Driver(client, session["id"]))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, RuntimeError, OSError) as error:
        print("manual harness:", error, file=sys.stderr)
        raise SystemExit(1)
