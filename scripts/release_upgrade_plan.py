"""Plan channel-preserving updates through retained trust generations."""
from __future__ import annotations

from release_semver import compare, parse
from update_keys import generation, validate_binding


def route(source: dict, candidate: dict, registry: dict, feeds: dict, channel: str) -> list[dict]:
    current = source
    steps = []
    target = candidate["update_keys"]["signing_generation"]
    number = current["update_keys"]["embedded_generation"]
    while number < target:
        row = generation(registry, number)
        bridge = feeds.get((channel, number))
        if bridge is None or bridge["manifest"]["version"] != row["bridge_version"]:
            raise ValueError("retained generation has no authenticated bridge")
        following = bridge["manifest"]
        if following.get("withdrawn") or following["update_keys"]["embedded_generation"] != number + 1:
            raise ValueError("retained generation cannot advance trust")
        if compare(parse(current["version"]), parse(following["version"])) >= 0:
            raise ValueError("bridge must advance the installed version")
        steps.append(following)
        current = following
        number += 1
    if number != target or compare(parse(current["version"]), parse(candidate["version"])) >= 0:
        raise ValueError("candidate is unreachable from the source trust generation")
    return [*steps, candidate]


def plan(candidate: dict, registry: dict, snapshots: list[dict]) -> dict:
    validate_binding(registry, candidate)
    target = parse(candidate["version"])
    feeds = {}
    for snapshot in snapshots:
        channel, number = snapshot["channel"], snapshot["generation"]
        if channel not in ("stable", "preview") or (channel, number) in feeds:
            raise ValueError("duplicate or invalid source feed")
        source = snapshot["manifest"]
        validate_binding(registry, source, number)
        if channel == "stable" and parse(source["version"]).channel != "stable":
            raise ValueError("stable feed contains a prerelease")
        feeds[channel, number] = snapshot
    cases = {}
    for (source_channel, number), snapshot in feeds.items():
        source = snapshot["manifest"]
        version = source["version"]
        if compare(parse(version), parse("1.0.0")) < 0 or compare(parse(version), target) >= 0:
            continue
        channels = ("stable", "preview") if target.channel == "stable" else ("preview",)
        for channel in channels:
            # Preview installations can originate from either published channel.
            if channel == "stable" and source_channel != "stable":
                continue
            steps = route(source, candidate, registry, feeds, channel)
            key = (version, channel)
            cases[key] = {"id": f"{version}-{channel}", "source_version": version,
                          "channel": channel, "source_feed": snapshot["path"],
                          "source_generation": number, "steps": steps}
    return {"format_version": 1, "candidate": candidate, "feeds": snapshots,
            "cases": [cases[key] for key in sorted(cases)],
            "skip_reason": None if cases else "no_older_published_source"}
