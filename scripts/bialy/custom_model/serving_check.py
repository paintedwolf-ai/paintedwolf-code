"""Check that a llama.cpp server renders student requests exactly as training rendered them.

The server must serve the training chat template and turn every served conversation
fixture, sent in the wire form the host dispatches, into the prompt the fixture expects,
with one leading BOS token. Uses only the standard library.
"""
import argparse
import hashlib
import json
from pathlib import Path
import urllib.error
import urllib.request

TEMPLATES = Path(__file__).resolve().parents[3] / "lycaon/internal/generation/templates"
TEMPLATE, CASES = TEMPLATES / "gemma4-code-v1.jinja", TEMPLATES / "gemma4-code-v1.cases.json"
SCHEMA = "pw-serving-render/1"


def call(url, path, body=None):
    request = urllib.request.Request(url + path, None if body is None else json.dumps(body).encode(),
                                     {"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=120) as response:
            return json.load(response)
    except urllib.error.HTTPError as exc:
        raise ValueError("server refused %s: %s" % (path, exc.read().decode(errors="replace")[:500])) from exc


def wire(messages):
    """Messages as the host's chat-completions wire carries them: call arguments are JSON text."""
    return [dict(message, tool_calls=[dict(item, function=dict(item["function"], arguments=json.dumps(
        item["function"]["arguments"], ensure_ascii=False))) for item in message["tool_calls"]])
            if message.get("tool_calls") else message for message in messages]


def check(url, call=call):
    template = TEMPLATE.read_text()
    properties = call(url, "/props")
    # The server keeps the template without its final newline.
    if properties.get("chat_template", "").rstrip("\n") != template.rstrip("\n"):
        raise ValueError("server does not serve the training chat template")
    (bos,) = call(url, "/tokenize", {"content": properties["bos_token"], "add_special": False, "parse_special": True})["tokens"]
    cases = [case for case in json.loads(CASES.read_text()) if case.get("wire")]
    for case in cases:
        body = {"messages": wire(case["messages"]), "add_generation_prompt": True}
        if case["tools"]:
            body["tools"] = case["tools"]
        prompt = call(url, "/apply-template", body)["prompt"]
        # The template prints BOS only for a tokenizer that does not add it itself.
        if prompt not in {case["expected"].replace("<bos>", opening, 1) for opening in ("", properties["bos_token"])}:
            raise ValueError("server renders a conversation differently from training: " + case["name"])
        tokens = call(url, "/tokenize", {"content": prompt, "add_special": True, "parse_special": True})["tokens"]
        if tokens[:1] != [bos] or bos in tokens[1:]:
            raise ValueError("server does not open the prompt with exactly one BOS token: " + case["name"])
    return {"schema": SCHEMA, "template_sha256": hashlib.sha256(template.encode()).hexdigest(),
            "cases_sha256": hashlib.sha256(CASES.read_bytes()).hexdigest(), "cases": [case["name"] for case in cases],
            "build": properties.get("build_info")}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", required=True, help="server root, without /v1")
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()
    args.out.write_text(json.dumps(check(args.url), sort_keys=True) + "\n")
