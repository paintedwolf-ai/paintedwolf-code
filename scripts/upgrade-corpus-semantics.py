#!/usr/bin/env python3
"""Capture and verify fixture facts through the running application's readers."""

import argparse
import hashlib
import json
import subprocess
from pathlib import Path
from urllib.request import Request, urlopen


def read_json(path):
    return json.loads(path.read_text())


def request(base, token, path):
    req = Request(base + path, headers={"Authorization": "Bearer " + token})
    with urlopen(req, timeout=30) as response:
        return json.load(response)


def retained_facts(base, token, manifest):
    session_path = "/v1/sessions/" + manifest["session_id"]
    artifact_id = manifest["artifact_id"]
    req = Request(base + session_path + "/artifacts/" + artifact_id,
                  headers={"Authorization": "Bearer " + token})
    with urlopen(req, timeout=30) as response:
        artifact = {"id": artifact_id, "mime": response.headers.get_content_type(),
                    "sha256": hashlib.sha256(response.read()).hexdigest()}
    grants = request(base, token, "/v1/approval-grants")["grants"]
    grant = next((item for item in grants if item["id"] == manifest["grant_id"]), None)
    if grant is None:
        raise ValueError("retained approval grant missing")
    durable_grant = {key: grant.get(key) for key in
                     ("id", "scope", "category", "pattern", "granted_at", "expires_at")}
    return {"artifact": artifact, "grant": durable_grant}


def facts(base, token, manifest):
    project = request(base, token, "/v1/projects/" + manifest["project_id"])
    if project["id"] != manifest["project_id"]:
        raise ValueError("seeded project missing")
    trust = request(base, token, "/v1/projects/" + project["id"] + "/trust")
    trust_read = {"unread_count": trust["unread_count"],
                  "surfaces": {surface["id"]: surface["seen"] for surface in trust["surfaces"]},
                  "review": trust["review"]}
    if (trust_read["unread_count"] != 0 or not all(trust_read["surfaces"].values())
            or not any(change["kind"] == "modified" and change["before"]
                       for change in trust_read["review"]["changes"])):
        raise ValueError("retained trust read baseline missing or changed")
    messages = []
    path = "/v1/sessions/" + manifest["session_id"] + "/messages"
    response = request(base, token, path)
    for message in response["messages"]:
        messages.append({key: message.get(key) for key in ("id", "role", "content")})
    if not any(item["id"] == manifest["tool_result_message_id"] and item["role"] == "tool" for item in messages):
        raise ValueError("tool_result missing")
    return {"project_id": project["id"], "trust_read": trust_read,
            "messages": sorted(messages, key=lambda item: item["id"]),
            **retained_facts(base, token, manifest)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("capture", "verify"))
    parser.add_argument("--base", required=True)
    parser.add_argument("--token-file", required=True, type=Path)
    parser.add_argument("--fixture", required=True, type=Path)
    parser.add_argument("--config-dir", required=True, type=Path)
    args = parser.parse_args()
    manifest = read_json(args.fixture / "MANIFEST.json")
    observed = facts(args.base, args.token_file.read_text().strip(), manifest)
    expected_path = args.fixture / "SEMANTICS.json"
    if args.mode == "capture":
        expected_path.write_text(json.dumps(observed, indent=2, sort_keys=True) + "\n")
        return
    preference_path = Path("app-state-v1/6465627567.json")
    if read_json(args.config_dir / preference_path) != read_json(args.fixture / preference_path):
        raise ValueError("retained application preference changed")
    subprocess.run(["node", str(Path(__file__).with_name("upgrade-editor-fixture.mjs")), "verify",
                    args.base, str(args.config_dir), str(args.fixture / "MANIFEST.json")], check=True)
    expected = read_json(expected_path)
    if observed["project_id"] != expected["project_id"]:
        raise ValueError("retained project identity changed")
    for key in ("artifact", "grant", "trust_read"):
        if observed[key] != expected[key]:
            raise ValueError("retained " + key + " changed")
    actual = {message["id"]: message for message in observed["messages"]}
    for message in expected["messages"]:
        if actual.get(message["id"]) != message:
            raise ValueError("retained transcript content changed: " + message["id"])
    expected_digest = manifest["semantics_sha256"]
    if hashlib.sha256(expected_path.read_bytes()).hexdigest() != expected_digest:
        raise ValueError("fixture semantic expectations changed")


if __name__ == "__main__":
    main()
