"""Admit complete installed Linux campaigns against the shared support matrix."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys
import time

from scripts.artifact_paths import artifact_root

from . import installed_evidence as evidence
from .support_matrix import CATALOG, expand

STATUSES = {"passed", "failed", "unverified", "skipped"}


def outcome(value: object) -> str:
    if not isinstance(value, str) or value not in STATUSES:
        raise ValueError("unknown case/result status")
    return value


def case_results(raw: object, required: list[str], root: Path) -> list[str]:
    if not isinstance(raw, list):
        raise ValueError("cases must be a list")
    seen, errors = set(), []
    for value in raw:
        case = evidence.object_fields(value, {"id", "status", "evidence"})
        key = case["id"]
        if not isinstance(key, str) or key not in required or key in seen:
            raise ValueError("unknown or duplicate case id")
        seen.add(key)
        status = outcome(case["status"])
        if status != "passed":
            errors.append(f"case {key}: {status}")
        records = case["evidence"]
        if not isinstance(records, list) or status == "passed" and not records:
            raise ValueError(f"case {key} has no retained evidence")
        paths = set()
        for record in records:
            evidence.retained_file(record, root)
            if record["path"] in paths:
                raise ValueError(f"case {key} repeats an evidence file")
            paths.add(record["path"])
    errors.extend(f"case {key}: missing" for key in sorted(set(required) - seen))
    return errors


def row_errors(item: dict, row: dict, artifact: dict, source_commit: str,
               facts: list[str], root: Path) -> list[str]:
    errors = []
    if evidence.source(item["source_commit"]) != source_commit:
        errors.append("source commit differs from the final artifact source")
    if (artifact["installation"] != row["installation"]
            or artifact["architecture"] != row["architecture"]):
        errors.append("artifact installation/architecture differs from row")
    if item["tier"] != row["tier"]:
        errors.append("evidence tier differs from row")
    if item["execution"] not in ("native", "emulated"):
        raise ValueError("execution must be native or emulated")
    if item["execution"] == "emulated" and not row["emulation_allowed"]:
        errors.append("required native evidence cannot be replaced by emulation")
    status = outcome(item["status"])
    if status != "passed":
        errors.append(f"result: {status}")
    actual = item["environment_facts"]
    if not isinstance(actual, dict) or actual.keys() != set(facts):
        raise ValueError("environment facts must contain exactly the catalog's recorded fields")
    if any(not isinstance(value, str) or not value.strip() for value in actual.values()):
        raise ValueError("environment facts must contain nonempty recorded values")
    errors.extend(case_results(item["cases"], row["cases"], root))
    return errors


def admit(catalog: dict, campaign: object, final_artifacts: object,
          source_commit: str, root: Path) -> dict:
    """Validate collector output; final_artifacts comes from the release owner."""
    evidence.source(source_commit)
    rows = expand(catalog)
    facts = catalog.get("recorded_environment_facts")
    if (not isinstance(facts, list) or not facts or len(set(facts)) != len(facts)
            or any(not isinstance(key, str) or not key for key in facts)):
        raise ValueError("catalog recorded_environment_facts must name distinct fields")
    expected_artifacts = evidence.artifacts(final_artifacts, source_commit)
    document = evidence.object_fields(campaign, {"format_version", "source_commit", "artifacts", "results"})
    if type(document["format_version"]) is not int or document["format_version"] != 1:
        raise ValueError("unsupported installed campaign format")
    actual_artifacts = evidence.artifacts({key: document[key] for key in
                                          ("format_version", "source_commit", "artifacts")}, source_commit)
    if actual_artifacts != expected_artifacts:
        raise ValueError("campaign artifacts differ from the final release artifact set")
    if not isinstance(document["results"], list):
        raise ValueError("campaign results must be a list")
    known = {row["id"]: row for row in rows}
    actual, used_artifacts = {}, set()
    for value in document["results"]:
        item = evidence.object_fields(value, {"row_id", "source_commit", "artifact_id", "tier",
                                             "execution", "status", "cases", "environment_facts"})
        key, artifact_id = item["row_id"], item["artifact_id"]
        if not isinstance(key, str) or key not in known or key in actual:
            raise ValueError("unknown or duplicate support row")
        if not isinstance(artifact_id, str) or artifact_id not in actual_artifacts:
            raise ValueError("result does not identify a declared final artifact")
        used_artifacts.add(artifact_id)
        errors = row_errors(item, known[key], actual_artifacts[artifact_id], source_commit, facts, root)
        failed = item["status"] == "failed" or any(case["status"] == "failed" for case in item["cases"])
        actual[key] = {"status": "failed" if failed else "unverified" if errors else "passed", "errors": errors}
    results = [{**row, **actual.get(row["id"], {"status": "unverified", "errors": ["required row is missing"]})}
               for row in rows]
    errors = [f"artifact {key}: no installed results" for key in sorted(expected_artifacts.keys() - used_artifacts)]
    accepted = not errors and all(row["status"] == "passed" for row in results)
    return {"format_version": 1, "source_commit": source_commit, "accepted": accepted,
            "errors": errors, "rows": results,
            "manual_checklist": [{"row_id": row["id"], "tier": row["tier"], "cases": row["cases"], "status": row["status"]}
                                 for row in results if row["tier"] != "installed-functional"]}


def main() -> int:
    if len(sys.argv) > 1 and sys.argv[1] == "--run-cases":
        from .installed_execution import main as execute
        return execute(sys.argv[2:])
    if len(sys.argv) > 1 and sys.argv[1] == "--execute-plan":
        from .installed_campaign import main as execute_plan
        return execute_plan(sys.argv[2:])
    if len(sys.argv) > 1 and sys.argv[1] == "--diagnose-first-launch":
        from .installed_cases.desktop import diagnostic_main
        return diagnostic_main(sys.argv[2:])
    if len(sys.argv) > 1 and sys.argv[1] == "--prepare-candidate":
        from .installed_candidate_inputs import main as prepare_candidate
        return prepare_candidate(sys.argv[2:])
    if len(sys.argv) > 1 and sys.argv[1] == "--prepare-hosted-plan":
        from .hosted_installed_plan import main as prepare_hosted_plan
        return prepare_hosted_plan(sys.argv[2:])
    if len(sys.argv) > 1 and sys.argv[1] == "--hosted-runtime":
        from .hosted_installed import main as hosted_runtime
        return hosted_runtime(sys.argv[2:])
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=CATALOG)
    parser.add_argument("--campaign", type=Path, required=True)
    parser.add_argument("--final-artifacts", type=Path, required=True)
    parser.add_argument("--source-commit", required=True)
    parser.add_argument("--evidence-root", type=Path, required=True)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    output = (args.report or (artifact_root(Path(__file__).resolve().parents[2]) /
              "linux-installed" / f"campaign-{time.time_ns()}.json")).resolve()
    if output.is_relative_to(args.evidence_root.resolve()) or output in {
            args.catalog.resolve(), args.campaign.resolve(), args.final_artifacts.resolve()}:
        parser.error("campaign report must not replace its inputs or retained evidence")
    try:
        read = lambda path: json.loads(path.read_text(encoding="utf-8"))
        report = admit(read(args.catalog), read(args.campaign), read(args.final_artifacts),
                       args.source_commit, args.evidence_root)
    except (OSError, ValueError, TypeError, AttributeError) as exc:
        report = {"format_version": 1, "accepted": False, "errors": [str(exc)]}
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open("x", encoding="utf-8") as retained:
        json.dump(report, retained, indent=2)
        retained.write("\n")
    print(f"Installed Linux campaign report: {output}", file=sys.stderr)
    json.dump(report, sys.stdout, indent=2)
    sys.stdout.write("\n")
    return 0 if report["accepted"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
