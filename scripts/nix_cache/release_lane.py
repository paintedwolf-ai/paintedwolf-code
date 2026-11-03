"""Publish and withdraw the release's Nix closures through the production signed cache."""
from __future__ import annotations

import argparse
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

from . import adapter, publication, release_authority
from .admission import public_key, SYSTEMS, validate_scope
from .r2 import R2Transport

ROOT = Path(__file__).resolve().parents[2]
POLICY = ROOT / "packaging/linux/nix-cache-trust.json"
KEY_NAME = "paintedwolf-cache-1"
SECRET = "NIX_CACHE_SIGNING_KEY"
PROVISION = (f"generate the {KEY_NAME} Nix cache key, store its secret as {SECRET} in the release-publication "
             f"environment, and add its public key to packaging/linux/nix-cache-trust.json")


def trust_policy(path: Path = POLICY) -> dict:
    policy = json.loads(path.read_text())
    if policy.get("public_keys") == []:
        raise ValueError("production Nix cache trust is not provisioned: " + PROVISION)
    if not isinstance(policy.get("public_keys"), list) or any(
            not isinstance(key, str) or public_key(key) != KEY_NAME for key in policy["public_keys"]):
        raise ValueError(f"production Nix cache trust must contain only the {KEY_NAME} public key")
    return policy


def credential(secret: str | None, policy_path: Path = POLICY) -> str:
    """The secret's Ed25519 public half must be the committed production trust key."""
    import base64
    name, separator, encoded = (secret or "").strip().partition(":")
    try:
        raw = base64.b64decode(encoded, validate=True)
    except ValueError:
        raw = b""
    if not secret or name != KEY_NAME or not separator or len(raw) != 64:
        raise ValueError(f"{SECRET} must be the {KEY_NAME} secret written by nix-store --generate-binary-cache-key")
    public = f"{KEY_NAME}:{base64.b64encode(raw[32:]).decode()}"
    if public not in trust_policy(policy_path)["public_keys"]:
        raise ValueError(f"{SECRET} does not match the public key in packaging/linux/nix-cache-trust.json")
    return public


def linux_public(catalog: Path = ROOT / "packaging/release-platforms.json") -> bool:
    return any(row["goos"] == "linux" and row["publication"] == "public"
               for row in json.loads(catalog.read_text())["platforms"])


@contextmanager
def signing_key():
    value = os.environ.pop(SECRET, None)
    if not value:
        raise ValueError(f"{SECRET} is required for production Nix cache publication: " + PROVISION)
    with tempfile.TemporaryDirectory(prefix="paintedwolf-nix-cache-key-") as directory:
        path = Path(directory) / "signing.key"
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w") as output:
            output.write(value.strip())
        yield path


def request(source_commit: str, version: str, system: str, outputs: list[str], lock: Path, *,
            rehearsal=False, cache_prefix='nix-cache') -> dict:
    if type(rehearsal) is not bool:
        raise ValueError('Nix cache requires an explicit trust mode')
    return {"schema_version": 1, "mode": "rehearsal" if rehearsal else "production", "version": version, "application_commit": source_commit,
            "flake_lock_sha256": hashlib.sha256(lock.read_bytes()).hexdigest(), "system": system,
            "outputs": outputs, "cache_prefix": cache_prefix}


def publish(outputs: dict[str, list[str]], source_commit: str, version: str, work: Path, key: Path, qualified, *,
            lock: Path = ROOT / "flake.lock", policy: dict | None = None, construct=adapter.construct,
            deliver=publication.publish, backend_factory=R2Transport, rehearsal=False, cache_prefix="nix-cache") -> list[dict]:
    """Every native system's closure, signed with the provisioned key, after fresh release admission."""
    if type(rehearsal) is not bool or rehearsal and (policy is None or backend_factory is R2Transport):
        raise ValueError('rehearsal publication requires disposable trust and isolated transport')
    maintained = trust_policy() if not rehearsal else policy
    if not rehearsal and policy is not None and policy != maintained:
        raise ValueError('production publication cannot replace maintained cache trust')
    policy = maintained
    validate_scope('rehearsal' if rehearsal else 'production', cache_prefix, policy)
    if sorted(outputs) != sorted(SYSTEMS):
        raise ValueError("Nix cache publication requires every native Linux system's closure")
    selection = {"source_commit": source_commit, "version": version}
    receipts = []
    for system in SYSTEMS:
        selected = request(source_commit, version, system, outputs[system], lock, rehearsal=rehearsal, cache_prefix=cache_prefix)
        report = construct(selected, policy, key, work / ("cache-" + system))

        def authorize(checked_request, checked, policy=policy):
            return release_authority.authorize(selection, checked_request, checked, trust_policy=policy,
                                               qualified=qualified, rehearsal=rehearsal)
        receipts.append(deliver(work / ("cache-" + system), report, authorize=authorize,
                                backend_factory=backend_factory))
    return receipts




def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    publish_command = commands.add_parser("publish")
    publish_command.add_argument("--outputs", type=Path, required=True, help="directory of <system>.txt store paths")
    publish_command.add_argument("--source-commit", required=True)
    publish_command.add_argument("--version", required=True)
    publish_command.add_argument("--output", type=Path, required=True)
    commands.add_parser("check-trust")
    commands.add_parser("check-credential", help="validate the provisioned key; fails only once Linux is public")
    prepare_halt = commands.add_parser("prepare-withdrawal")
    prepare_halt.add_argument("--distribution-plan", type=Path, required=True)
    prepare_halt.add_argument("--output", type=Path, required=True)
    withdraw_command = commands.add_parser("withdraw")
    withdraw_command.add_argument("--file", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "check-trust":
            trust_policy()
        elif args.command == "check-credential":
            try:
                print("Nix cache credential matches " + credential(os.environ.pop(SECRET, None)))
            except ValueError as exc:
                if linux_public():
                    raise
                print(f"::warning title=Nix cache credential::{exc}")
        elif args.command == "publish":
            from scripts.linux_release import release_signing as release_qualification
            from release_distribution_contract.ledger import require_publishable
            def admit(source):
                require_publishable(args.version)
                release_qualification.require(source)
            outputs = {system: (args.outputs / (system + ".txt")).read_text().split() for system in SYSTEMS}
            with signing_key() as key, tempfile.TemporaryDirectory(prefix="paintedwolf-nix-cache-") as work:
                result = publish(outputs, args.source_commit, args.version, Path(work), key, admit)
                args.output.write_text(json.dumps(result, indent=2) + "\n")
        elif args.command == "prepare-withdrawal":
            from .withdrawal import prepare
            args.output.write_text(json.dumps(prepare(json.loads(args.distribution_plan.read_bytes())), indent=2) + "\n")
        else:
            from .withdrawal import apply
            print(json.dumps(apply(json.loads(args.file.read_bytes()))))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as exc:
        parser.exit(1, f"Nix cache release lane failed: {exc}\n")


if __name__ == "__main__":
    main()
