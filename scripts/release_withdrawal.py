"""Withdraw package and website discovery using a validated feed plan."""
from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import time
import urllib.error
import urllib.request

from release_distribution import PUBLIC_READ_HEADERS, cask_version
from release_distribution_contract import confirmation
from release_distribution_contract.ledger import require_publishable
from release_semver import parse
from release_distribution_contract.withdrawal import identities
from release_distribution_contract.halt_plan import FIELDS, FACTS


def api(token: str, endpoint: str, method: str = "GET", body: dict | None = None) -> dict | None:
    request = urllib.request.Request("https://api.github.com/" + endpoint,
        data=None if body is None else json.dumps(body).encode(), method=method,
        headers={"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json",
                 "Content-Type": "application/json", "X-GitHub-Api-Version": "2022-11-28"})
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read(2 * 1024 * 1024 + 1)
            if len(raw) > 2 * 1024 * 1024:
                raise ValueError("GitHub release metadata exceeds its bound")
            value = json.loads(raw) if raw else None
            if method == "GET" and not isinstance(value, dict):
                raise ValueError("present GitHub GET metadata must be a JSON object")
            return value
    except urllib.error.HTTPError as error:
        if method == "GET" and error.code == 404:
            return None
        raise ValueError(f"GitHub {method} failed for {endpoint}: HTTP {error.code}") from error


def download(key: str) -> bytes:
    url = os.environ["DOWNLOAD_BASE_URL"].rstrip("/") + "/" + key
    request = urllib.request.Request(url, headers=PUBLIC_READ_HEADERS)
    with urllib.request.urlopen(request, timeout=30) as response:
        value = response.read(2 * 1024 * 1024 + 1)
        if len(value) > 2 * 1024 * 1024:
            raise ValueError("release metadata exceeds its bound")
        return value


def token_for(channel: str) -> str:
    if channel not in ("stable", "preview"):
        raise ValueError("invalid distribution channel")
    return "painted-wolf-code" + ("@preview" if channel == "preview" else "")


def website_receipt(channel):
    request = urllib.request.Request(f"https://paintedwolf.ai/.well-known/releases/{channel}.json", headers=PUBLIC_READ_HEADERS)
    with urllib.request.urlopen(request, timeout=30) as response:
        confirmation.no_store(response.headers)
        raw = response.read(16385)
    if len(raw) > 16384:
        raise ValueError("website receipt exceeds its bound")
    receipt = json.loads(raw)
    expected = {"channel": channel, **{key: receipt.get(key) for key in ("version", "manifest_sha256", "withdrawn_version")}}
    if receipt.get("schema_version") == 2:
        expected["distribution_sha256"] = receipt.get("distribution_sha256")
    confirmation.validate_website_receipt(receipt, expected)
    return receipt




def mirror_control(release, version):
    parse(version)
    if (not isinstance(release, dict) or not {'id', 'tag_name', 'draft', 'body'} <= release.keys()
            or type(release['id']) is not int or release['id'] <= 0 or release['tag_name'] != 'v' + version
            or type(release['draft']) is not bool or type(release.get('immutable', False)) is not bool
            or release['body'] is not None and not isinstance(release['body'], str)):
        raise ValueError('GitHub mirror lacks its typed release identity or edited control facts')
    return {key: release[key] for key in ('id', 'tag_name', 'draft', 'body')} | {'immutable': release.get('immutable', False)}


def prepare(distribution: dict, *, receipt=None, binding=None) -> dict:
    from release_distribution_contract.halt_plan import validate
    validate(distribution)
    receipt, binding = receipt or website_receipt, binding or identities
    tap = os.environ["HOMEBREW_TAP_REPO"]
    tap_token, website_token = os.environ["HOMEBREW_TAP_TOKEN"], os.environ["WWW_DISPATCH_TOKEN"]
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", tap) or not tap_token or not website_token:
        raise ValueError("configure the tap repository and publication credentials")
    if api(tap_token, f"repos/{tap}") is None or api(website_token, "repos/paintedwolf-ai/paintedwolf-www") is None:
        raise ValueError("publication credentials cannot access the tap or website")
    rows = []
    for row in distribution["channels"]:
        channel, bad, good = row["channel"], row["bad"], row["last_good"]
        parse(bad)
        token = token_for(channel)
        path = f"repos/{tap}/contents/Casks/{token}.rb"
        current = api(tap_token, path)
        tap_action = "keep"
        if current is not None:
            current_bytes = base64.b64decode(current["content"])
            current_version = cask_version(current_bytes)
            if current_version == bad:
                if current_bytes != download(f"release-metadata/{bad}/{token}.rb"):
                    raise ValueError(f"{channel} cask differs from its immutable release record")
                tap_action = "withdraw"
        advertised = receipt(channel)
        website_action = "withdraw" if advertised["version"] == bad else "keep"
        replacement = None
        manifest_digest = row["last_good_manifest_sha256"]
        if good is not None and (tap_action == "withdraw" or website_action == "withdraw"):
            if good in distribution["bad_versions"]:
                raise ValueError("a withdrawal cannot restore another version in the same plan")
            require_publishable(good)
            if channel == "stable" and parse(good).channel != "stable":
                raise ValueError("Stable cannot restore a prerelease")
            if tap_action == "withdraw":
                replacement = download(f"release-metadata/{good}/{token}.rb")
            checksum = download(f"releases/v{good}/painted-wolf-code_v{good}_darwin-aarch64.dmg.sha256").decode().strip()
            if not re.fullmatch(r"[0-9a-f]{64}", checksum) or replacement is not None and (f'version "{good}"'.encode() not in replacement or f'sha256 "{checksum}"'.encode() not in replacement):
                raise ValueError("last-good cask does not match the immutable package checksum")
            if hashlib.sha256(download(f"updates/releases/{good}.json")).hexdigest() != manifest_digest:
                raise ValueError("last-good updater differs from the common halt plan")
        document = binding(row, advertised) if website_action == "withdraw" else {
            "distribution_sha256": row["distribution_sha256"], "bad_distribution_sha256": None,
            "last_good_legacy": row["last_good_legacy"], "bad_legacy": row["bad_legacy"]}
        rows.append({**row, **document, "tap_action": tap_action, "website_action": website_action, "advertised": advertised, "channel": channel, "bad": bad, "last_good": good, "path": path,
                     "sha": current["sha"] if tap_action == "withdraw" else None,
                     "content": base64.b64encode(replacement).decode() if replacement is not None else None,
                     "manifest_sha256": manifest_digest})
    mirrors = []
    repository, token = os.environ["GITHUB_REPOSITORY"], os.environ["GH_TOKEN"]
    for bad in distribution["bad_versions"]:
        published = api(token, f"repos/{repository}/releases/tags/v{bad}")
        control = mirror_control(published, bad) if published is not None else None
        mirrors.append({"version": bad, "action": "withdraw" if control is not None and not control["draft"] else "keep",
                        "release": control})
    return {"bad_versions": distribution["bad_versions"], "channels": rows, "mirrors": mirrors}


def wait_for_withdrawal(row: dict, seconds: int = 1800) -> None:
    deadline = time.monotonic() + seconds
    while True:
        stored = api(os.environ["WWW_DISPATCH_TOKEN"], "repos/paintedwolf-ai/paintedwolf-www/contents/state.json?ref=release-state")
        if stored is not None:
            state = json.loads(base64.b64decode(stored["content"]))
            channel = state["channels"][row["channel"]]
            if row["bad"] in channel["withdrawn_versions"]:
                release = channel["release"]
                if release is not None and release["version"] == row["bad"]:
                    raise ValueError("website still advertises the withdrawn version")
                expected = {"channel": row["channel"], "version": release["version"] if release else None,
                    "manifest_sha256": release["manifest_sha256"] if release else None,
                    "distribution_sha256": release.get("distribution_sha256") if release else None,
                    "withdrawn_version": channel["withdrawn_version"]}
                legacy = (release is not None and release["version"] == row["last_good"] and row["last_good_legacy"])
                if release is not None and release.get("distribution_sha256") is None and not legacy:
                    raise ValueError("website restored an unclassified legacy release")
                confirmation.wait_for_website("https://paintedwolf.ai", expected, legacy_catalog=legacy)
                return
        if time.monotonic() >= deadline:
            raise ValueError("website has not recorded the withdrawal")
        time.sleep(min(5, max(0, deadline - time.monotonic())))


def apply(prepared: dict) -> None:
    for row in prepared['mirrors']:
        parse(row['version'])
        selected = row['release']
        if selected is not None and mirror_control(selected, row['version']) != selected:
            raise ValueError('prepared GitHub mirror control has unsupported fields')
        if row['action'] != ('withdraw' if selected is not None and not selected['draft'] else 'keep'):
            raise ValueError('GitHub mirror action differs from its prepared public state')
        current = api(os.environ['GH_TOKEN'], f"repos/{os.environ['GITHUB_REPOSITORY']}/releases/tags/v{row['version']}")
        actual = mirror_control(current, row['version']) if current is not None else None
        if actual != selected:
            raise ValueError('package mirror control changed after complete halt preparation')
    for row in prepared["channels"]:
        if row["website_action"] == "withdraw":
            actual = identities({key: row[key] for key in FIELDS | FACTS}, row["advertised"])
            if any(row.get(key) != value for key, value in actual.items()):
                raise ValueError("halt distribution identities changed after preparation")
    errors = []
    for row in prepared["channels"]:
        try:
            body = {"message": f"withdraw {row['bad']} from {row['channel']}"}
            if row["sha"]:
                body["sha"] = row["sha"]
            if row["tap_action"] == "withdraw" and row["content"] is not None:
                body["content"] = row["content"]
                api(os.environ["HOMEBREW_TAP_TOKEN"], row["path"], "PUT", body)
            elif row["tap_action"] == "withdraw" and row["sha"]:
                api(os.environ["HOMEBREW_TAP_TOKEN"], row["path"], "DELETE", body)
        except (OSError, ValueError) as error:
            errors.append(str(error))
        if row["website_action"] == "keep":
            continue
        try:
            payload = {"channel": row["channel"], "bad_version": row["bad"], "last_good_version": row["last_good"],
                       "manifest_sha256": row["manifest_sha256"],
                       "distribution_sha256": row["distribution_sha256"],
                       "bad_distribution_sha256": row["bad_distribution_sha256"],
                       "last_good_legacy": row["last_good_legacy"], "bad_legacy": row["bad_legacy"]}
            api(os.environ["WWW_DISPATCH_TOKEN"], "repos/paintedwolf-ai/paintedwolf-www/dispatches", "POST",
                {"event_type": "painted-wolf-release-halt", "client_payload": payload})
            wait_for_withdrawal(row)
        except (OSError, ValueError) as error:
            errors.append(str(error))
    for row in prepared["mirrors"]:
        if row["action"] == "keep":
            continue
        try:
            bad, release = row["version"], row["release"]
            repository, token = os.environ["GITHUB_REPOSITORY"], os.environ["GH_TOKEN"]
            current = api(token, f"repos/{repository}/releases/tags/v{bad}")
            if current is None or mirror_control(current, bad) != release:
                raise ValueError("package mirror changed after complete halt preparation")
            notice = "This release has been withdrawn. Use the current release from https://paintedwolf.ai.\n\n"
            notes = release.get("body") or ""
            body = {"make_latest": "false", "name": f"Withdrawn: Painted Wolf Code v{bad}",
                    "body": notes if notes.startswith(notice) else notice + notes}
            if not release.get("immutable", False):
                body["draft"] = True
            api(token, f"repos/{repository}/releases/{release['id']}", "PATCH", body)
        except (OSError, ValueError) as error:
            errors.append(str(error))
    if errors:
        raise ValueError("withdrawal needs a retry: " + "; ".join(errors))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("operation", choices=("prepare", "apply"))
    parser.add_argument("--file", type=Path, required=True)
    parser.add_argument("--output", type=Path)
    args = parser.parse_args()
    value = json.loads(args.file.read_text())
    if args.operation == "prepare":
        if args.output is None:
            parser.error("prepare requires --output")
        args.output.write_text(json.dumps(prepare(value), indent=2) + "\n")
    else:
        apply(value)


if __name__ == "__main__":
    main()
