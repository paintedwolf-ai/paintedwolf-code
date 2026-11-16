"""A stand-in for llama.cpp's server in tests: the endpoints the export checks call.

It renders with the template named by FIXTURE_TEMPLATE, tokenizes a character to its
code point with `<bos>` as token 2, and completes with the tokens in FIXTURE_COMPLETION.
"""
import argparse
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path

from jinja2 import Environment


def tokens(text, add_special):
    ids = [2 if piece == "\x02" else ord(piece) for piece in text.replace("<bos>", "\x02")]
    return [2, *ids] if add_special else ids


def render(template, body):
    def reject(message):
        raise ValueError(message)
    environment = Environment()
    environment.globals["raise_exception"] = reject
    messages = [dict(message, tool_calls=[dict(call, function=dict(call["function"], arguments=json.loads(
        call["function"]["arguments"]))) for call in message["tool_calls"]]) if message.get("tool_calls") else message
                for message in body["messages"]]
    return environment.from_string(template).render(messages=messages, tools=body.get("tools", []), bos_token="",
                                                    add_generation_prompt=body["add_generation_prompt"])


class Handler(BaseHTTPRequestHandler):
    def reply(self, value):
        raw = json.dumps(value).encode()
        self.send_response(200)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        template = Path(os.environ["FIXTURE_TEMPLATE"]).read_text()
        self.reply({"status": "ok"} if self.path == "/health" else
                   {"chat_template": template.rstrip("\n"), "bos_token": "<bos>", "build_info": "fixture"})

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if self.path == "/tokenize":
            self.reply({"tokens": tokens(body["content"], body["add_special"])})
        elif self.path == "/apply-template":
            self.reply({"prompt": render(Path(os.environ["FIXTURE_TEMPLATE"]).read_text(), body)})
        elif self.path == "/completion":
            completion = json.loads(Path(os.environ["FIXTURE_COMPLETION"]).read_text())
            self.reply({"tokens": completion[json.dumps(body["prompt"])][:body["n_predict"]]})
        else:
            self.send_error(404)

    def log_message(self, *_):
        pass


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--port", type=int, required=True)
    port = parser.parse_known_args()[0].port
    if os.environ.get("FIXTURE_REFUSES_TO_LOAD"):
        raise SystemExit(1)
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()
