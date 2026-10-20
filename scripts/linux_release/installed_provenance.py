"""Select immutable campaign artifacts from the protected release controller."""

from __future__ import annotations

import re

ARTIFACT = "linux-final-admission-inputs"
RAW_ARTIFACT = "linux-installed-raw-campaign"
WORKFLOW = ".github/workflows/release.yml"


def controller_context(environment) -> dict:
    """Bind current execution to the immutable protected main workflow revision."""
    repository = environment.get("GITHUB_REPOSITORY", "")
    commit = environment.get("GITHUB_SHA", "")
    if (not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository)
            or environment.get("GITHUB_EVENT_NAME") != "workflow_dispatch"
            or environment.get("GITHUB_REF") != "refs/heads/main"
            or environment.get("GITHUB_WORKFLOW_REF") != f"{repository}/{WORKFLOW}@refs/heads/main"
            or not re.fullmatch("[0-9a-f]{40}", commit)
            or environment.get("GITHUB_WORKFLOW_SHA") != commit):
        raise ValueError("campaign authority requires the protected main release controller")
    identities = {key: int(environment[name]) for key, name in
                  [("repository_id", "GITHUB_REPOSITORY_ID"), ("run_id", "GITHUB_RUN_ID"),
                   ("run_attempt", "GITHUB_RUN_ATTEMPT")]}
    if any(value < 1 for value in identities.values()):
        raise ValueError("campaign controller IDs must be positive")
    return {**identities, "controller_commit": commit}


def select_final(api, repository: str, repository_id: int, run_id: int) -> dict:
    """Select the retained final campaign from its independently completed controller."""
    return _select(api, repository, repository_id, run_id, ARTIFACT)


def select_qualification(api, repository: str, repository_id: int, run_id: int) -> dict:
    """Select actual guest observations separately from the retained final campaign."""
    return _select(api, repository, repository_id, run_id, RAW_ARTIFACT)


def select_control(api, repository: str, repository_id: int, run_id: int, artifact_id: int) -> dict:
    return _identified(api, repository, repository_id, run_id, artifact_id, "linux-installed-control-inputs")


def select_plan(api, repository: str, repository_id: int, run_id: int, artifact_id: int) -> dict:
    return _identified(api, repository, repository_id, run_id, artifact_id, "linux-installed-qualification-plan")


def _identified(api, repository, repository_id, run_id, artifact_id, name):
    if type(artifact_id) is not int or artifact_id < 1:
        raise ValueError("select an exact positive controller artifact ID")
    selected = _select(api, repository, repository_id, run_id, name)
    if selected["artifact_id"] != artifact_id:
        raise ValueError("selected controller artifact ID differs from its fixed role")
    return selected


def _select(api, repository: str, repository_id: int, run_id: int, artifact_name: str) -> dict:
    """Read server-owned provenance before downloading or interpreting campaign data."""
    if (not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository)
            or type(repository_id) is not int or repository_id < 1
            or type(run_id) is not int or run_id < 1):
        raise ValueError("select the controller repository and an exact workflow run")
    prefix = f"repos/{repository}/actions"
    run = api(f"{prefix}/runs/{run_id}")
    commit = run.get("head_sha")
    attempt = run.get("run_attempt")
    if (type(run.get("id")) is not int or run["id"] != run_id
            or run.get("repository", {}).get("id") != repository_id
            or run.get("repository", {}).get("full_name") != repository
            or run.get("head_repository", {}).get("id") != repository_id
            or run.get("path") != WORKFLOW or run.get("head_branch") != "main"
            or run.get("event") != "workflow_dispatch" or run.get("status") != "completed"
            or run.get("conclusion") != "success" or type(attempt) is not int or attempt < 1
            or not isinstance(commit, str) or not re.fullmatch(r"[0-9a-f]{40}", commit)):
        raise ValueError("campaign inputs require a successful protected main release controller")
    # Select by artifact ID. A name-based download can combine stale rerun artifacts.
    matches, page = [], 1
    while True:
        response = api(f"{prefix}/runs/{run_id}/artifacts?per_page=100&page={page}")
        rows = response.get("artifacts")
        if not isinstance(rows, list) or len(rows) > 100:
            raise ValueError("invalid controller artifact inventory")
        matches.extend(row for row in rows if row.get("name") == artifact_name)
        if len(rows) < 100:
            break
        if page >= 100:
            raise ValueError("controller artifact inventory exceeds its bound")
        page += 1
    if len(matches) != 1:
        raise ValueError("select exactly one retained final campaign artifact")
    artifact = matches[0]
    producer = artifact.get("workflow_run", {})
    identity = artifact.get("id")
    digest = artifact.get("digest")
    if (type(identity) is not int or identity < 1 or artifact.get("expired") is not False
            or type(artifact.get("size_in_bytes")) is not int or not 1 <= artifact["size_in_bytes"] <= 64 << 30
            or type(producer.get("id")) is not int or producer["id"] != run_id
            or producer.get("head_sha") != commit
            or type(producer.get("repository_id")) is not int or producer["repository_id"] != repository_id
            or type(producer.get("head_repository_id")) is not int or producer["head_repository_id"] != repository_id
            or not isinstance(digest, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", digest)):
        raise ValueError("final campaign artifact has stale or mismatched server provenance")
    return {"repository": repository, "repository_id": repository_id, "run_id": run_id,
            "run_attempt": attempt, "controller_commit": commit, "artifact_id": identity,
            "artifact_bytes": artifact["size_in_bytes"],
            "artifact_sha256": digest.removeprefix("sha256:")}


def verify_producer(selected: dict, producer: dict, source_commit: str, source_tree: str) -> None:
    """Bind the downloaded artifact to the selected attempt and immutable runtime source."""
    expected = {"format_version": 1, "repository_id": selected["repository_id"],
                "run_id": selected["run_id"], "run_attempt": selected["run_attempt"],
                "controller_commit": selected["controller_commit"],
                "source_commit": source_commit, "source_tree": source_tree}
    if (not all(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{40}", value)
                for value in [source_commit, source_tree])
            or not isinstance(producer, dict) or set(producer) != set(expected)
            or any(type(producer[key]) is not type(value) or producer[key] != value
                   for key, value in expected.items())):
        raise ValueError("downloaded final campaign differs from its selected producer or source")
