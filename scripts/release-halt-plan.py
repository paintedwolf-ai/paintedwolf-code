#!/usr/bin/env python3
"""Validate every affected updater feed before applying a withdrawal."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess

from update_keys import feed_key, generation, load_registry, validate_binding
from release_semver import compare, parse
from release_distribution import read_storage


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
                row.update(bad=bad, last_good=last_good)
            else:
                row["keep_version"] = version
            rows.append(row)
    return {"source_generation": source, "feeds": rows}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--plan", type=Path)
    parser.add_argument("--bad")
    parser.add_argument("--last-good")
    parser.add_argument("--source-generation", type=int, default=1)
    parser.add_argument("--write-plan", type=Path)
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--distribution-output", type=Path)
    args = parser.parse_args()
    registry = load_registry()
    if bool(args.plan) == bool(args.bad):
        raise ValueError("supply either a reviewed plan or a version to withdraw")
    plan = json.loads(args.plan.read_text()) if args.plan else plan_withdrawal(registry, args.source_generation, args.bad, args.last_good)
    if args.write_plan:
        args.write_plan.write_text(json.dumps(plan, indent=2) + "\n")
    if set(plan) != {"source_generation", "feeds"}:
        raise ValueError("halt plan requires source_generation and feeds")
    source = plan["source_generation"]
    generation(registry, source)
    rows = {}
    for row in plan["feeds"]:
        key = (row["generation"], row["channel"])
        if key in rows:
            raise ValueError("duplicate halt feed")
        if set(row) not in ({"generation", "channel", "bad", "last_good"}, {"generation", "channel", "keep_version"}):
            raise ValueError("each feed must halt bad to last_good, or explicitly keep its current version")
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
    script = Path(__file__).with_name("release-halt.sh")
    commands = []
    for (number, channel), row in rows.items():
        if "bad" in row:
            command = ["bash", str(script), "--generation", str(number), "--channel", channel,
                       "--bad", row["bad"], "--reviewed-plan"]
            if row["last_good"] is not None:
                command += ["--last-good", row["last_good"]]
            commands.append(command)
    for command in commands:
        subprocess.run(command + ["--dry-run"], check=True)
    if args.distribution_output:
        args.distribution_output.write_text(json.dumps(distribution_plan(list(rows.values()), args.bad, args.last_good)))
    if not args.dry_run:
        failures = []
        for command in commands:
            result = subprocess.run(command, check=False)
            if result.returncode:
                failures.append(f"{command}: exit {result.returncode}")
        if failures:
            raise ValueError("withdrawal failed for some feeds: " + "; ".join(failures))


if __name__ == "__main__":
    main()
