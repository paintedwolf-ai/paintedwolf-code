"""Confirm every website package link from independently pinned signed distribution bytes."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import tempfile
import urllib.request

from scripts.release_distribution_contract.document_schema import decode, validate
from scripts.feed_signature import check
from scripts.feed_signing import verify
from scripts.update_keys import generation, load_registry


def read(url, maximum):
    request = urllib.request.Request(url, headers={"Cache-Control": "no-cache, no-store",
                                                 "User-Agent": "painted-wolf-release/1"})
    with urllib.request.urlopen(request, timeout=30) as response:
        raw = response.read(maximum + 1)
    if len(raw) > maximum:
        raise ValueError("website distribution metadata exceeds its bound")
    return raw


def public_downloads(receipt):
    version, expected = receipt["version"], receipt["distribution_sha256"]
    if version is None:
        if expected is not None or receipt["manifest_sha256"] is not None:
            raise ValueError("withdrawn website state cannot retain public release digests")
        return []
    if not isinstance(expected, str) or not re.fullmatch("[a-f0-9]{64}", expected):
        raise ValueError("website requires an independent distribution digest")
    from scripts.release_semver import parse
    parse(version)
    base = "https://downloads.paintedwolf.dev/releases/v" + version + "/distribution.v1.json"
    raw = read(base, 2 << 20)
    signature = read(base + ".sig", 65536).decode()
    if hashlib.sha256(raw).hexdigest() != expected:
        raise ValueError("website distribution differs from its independently selected digest")
    document = verify_metadata(raw, signature, version, receipt["manifest_sha256"])
    return sorted(package["url"] for row in document["platforms"].values()
                  for package in row["packages"].values())


def verify_metadata(raw, signature, version, manifest_sha256):
    document = decode(raw)
    validate(document)
    if (document["mode"] != "production" or document["version"] != version
            or document["updater_manifest_sha256"] != manifest_sha256):
        raise ValueError("website distribution differs from the selected release")
    registry = load_registry()
    with tempfile.TemporaryDirectory(prefix="website-distribution-") as temporary:
        path = Path(temporary) / "distribution.v1.json"
        path.write_bytes(raw)
        number = document["signing_generation"]
        check(signature, file=path.name, version=version, number=number, registry=registry)
        verify(path, signature, generation(registry, number)["feed_public_key"])
    return document


NOTIFICATION = ("version", "tag", "channel", "manifest_sha256", "distribution_sha256")


def release_notification(payload):
    """Accept only the exact release payload the document pipeline produced after public verification."""
    if not isinstance(payload, dict) or set(payload) != set(NOTIFICATION):
        raise ValueError("website notification must carry exactly the verified release identities")
    from scripts.release_semver import parse
    if not isinstance(payload["version"], str):
        raise ValueError("website notification version must be a release version")
    parse(payload["version"])
    if payload["tag"] != "v" + payload["version"] or payload["channel"] not in ("stable", "preview"):
        raise ValueError("website notification tag or channel differs from its release version")
    for name in ("manifest_sha256", "distribution_sha256"):
        if not isinstance(payload[name], str) or not re.fullmatch("[a-f0-9]{64}", payload[name]):
            raise ValueError("website notification requires independent updater and distribution digests")
    return payload


def dispatch_body(payload):
    return {"event_type": "painted-wolf-release", "client_payload": release_notification(payload)}


def confirm(payload, generation, base_url="https://paintedwolf.ai", seconds=1800):
    """Wait for the website to acknowledge both digests on every channel this release promotes."""
    from scripts.release_distribution import publication_channels, wait_for_website
    payload = release_notification(payload)
    for channel in publication_channels(payload["channel"], payload["version"], generation):
        wait_for_website(base_url, {"channel": channel, "version": payload["version"],
                                    "manifest_sha256": payload["manifest_sha256"], "withdrawn_version": None,
                                    "distribution_sha256": payload["distribution_sha256"]},
                         seconds, allow_newer=payload["channel"] == "stable" and channel == "preview")


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    body = commands.add_parser("dispatch-body")
    body.add_argument("--notification", type=Path, required=True)
    body.add_argument("--output", type=Path, required=True)
    confirmation = commands.add_parser("confirm")
    confirmation.add_argument("--notification", type=Path, required=True)
    confirmation.add_argument("--base-url", default="https://paintedwolf.ai")
    args = parser.parse_args(arguments)
    try:
        payload = json.loads(args.notification.read_bytes())
        if args.command == "dispatch-body":
            body = dispatch_body(payload)
            with args.output.open("x") as stream:
                json.dump(body, stream)
        else:
            confirm(payload, load_registry()["signing_generation"], args.base_url)
    except (OSError, ValueError, KeyError, TypeError) as error:
        parser.exit(1, "Website release notification refused: " + str(error) + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
