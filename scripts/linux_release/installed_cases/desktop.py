"""Actual installed desktop interactions; missing drivers remain unverified."""
from __future__ import annotations

from pathlib import Path
import json
import re

from .. import installed_evidence as evidence

from .app import InstalledApp
from .desktop_ui import interactive_window
from .records import CaseUnavailable, retain


def observed_journal(binding: dict, root: Path, transaction: dict, phase: str, *, first_launch=False) -> dict:
    fields = {"format_version", "status", "source_commit", "artifact_id", "evidence", "observations"}
    if first_launch:
        fields.add("execution_nonce")
    row = evidence.object_fields(transaction, fields)
    if first_launch and (not isinstance(binding.get("execution_nonce"), str)
            or re.fullmatch(r"[a-f0-9]{32}", binding["execution_nonce"]) is None):
        raise ValueError("first launch journal requires the actual fresh execution nonce")
    if (type(row["format_version"]) is not int or row["format_version"] != 1
            or row["status"] not in {"passed", "failed", "unverified"}
            or row["source_commit"] != binding["source_commit"] or row["artifact_id"] != binding["artifact"]["id"]
            or first_launch and row["execution_nonce"] != binding.get("execution_nonce")
            or not isinstance(row["evidence"], list) or not 1 <= len(row["evidence"]) <= 17):
        raise ValueError("installation journal differs from the selected native execution")
    journal, seen = None, set()
    prefix = "installation/" + phase + "/"
    for record in row["evidence"]:
        evidence.retained_file(record, root)
        if record["path"] in seen or not record["path"].startswith(prefix):
            raise ValueError("installation journal repeats or crosses retained operation evidence")
        seen.add(record["path"])
        if record["path"] == prefix + "execution.json":
            path = root / record["path"]
            if path.stat().st_size > 1 << 20:
                raise ValueError("installation journal exceeds its bound")
            journal = json.loads(path.read_text())
    expected = {"format_version": 1, "source_commit": binding["source_commit"], "artifact": binding["artifact"],
                "capability": "installation", "case": phase, "status": row["status"],
                "observations": row["observations"]}
    if journal != expected or not isinstance(row["observations"], dict):
        raise ValueError("installation journal differs from its actual retained operation")
    return row


def installation_transaction(binding: dict, root: Path) -> list[dict] | None:
    if binding.get("installation_transaction") is None:
        return None
    row = observed_journal(binding, root, binding["installation_transaction"], "preinstallation-transaction")
    observations = row["observations"]
    if (row["status"] != "passed" or observations.get("operation") != "fresh-install"
            or observations.get("expected_absent") is not True or observations.get("profile_absent") is not True
            or observations.get("webview_profile_absent") is not True
            or observations.get("installed") is not True or observations.get("recovery_required") is not False
            or observations.get("output_limit") is not False):
        raise ValueError("fresh installation journal did not observe successful absent-to-installed effects")
    return row["evidence"]


def prepare_first_launch(binding: dict, root: Path) -> dict:
    installation = installation_transaction(binding, root)
    if installation is None:
        raise ValueError("first postinstallation launch requires its actual fresh installation")
    nonce = binding.get("execution_nonce")
    if not isinstance(nonce, str) or re.fullmatch(r"[a-f0-9]{32}", nonce) is None:
        raise ValueError("first postinstallation launch requires the fresh controller nonce")
    return observe_startup(binding, root, "first-launch-transaction",
        {"execution_nonce": nonce, "profile_absent_before": True, "webview_profile_absent_before": True,
         "preinstallation_evidence": installation})


def observe_startup(binding: dict, root: Path, phase: str, observations: dict) -> dict:
    app, status = InstalledApp(binding), "failed"
    nonce = binding["execution_nonce"]
    try:
        with app:
            observations["interactive_window"] = interactive_window(app)
            response = app.request("GET", "/health")
            observations["engine_health"] = response["status"]
            if response["status"] != 200:
                raise ValueError("first installed application launch did not establish its actual engine")
        status = "passed"
    except (OSError, ValueError) as error:
        observations["error_type"], observations["reason"] = type(error).__name__, str(error)
        observations["failure_observations"] = getattr(error, "observations", {})
        status = "unverified" if isinstance(error, CaseUnavailable) else "failed"
    finally:
        app.stop()
        observations["desktop"] = app.observations
    if status == "passed" and (not app.observations.get("capture_complete") or app.observations.get("output_limit")):
        status = "failed"
    result = retain(binding, "installation", phase, root, status, observations, app.streams())
    return {"format_version": 1, "status": status, "source_commit": binding["source_commit"],
            "artifact_id": binding["artifact"]["id"], "execution_nonce": nonce,
            "observations": observations, "evidence": result["evidence"]}

def execute(binding: dict, capability: str, case: str, root: Path) -> dict:
    inputs = binding.get("case_inputs", {}).get("desktop", {})
    if inputs not in ({}, {"format_version": 1}):
        raise ValueError("desktop driver does not accept arbitrary fixture or command inputs")
    if (capability, case) != ("installation", "clean-install-and-launch"):
        return retain(binding, capability, case, root, "unverified",
                      {"reason": "native-desktop-interaction-driver-required"})
    transaction = installation_transaction(binding, root)
    if transaction is not None:
        first = binding.get("first_launch_transaction")
        if first is None:
            return retain(binding, capability, case, root, "unverified",
                          {"reason": "fresh-first-postinstallation-launch-not-observed"})
        first = observed_journal(binding, root, first, "first-launch-transaction", first_launch=True)
        observations = first["observations"]
        if (observations.get("execution_nonce") != binding["execution_nonce"]
                or observations.get("profile_absent_before") is not True
                or observations.get("webview_profile_absent_before") is not True
                or observations.get("preinstallation_evidence") != transaction):
            raise ValueError("first launch journal differs from the fresh preinstallation request")
        interactive = observations.get("interactive_window", {})
        if first["status"] == "passed" and (observations.get("engine_health") != 200
                or not isinstance(interactive, dict) or interactive.get("visible") is not True
                or interactive.get("sensitive") is not True or interactive.get("defunct") is not False
                or interactive.get("normal_actionable_control") is not True
                or observations.get("desktop", {}).get("capture_complete") is not True
                or observations.get("desktop", {}).get("output_limit")):
            raise ValueError("first launch journal omitted its actual interaction and capture witnesses")
        result = retain(binding, capability, case, root, first["status"],
                        {"first_postinstallation_launch": observations})
        result["evidence"].extend(transaction + first["evidence"])
        return result
    app = InstalledApp(binding)
    try:
        with app:
            app.observations["interactive_window"] = interactive_window(app)
            response = app.request("GET", "/health")
            if response["status"] != 200:
                raise ValueError("selected installed application engine is not healthy")
    finally:
        app.stop()
    complete = app.observations.get("capture_complete") and not app.observations.get("output_limit")
    if transaction is None:
        app.observations["remaining_interaction"] = "fresh-install-transaction-not-yet-observed"
    result = retain(binding, capability, case, root,
                    "passed" if complete and transaction is not None else "unverified" if complete else "failed",
                    app.observations, app.streams())
    if transaction is not None:
        result["evidence"].extend(transaction)
    return result


def diagnostic_main(arguments: list[str]) -> int:
    """A partial artifact can prove diagnostic startup, never campaign admission."""
    import argparse
    import stat
    from ..installed_execution import current_tree, execute_case, read
    from ..installed_runtime import bind
    from .native_workflows import prepare_install
    parser = argparse.ArgumentParser(description=diagnostic_main.__doc__)
    parser.add_argument('--candidate-inputs', type=Path, required=True)
    parser.add_argument('--artifact-id', required=True)
    parser.add_argument('--candidate-run-id', type=int, required=True)
    parser.add_argument('--candidate-artifact-id', type=int, required=True)
    parser.add_argument('--candidate-controller-commit', required=True)
    parser.add_argument('--source-commit', required=True)
    parser.add_argument('--source-tree', required=True)
    parser.add_argument('--driver-tree', required=True)
    parser.add_argument('--execution-nonce', required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--continue-installed', action='store_true')
    args = parser.parse_args(arguments)
    try:
        source = evidence.source(args.source_commit)
        tree = evidence.source(args.source_tree)
        driver_tree = evidence.source(args.driver_tree)
        controller = evidence.source(args.candidate_controller_commit)
        if (min(args.candidate_run_id, args.candidate_artifact_id) < 1
                or re.fullmatch(r'[a-f0-9]{32}', args.execution_nonce) is None):
            raise ValueError('diagnostic selection requires positive server IDs and its fresh nonce')
        checkout = Path(__file__).resolve().parents[3]
        if current_tree(checkout) != driver_tree:
            raise ValueError('diagnostic driver differs from the selected immutable controller')
        inputs = args.candidate_inputs
        for path in (inputs, inputs / 'packages', inputs / 'candidate-selection.json', inputs / 'final-artifacts.json'):
            info = path.lstat()
            if (path.resolve(strict=True) != path or info.st_uid != 0 or info.st_mode & 0o022
                    or not (stat.S_ISDIR(info.st_mode) if path in {inputs, inputs / 'packages'} else stat.S_ISREG(info.st_mode))):
                raise ValueError('diagnostic inputs must be physically staged read-only by the guest owner')
        selected = evidence.object_fields(read(inputs / 'candidate-selection.json'),
            {'format_version', 'partial', 'qualification_authority', 'publication_authority', 'source', 'provenance'})
        if not isinstance(selected['source'], dict) or not isinstance(selected['provenance'], dict):
            raise ValueError('diagnostic selection requires typed source and producer identities')
        final = read(inputs / 'final-artifacts.json')
        provenance = selected.get('provenance', {})
        if (type(selected.get('format_version')) is not int or selected['format_version'] != 1
                or selected.get('partial') is not True or selected.get('qualification_authority') is not False
                or selected.get('publication_authority') is not False
                or selected.get('source', {}).get('source_commit') != source
                or selected['source'].get('source_tree') != tree
                or provenance.get('run_id') != args.candidate_run_id
                or provenance.get('artifact_id') != args.candidate_artifact_id
                or provenance.get('controller_commit') != controller):
            raise ValueError('diagnostic inputs differ from the selected partial producer and application source')
        artifacts = evidence.artifacts(final, source)
        if args.artifact_id not in artifacts or artifacts[args.artifact_id]['installation'] not in {'deb', 'rpm', 'pacman'}:
            raise ValueError('diagnostic startup requires one selected native package')
        if not args.output.is_absolute() or args.output.resolve().is_relative_to(checkout):
            raise ValueError('diagnostic output must be independent from the controller checkout')
        args.output.mkdir(parents=True, mode=0o700, exist_ok=False)
        retained = args.output / 'evidence'
        retained.mkdir()
        case_inputs = {'native_packages': {'format_version': 1,
            'current_package': str(inputs / 'packages' / args.artifact_id),
            'installation': {'operation': 'fresh-install', 'expected_absent': True}}}
        transaction = None if args.continue_installed else prepare_install(
            final, source, args.artifact_id, inputs / 'packages', Path('/opt/painted-wolf-code'), case_inputs, retained)
        first = None
        if args.continue_installed or transaction['status'] == 'passed':
            binding = bind(final, source, args.artifact_id, inputs / 'packages', Path('/opt/painted-wolf-code'))
            binding.update(case_inputs=case_inputs, installation_transaction=transaction,
                           execution_nonce=args.execution_nonce)
            first = (observe_startup(binding, retained, "continued-startup",
                {"execution_nonce": args.execution_nonce, "existing_profile_preserved": True})
                if args.continue_installed else prepare_first_launch(binding, retained))
        status = first['status'] if first else transaction['status']
        cases = []
        if first is not None and first['status'] == 'passed':
            binding['first_launch_transaction'] = first
            for capability, case in [('vault', 'wrong-launcher-and-memory'), ('session', 'inhibitor-lifetime'),
                                     ('conversation', 'provider-onboarding'), ('files', 'edit-search-review')]:
                try:
                    observed = execute_case(binding, capability, case, retained)
                except (OSError, ValueError, TypeError) as error:
                    outcome = 'unverified' if isinstance(error, CaseUnavailable) else 'failed'
                    observed = retain(binding, capability, case, retained, outcome,
                        {'reason': str(error), **getattr(error, 'observations', {})}, getattr(error, 'streams', None))
                cases.append({'capability': capability, **observed})
            if any(case['status'] == 'failed' for case in cases):
                status = 'failed'
            elif any(case['status'] != 'passed' for case in cases):
                status = 'unverified'
        document = {'format_version': 1, 'scope': ('partial-native-onboarding-continuation' if args.continue_installed
                                                   else 'partial-native-first-launch-diagnostic'),
                    'source_commit': source, 'source_tree': tree, 'driver_tree': driver_tree,
                    'candidate_provenance': provenance, 'status': status, 'installation': transaction,
                    'first_launch': None if args.continue_installed else first,
                    'continued_startup': first if args.continue_installed else None, 'cases': cases, 'complete_candidate': False, 'application_qualified': False,
                    'qualification_authority': False, 'publication_authority': False}
        (args.output / 'diagnostic.json').write_text(json.dumps(document, indent=2, sort_keys=True) + '\n')
        return {'passed': 0, 'failed': 1, 'unverified': 2}[status]
    except (OSError, ValueError, TypeError, KeyError) as error:
        parser.exit(2, 'Native startup diagnostic refused: ' + str(error) + '\n')
