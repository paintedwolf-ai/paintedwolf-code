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

from release_distribution import PUBLIC_READ_HEADERS, cask_version, require_publishable, wait_for_website
from release_semver import parse


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
            return json.loads(raw) if raw else None
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


def prepare(distribution: dict) -> dict:
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
        change_tap = False
        if current is not None:
            current_bytes = base64.b64decode(current["content"])
            current_version = cask_version(current_bytes)
            if current_version == bad:
                if current_bytes != download(f"release-metadata/{bad}/{token}.rb"):
                    raise ValueError(f"{channel} cask differs from its immutable release record")
                change_tap = True
        replacement = None
        manifest_digest = None
        if good is not None:
            if good in distribution["bad_versions"]:
                raise ValueError("a withdrawal cannot restore another version in the same plan")
            require_publishable(good)
            if channel == "stable" and parse(good).channel != "stable":
                raise ValueError("Stable cannot restore a prerelease")
            replacement = download(f"release-metadata/{good}/{token}.rb")
            checksum = download(f"releases/v{good}/painted-wolf-code_v{good}_darwin-aarch64.dmg.sha256").decode().strip()
            if not re.fullmatch(r"[0-9a-f]{64}", checksum) or f'version "{good}"'.encode() not in replacement or f'sha256 "{checksum}"'.encode() not in replacement:
                raise ValueError("last-good cask does not match the immutable package checksum")
            manifest_digest = hashlib.sha256(download(f"updates/releases/{good}.json")).hexdigest()
        rows.append({"channel": channel, "bad": bad, "last_good": good, "path": path,
                     "sha": current["sha"] if change_tap else None, "change_tap": change_tap,
                     "content": base64.b64encode(replacement).decode() if replacement is not None else None,
                     "manifest_sha256": manifest_digest})
    return {"bad_versions": distribution["bad_versions"], "channels": rows}


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
                wait_for_website("https://paintedwolf.ai", {"channel": row["channel"],
                    "version": release["version"] if release else None,
                    "manifest_sha256": release["manifest_sha256"] if release else None,
                    "withdrawn_version": channel["withdrawn_version"]})
                return
        if time.monotonic() >= deadline:
            raise ValueError("website has not recorded the withdrawal")
        time.sleep(min(5, max(0, deadline - time.monotonic())))


def apply(prepared: dict) -> None:
    errors = []
    for row in prepared["channels"]:
        try:
            body = {"message": f"withdraw {row['bad']} from {row['channel']}"}
            if row["sha"]:
                body["sha"] = row["sha"]
            if row["change_tap"] and row["content"] is not None:
                body["content"] = row["content"]
                api(os.environ["HOMEBREW_TAP_TOKEN"], row["path"], "PUT", body)
            elif row["change_tap"] and row["sha"]:
                api(os.environ["HOMEBREW_TAP_TOKEN"], row["path"], "DELETE", body)
        except (OSError, ValueError) as error:
            errors.append(str(error))
        try:
            payload = {"channel": row["channel"], "bad_version": row["bad"], "last_good_version": row["last_good"],
                       "manifest_sha256": row["manifest_sha256"]}
            api(os.environ["WWW_DISPATCH_TOKEN"], "repos/paintedwolf-ai/paintedwolf-www/dispatches", "POST",
                {"event_type": "lycaon-release-halt", "client_payload": payload})
            wait_for_withdrawal(row)
        except (OSError, ValueError) as error:
            errors.append(str(error))
    for bad in prepared["bad_versions"]:
        try:
            repository = os.environ["GITHUB_REPOSITORY"]
            token = os.environ["GH_TOKEN"]
            release = api(token, f"repos/{repository}/releases/tags/v{bad}")
            if release:
                # Immutable releases retain their assets after withdrawal.
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
