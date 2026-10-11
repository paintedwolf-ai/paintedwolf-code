#!/usr/bin/env python3
"""Validate every affected updater feed before applying a withdrawal."""
from __future__ import annotations

import argparse
import json
import hashlib
import tempfile
from pathlib import Path
import subprocess

from update_keys import feed_key, generation, load_registry, validate_binding
from release_semver import compare, parse
from release_distribution import read_storage, read_storage_bytes

# Clients embedding these generations ignore a pointer's `withdrawn` marker, so their
# feeds can only stop a release by pointing at a replacement.
WITHDRAWN_UNAWARE_GENERATIONS = {1}


def require_replacement(number: int, last_good: str | None) -> None:
    if last_good is None and number in WITHDRAWN_UNAWARE_GENERATIONS:
        raise ValueError(f"generation {number} clients ignore withdrawn markers; name a last-good release")


def distribution_plan(rows: list[dict], bad: str | None = None, last_good: str | None = None) -> dict:
    if bad is not None:
        channels = ("stable", "preview") if parse(bad).channel == "stable" else ("preview",)
        return {"bad_versions": [bad], "channels": [{"channel": channel, "bad": bad, "last_good": last_good} for channel in channels]}
    latest = {}
    for row in rows:
        channel = row["channel"]
        if channel not in latest or row["generation"] > latest[channel]["generation"]:
            latest[channel] = row
    return {"bad_versions": sorted({row["bad"] for row in rows if "bad" in row}),
            "channels": [row for row in latest.values() if "bad" in row]}


def plan_withdrawal(registry: dict, source: int, bad: str, last_good: str | None) -> dict:
    parse(bad)
    if last_good is not None and compare(parse(bad), parse(last_good)) <= 0:
        raise ValueError("last-good must be older than the withdrawn release")
    generation(registry, source)
    rows = []
    for number in range(source, len(registry["generations"]) + 1):
        for channel in ("stable", "preview"):
            manifest = read_storage(feed_key(channel, number))
            if manifest is None:
                continue
            validate_binding(registry, manifest, number)
            version = manifest["version"]
            row = {"generation": number, "channel": channel}
            if version in {bad, last_good}:
                require_replacement(number, last_good)
                row.update(bad=bad, last_good=last_good)
            else:
                row["keep_version"] = version
            rows.append(row)
    return {"source_generation": source, "feeds": rows, "distribution": distribution_plan(rows, bad, last_good)}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--plan", type=Path)
    parser.add_argument("--bad")
    parser.add_argument("--last-good")
    parser.add_argument("--source-generation", type=int, default=1)
    parser.add_argument("--write-plan", type=Path)
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--distribution-output", type=Path)
    parser.add_argument("--prepare-output", type=Path)
    parser.add_argument("--apply-prepared", type=Path)
    args = parser.parse_args()
    registry = load_registry()
    if bool(args.plan) == bool(args.bad):
        raise ValueError("supply either a reviewed plan or a version to withdraw")
    plan = json.loads(args.plan.read_text()) if args.plan else plan_withdrawal(registry, args.source_generation, args.bad, args.last_good)
    if args.write_plan:
        args.write_plan.write_text(json.dumps(plan, indent=2) + "\n")
    if set(plan) not in ({"source_generation", "feeds"}, {"source_generation", "feeds", "distribution"}):
        raise ValueError("halt plan requires source_generation, feeds, and optional distribution")
    source = plan["source_generation"]
    generation(registry, source)
    rows = {}
    for row in plan["feeds"]:
        key = (row["generation"], row["channel"])
        if key in rows:
            raise ValueError("duplicate halt feed")
        if set(row) not in ({"generation", "channel", "bad", "last_good"}, {"generation", "channel", "keep_version"}):
            raise ValueError("each feed must halt bad to last_good, or explicitly keep its current version")
        if "bad" in row:
            require_replacement(row["generation"], row["last_good"])
        rows[key] = row
    affected = range(source, len(registry["generations"]) + 1)
    current = {}
    for number in affected:
        for channel in ("stable", "preview"):
            key = (number, channel)
            manifest = read_storage(feed_key(channel, number))
            if manifest is None:
                if key in rows:
                    raise ValueError(f"planned feed does not exist: {key}")
                continue
            validate_binding(registry, manifest, number)
            current[key] = manifest["version"]
            if key not in rows:
                raise ValueError(f"plan omits affected feed {key}; declare halt or keep_version")
            row = rows[key]
            accepted = {row["keep_version"]} if "keep_version" in row else {row["bad"], row["last_good"]}
            if manifest["version"] not in accepted:
                raise ValueError(f"affected feed changed: {key}")
    if set(rows) != set(current):
        raise ValueError("plan names a feed outside the affected generation chain")
    withdrawals = {(row["generation"], row["bad"]) for row in rows.values() if "bad" in row}
    for row in rows.values():
        if "keep_version" in row and (row["generation"], row["keep_version"]) in withdrawals:
            raise ValueError("a withdrawn bridge cannot remain offered on the other source channel")
    distribution = plan.get("distribution", distribution_plan(list(rows.values())))
    if args.distribution_output:
        args.distribution_output.write_text(json.dumps(distribution))
    if args.prepare_output and args.apply_prepared:
        raise ValueError("choose prepare-output or apply-prepared")
    with tempfile.TemporaryDirectory(prefix="prepared-halt-") as temporary:
        directory = args.apply_prepared or args.prepare_output or Path(temporary)
        if not args.apply_prepared:
            prepare(rows, plan, distribution, directory)
        prepared = validate_prepared(rows, plan, directory, registry)
        if not args.dry_run:
            apply(prepared, directory)


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def prepare(rows: dict, plan: dict, distribution: dict, directory: Path) -> None:
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "prepared.json").unlink(missing_ok=True)
    index = {"format_version": 1, "plan": plan, "distribution": distribution, "feeds": []}
    script = Path(__file__).with_name("release-halt.sh")
    for (number, channel), row in rows.items():
        if "bad" not in row:
            continue
        key = feed_key(channel, number)
        before_bytes = read_storage_bytes(key)
        if before_bytes is None:
            raise ValueError("feed disappeared during preparation")
        before_signature = read_storage_bytes(key + ".sig")
        before = json.loads(before_bytes)
        command = ["bash", str(script), "--generation", str(number), "--channel", channel,
                   "--bad", row["bad"], "--reviewed-plan", "--prepare-output", str(directory)]
        if row["last_good"] is not None:
            command += ["--last-good", row["last_good"]]
        subprocess.run(command, check=True)
        name = f"latest-{channel}-key-{number}.json"
        index["feeds"].append({"generation": number, "channel": channel, "bad": row["bad"],
                               "before": before, "before_pointer_sha256": hashlib.sha256(before_bytes).hexdigest(),
                               "before_signature_sha256": hashlib.sha256(before_signature).hexdigest() if before_signature is not None else None,
                               "pointer_sha256": digest(directory / name),
                               "signature_sha256": digest(directory / (name + ".sig"))})
    # This receipt is written only after every signing operation has succeeded.
    (directory / "prepared.json").write_text(json.dumps(index, indent=2) + "\n")


def validate_prepared(rows: dict, plan: dict, directory: Path, registry: dict) -> dict:
    from feed_signature import check
    from feed_signing import verify
    from update_keys import generation, validate_publication
    index = json.loads((directory / "prepared.json").read_text())
    if set(index) != {"format_version", "plan", "distribution", "feeds"} or index["format_version"] != 1 or index["plan"] != plan:
        raise ValueError("prepared halt does not match the reviewed plan")
    if index["distribution"] != plan.get("distribution", distribution_plan(list(rows.values()))):
        raise ValueError("prepared distribution withdrawal differs from the reviewed plan")
    expected = {key for key, row in rows.items() if "bad" in row}
    seen = set()
    for item in index["feeds"]:
        number, channel = item["generation"], item["channel"]
        key = (number, channel)
        if key not in expected or key in seen or item["bad"] != rows[key]["bad"]:
            raise ValueError("prepared halt has unexpected or duplicate feeds")
        seen.add(key)
        name = f"latest-{channel}-key-{number}.json"
        pointer, signature = directory / name, directory / (name + ".sig")
        if digest(pointer) != item["pointer_sha256"] or digest(signature) != item["signature_sha256"]:
            raise ValueError("prepared halt bytes changed")
        manifest = json.loads(pointer.read_text())
        validate_publication(registry, manifest, number, halt=True)
        row = rows[key]
        if manifest["version"] != (row["last_good"] or row["bad"]) or bool(manifest.get("withdrawn")) != (row["last_good"] is None):
            raise ValueError("prepared pointer does not implement the reviewed withdrawal")
        check(signature.read_text(), file=name, version=manifest["version"], number=number, registry=registry)
        verify(pointer, signature.read_text(), generation(registry, number)["feed_public_key"])
        current = read_storage_bytes(feed_key(channel, number))
        current_signature = read_storage_bytes(feed_key(channel, number) + ".sig")
        current_hash = hashlib.sha256(current).hexdigest() if current is not None else None
        signature_hash = hashlib.sha256(current_signature).hexdigest() if current_signature is not None else None
        if current_hash not in {item["before_pointer_sha256"], item["pointer_sha256"]} or signature_hash not in {item["before_signature_sha256"], item["signature_sha256"]}:
            raise ValueError(f"feed changed since preparation: {key}")
    if seen != expected:
        raise ValueError("prepared halt is incomplete")
    return index


def apply(index: dict, directory: Path) -> None:
    distribution = directory / "distribution.json"
    distribution.write_text(json.dumps(index["distribution"]))
    subprocess.run(["python3", str(Path(__file__).with_name("release_distribution.py")),
                    "withdraw", "--file", str(distribution)], check=True)
    receipts = []
    for item in index["feeds"]:
        number, channel = item["generation"], item["channel"]
        name = f"latest-{channel}-key-{number}.json"
        result = subprocess.run(["bash", str(Path(__file__).with_name("release-r2-publish-pointer.sh")),
            "--file", str(directory / name), "--signature", str(directory / (name + ".sig")),
            "--channel", channel, "--generation", str(number), "--from-version", item["bad"]], check=False)
        receipts.append({"generation": number, "channel": channel, "exit_code": result.returncode,
                         "pointer_sha256": item["pointer_sha256"], "signature_sha256": item["signature_sha256"]})
        (directory / "application.json").write_text(json.dumps({"format_version": 1, "feeds": receipts}, indent=2) + "\n")
    if any(row["exit_code"] for row in receipts):
        raise ValueError("some feeds failed to publish; inspect application.json and retry the prepared halt")


if __name__ == "__main__":
    main()
