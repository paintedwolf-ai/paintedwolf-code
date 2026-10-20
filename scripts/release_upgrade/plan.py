"""Plan channel-preserving updates through retained trust generations."""
from __future__ import annotations

from release_semver import compare, parse
from update_keys import generation, validate_binding


def client_protocol(version: str) -> str:
    # The released 1.0.0 and 1.0.1 clients install through Tauri's updater plugin.
    return "tauri-plugin-v1" if compare(parse(version), parse("1.0.1")) <= 0 else "native-v2"


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


def plan(candidate: dict, registry: dict, snapshots: list[dict], anchors: list[dict] | None = None) -> dict:
    validate_binding(registry, candidate)
    anchors = anchors or []
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
    preview = feeds.get(("preview", candidate["update_keys"]["signing_generation"]))
    promotes_preview = preview is None or compare(parse(preview["manifest"]["version"]), target) <= 0
    cases = {}
    excluded = []
    sources = [("automatic_baseline", None, snapshot) for snapshot in anchors]
    sources.extend(("feed", channel, snapshot) for (channel, _), snapshot in feeds.items())
    identities = {}
    for kind, source_channel, snapshot in sources:
        source = snapshot["manifest"]
        validate_binding(registry, source)
        version = source["version"]
        identity = (source["update_keys"], source.get("platforms"))
        if version in identities and identities[version] != identity:
            raise ValueError("one source release has conflicting artifact or trust identities")
        identities[version] = identity
        if compare(parse(version), parse("1.0.0")) < 0 or compare(parse(version), target) >= 0:
            continue
        if client_protocol(version) != "native-v2":
            excluded.append({"source_version": version, "source_kind": kind, "source_path": snapshot["path"],
                             "protocol": client_protocol(version),
                             "reason": "requires_manual_installation"})
            continue
        channels = (("stable", "preview") if promotes_preview else ("stable",)) if target.channel == "stable" else ("preview",)
        for channel in channels:
            # Preview installations can originate from either published channel.
            if channel == "stable" and source_channel == "preview":
                continue
            steps = route(source, candidate, registry, feeds, channel)
            key = (version, channel)
            cases[key] = {"id": f"{version}-{channel}", "source_version": version,
                          "channel": channel, "protocol": client_protocol(version),
                          "source_kind": kind, "source_path": snapshot["path"], "source_manifest": source,
                          "source_signing_generation": source["update_keys"]["signing_generation"],
                          "initial_generation": source["update_keys"]["embedded_generation"], "steps": steps}
    return {"format_version": 1, "candidate": candidate, "feeds": snapshots, "anchors": anchors,
            "cases": [cases[key] for key in sorted(cases)], "excluded_sources": excluded,
            "skip_reason": None if cases else "no_automatic_upgrade_source"}
