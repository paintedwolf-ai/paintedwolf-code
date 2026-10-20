"""Attest completed fixed producer jobs within the current protected release workflow."""
from __future__ import annotations
import subprocess
from pathlib import Path

from ..installed_provenance import artifact_for_run, WORKFLOW
from ..installed_artifact_bundle import download, Limits
from ..release_source import facts
from .release_inputs import ARTIFACT, SelectedReleaseInputs

AGGREGATE_STEPS = {"Assemble complete updater manifest and cask",
    "Retain the actual signed non-Linux results under an independent producer role",
    "Retain protected release document inputs without publication", "Upload aggregate release metadata",
    "Upload public release artifacts"}
NATIVE_STEPS = {"Build, sign, and notarize", "Verify and stage platform artifacts", "Upload signed platform result"}
CONSUMERS = {"publish-immutable": "Publish the qualified Linux distribution document",
             "notify-website": "Freshly verify the Linux distribution notification"}
DOCUMENT_STEPS = {"Publish the qualified Linux distribution document", "Retain the published signed distribution document"}


def jobs(api, repository, producer):
    result, page = [], 1
    while True:
        data = api(f"repos/{repository}/actions/runs/{producer['run_id']}/attempts/{producer['run_attempt']}/jobs?per_page=100&page={page}")
        rows = data.get("jobs")
        if not isinstance(rows, list) or len(rows) > 100:
            raise ValueError("protected producer job inventory is malformed")
        result.extend(rows)
        if len(rows) < 100:
            return result
        if page >= 20:
            raise ValueError("protected producer job inventory exceeds its bound")
        page += 1


def attest(api, repository, producer, required, consumer):
    if consumer not in CONSUMERS:
        raise ValueError("same-run handoff is confined to its fixed protected consumers")
    run = api(f"repos/{repository}/actions/runs/{producer['run_id']}")
    expected = {"id": producer["run_id"], "run_attempt": producer["run_attempt"],
                "head_sha": producer["controller_commit"], "head_branch": "main", "path": WORKFLOW,
                "event": "workflow_dispatch", "status": "in_progress", "conclusion": None}
    if (any(type(run.get(key)) is not type(value) or run.get(key) != value for key, value in expected.items())
            or run.get("repository", {}).get("id") != producer["repository_id"]
            or run.get("repository", {}).get("full_name") != repository
            or run.get("head_repository", {}).get("id") != producer["repository_id"]):
        raise ValueError("same-run handoff requires the active protected controller, not whole-workflow completion")
    # The attempt-specific endpoint supplies attempt authority; job payloads need not repeat it.
    inventory = jobs(api, repository, producer)
    attested = []
    for name, step_names in {**required, consumer: {CONSUMERS[consumer]}}.items():
        matches = [row for row in inventory if row.get("name") == name]
        if len(matches) != 1:
            raise ValueError("select exactly one fixed producer or consumer job")
        row = matches[0]
        active = name == consumer
        if (type(row.get("id")) is not int or row["id"] < 1
                or type(row.get("run_id")) is not int or row["run_id"] != producer["run_id"]
                or row.get("head_sha") != producer["controller_commit"] or row.get("head_branch") != "main"
                or row.get("status") != ("in_progress" if active else "completed")
                or row.get("conclusion") != (None if active else "success")):
            raise ValueError("fixed producer job has not successfully completed in this exact attempt")
        steps = row.get("steps")
        if not isinstance(steps, list):
            raise ValueError("fixed producer step inventory is missing")
        selected = []
        for step_name in sorted(step_names):
            matches = [step for step in steps if step.get("name") == step_name]
            if len(matches) != 1:
                raise ValueError("select exactly one fixed successful producer step")
            step = matches[0]
            if (type(step.get("number")) is not int or step["number"] < 1
                    or step.get("status") != ("in_progress" if active else "completed")
                    or step.get("conclusion") != (None if active else "success")):
                raise ValueError("producer step is skipped, failed or incomplete")
            selected.append({"number": step["number"], "name": step_name})
        attested.append({"job_id": row["id"], "name": name, "steps": selected, "completed": not active})
    return attested


def retain_current(api, repository, producer, source_repository, source_commit, destination,
                   artifact_id, *, consumer):
    """Download exact server bytes after scoped job completion; no local-path bypass."""
    from scripts.release_distribution_contract.platforms import parse_platforms
    source = facts(source_repository, source_commit)
    raw_catalog = subprocess.check_output(["git", "--no-replace-objects", "-C", str(source_repository),
        "show", source_commit + ":packaging/release-platforms.json"], timeout=60)
    catalog = parse_platforms(raw_catalog)
    required = {"aggregate-release": AGGREGATE_STEPS}
    required.update({"native-signing-" + key: NATIVE_STEPS for key, row in catalog.items()
                     if row["publication"] == "public" and row["goos"] != "linux"})
    completed = attest(api, repository, producer, required, consumer)
    artifact = artifact_for_run(api, repository, producer["repository_id"], producer["run_id"], producer["controller_commit"], ARTIFACT)
    if type(artifact_id) is not int or artifact_id < 1 or artifact["artifact_id"] != artifact_id:
        raise ValueError("handoff artifact differs from the fixed producer upload output")
    provenance = {"repository": repository, **producer, **artifact}
    download(provenance, destination)
    if (destination / "catalog.json").read_bytes() != raw_catalog:
        raise ValueError("same-run producer catalog differs from the immutable application source")
    selected = SelectedReleaseInputs(destination, provenance, source, producer["controller_commit"])
    if (attest(api, repository, producer, required, consumer) != completed
            or artifact_for_run(api, repository, producer["repository_id"], producer["run_id"], producer["controller_commit"], ARTIFACT) != artifact):
        raise ValueError("producer handoff changed during digest-bound retention")
    return selected, {"completion_scope": "producer-jobs", "workflow_completed": False,
                      "producer_jobs": completed, "artifact": artifact}


def retain_document(api, repository, producer, source, destination, artifact_id):
    from ..installed_provenance import verify_producer
    from ..release_admission import document
    completed = attest(api, repository, producer, {"publish-immutable": DOCUMENT_STEPS}, "notify-website")
    role = "release-signed-distribution-document"
    artifact = artifact_for_run(api, repository, producer["repository_id"], producer["run_id"], producer["controller_commit"], role)
    if type(artifact_id) is not int or artifact_id < 1 or artifact["artifact_id"] != artifact_id:
        raise ValueError("signed document differs from its fixed producer upload output")
    download({"repository": repository, **producer, **artifact}, destination,
             limits=Limits(max_files=4, max_bytes=4 << 20, max_file=2 << 20))
    if {path.name for path in destination.iterdir()} != {"distribution.v1.json", "distribution.v1.json.sig", "distribution-identity.json", "producer.json"}:
        raise ValueError("signed document artifact must contain exactly its four fixed metadata files")
    proof, _ = document(destination / "producer.json")
    verify_producer(producer, proof, source["source_commit"], source["source_tree"])
    if (attest(api, repository, producer, {"publish-immutable": DOCUMENT_STEPS}, "notify-website") != completed
            or artifact_for_run(api, repository, producer["repository_id"], producer["run_id"], producer["controller_commit"], role) != artifact):
        raise ValueError("signed document selection changed during retention")
    return artifact
