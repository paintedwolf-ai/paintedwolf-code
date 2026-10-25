"""Qualify the applied boundary of every installed helper family.

Each family runs through its ordinary installed path. An administrator
observer reads the live helper's namespaces, seccomp mode, no_new_privs and
capabilities against the process that started it; families that run fixture
code also report, from inside, that denied writes and reads (including a
protected name created after launch, #1113) fail. Unit-level proof: the
test:confine:linux sandbox leg (TestLinuxWritesStayInsideWriteRoots,
TestLinuxDeniedReadsAreHidden, TestLinuxControlPlaneIsHiddenButSessionScratchRestored,
TestLinuxEscapeSyscallsKillTheProcess, TestLinuxCapabilitiesReportAnAppliedBoundary,
TestLinuxHelperRelaunchesFromOutsideTheEnginesNamespaces) and its helper leg
(TestLinuxStdioMCPAppliedBoundary, TestLinuxScannerBoundaryKeepsSourceReadOnlyAndOutputWritable,
TestLinuxDocumentCoreAppliedBoundary)."""
from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack
import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import time
import uuid

from . import process_tree
from .app import InstalledApp, process_identity
from .browser import browser_sandbox
from .helper_probe import WITNESS, Fixtures, domain_failures, read_witness
from .kernel import namespace_observer
from .process import probe_path, run
from .provider import ProviderFixture
from .records import CaseFailed, CaseUnavailable
from .security_boundary import successful_invocation
from .terminal import INPUT, TerminalProvider, transcript_results
from .workflow import ToolTurn, accepted, identity, wait_settled

FAMILIES = ('agent-command', 'terminal', 'background-job', 'scanner-opengrep', 'scanner-bialy',
            'document-core', 'ocr-engine', 'local-mcp', 'managed-browser')


def outcome(status: str, reason: str | None = None, **facts) -> dict:
    if status not in {'passed', 'failed', 'unverified'} or (status != 'passed') != (reason is not None):
        raise ValueError('helper family outcome needs a typed reason exactly when it did not pass')
    return {'status': status, **({'reason': reason} if reason else {}), **facts}


def tree_outcome(document: dict | None, images: list[list[int]], outer_pid: int, uid: int) -> dict:
    """A family's verdict from the observed live helpers it started."""
    if document is None:
        return outcome('unverified', 'helper-not-observed')
    rows = process_tree.matching(document, images)
    if not rows:
        return outcome('unverified', 'helper-not-observed')
    outer = [row for row in document['processes'] if row['identity']['pid'] == outer_pid]
    if len(outer) != 1:
        raise ValueError('helper observation lost the process that started the helper')
    failures = {str(row['identity']['pid']): process_tree.boundary_failures(row, outer[0]['namespaces'], uid) for row in rows}
    failed = sorted({failure for values in failures.values() for failure in values})
    return outcome('failed' if failed else 'passed', ','.join(failed) or None,
                   helpers=[{key: row[key] for key in ('identity', 'exe', 'namespaces', 'seccomp', 'no_new_privs', 'capabilities')}
                            for row in rows], outer_namespaces=outer[0]['namespaces'], failures=failures)


def sample(root: dict, images: list[list[int]], running, *, limit: float = 90) -> dict | None:
    """Observes root's tree until a sealed helper image is live or the operation ends."""
    deadline = time.monotonic() + limit
    while time.monotonic() < deadline:
        document = process_tree.observe(root)
        if process_tree.matching(document, images):
            return document
        if not running():
            return None
        time.sleep(0.02)
    return None


def aggregate(results: dict) -> str:
    if set(results) != set(FAMILIES) or any(row.get('status') not in {'passed', 'failed', 'unverified'} for row in results.values()):
        raise ValueError('helper family qualification omitted a declared family')
    statuses = {row['status'] for row in results.values()}
    return 'failed' if 'failed' in statuses else 'unverified' if 'unverified' in statuses else 'passed'


def guarded(function) -> dict:
    """One family's outcome, classifying exceptions as the security dispatcher does."""
    try:
        return function()
    except CaseFailed as error:
        return outcome('failed', 'installed-interaction-failed', detail=str(error))
    except (CaseUnavailable, OSError, subprocess.SubprocessError) as error:
        return outcome('unverified', 'installed-interaction-unavailable', error_type=type(error).__name__)
    except (ValueError, TypeError, KeyError, json.JSONDecodeError) as error:
        return outcome('failed', 'installed-contract-refused', error_type=type(error).__name__, detail=str(error)[:512])


def images(records: dict, paths: list[Path]) -> list[list[int]]:
    selected = [records.get(str(path)) for path in paths]
    if not paths or any(row is None for row in selected):
        raise CaseUnavailable('selected installed manifest omits a helper family image')
    return [[row['device'], row['inode'], row['bytes']] for row in selected]


def interpreter() -> list[list[int]]:
    info = Path(sys.executable).resolve().stat()
    return [[info.st_dev, info.st_ino, info.st_size]]


def in_domain(app, fixtures: Fixtures, observation: dict, *, project: bool, **facts) -> dict:
    outer = namespace_observer(app.observations['engine_identity'])
    failures = domain_failures(observation, outer, project=project)
    if not fixtures.intact():
        failures.append('outside-marker-changed')
    return outcome('failed' if failures else 'passed', ','.join(failures) or None,
                   observation=observation, engine_namespaces=outer, **facts)


def command_family(app, fixtures: Fixtures, root: Path, *, background: bool) -> dict:
    project = root / ('background' if background else 'command')
    project.mkdir()
    arguments = {'command': shlex.join(fixtures.command('file'))}
    if background:
        arguments.update({'background': True, 'wait_ms': 1, 'timeout_ms': 60000})
    with ProviderFixture([{'name': 'command', 'arguments': arguments}]) as provider, ToolTurn(app, provider, project) as turn:
        turn.submit()
        results = turn.wait(timeout=90)
        if not background and not successful_invocation(results):
            return outcome('failed', 'command-not-invoked', turn=turn.observations)
        observation = read_witness(turn.workspace / WITNESS)
        return in_domain(app, fixtures, observation, project=True, turn=turn.observations)


def terminal_family(app, fixtures: Fixtures, root: Path) -> dict:
    project = root / 'terminal'
    project.mkdir()
    with TerminalProvider(shlex.join(fixtures.command('terminal', INPUT))) as provider, ToolTurn(app, provider, project) as turn:
        accepted(app, 'POST', f'/v1/sessions/{turn.session_id}/prompts', {
            'operation_id': str(uuid.uuid4()), 'text': 'Use terminal_open, terminal_send, terminal_read and terminal_close to run the installed terminal fixture.'}, {202})
        turn.wait(timeout=90)
        wait_settled(app, turn.session_id)
        if provider.step(transcript_results(turn.results())) is not None:
            return outcome('failed', 'terminal-lifecycle-incomplete', turn=turn.observations)
        observation = read_witness(turn.workspace / WITNESS)
        return in_domain(app, fixtures, observation, project=True, turn=turn.observations)


def scanner_families(binding: dict, app, root: Path) -> dict:
    install = Path(binding['install_root'])
    opengrep = [Path(path) for path in binding['images'] if Path(path).name == 'opengrep'
                and Path(path).is_relative_to(install / 'engine-root')]
    selected = {'scanner-opengrep': images(binding['images'], opengrep),
                'scanner-bialy': images(binding['images'], [install / 'bin/bialy'])}
    project = root / 'scanners'
    project.mkdir()
    (project / 'fixture.py').write_text('import subprocess\nsubprocess.call("echo " + input(), shell=True)\n')
    created = accepted(app, 'POST', '/v1/projects', {'name': 'Installed scanner qualification',
                                                     'roots': [{'path': str(project), 'is_primary': True}]}, {201})
    accepted(app, 'POST', f'/v1/projects/{identity(created)}/scans', {'kind': 'full'}, {202})
    engine = app.observations['engine_identity']
    observed = {name: None for name in selected}
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline and any(value is None for value in observed.values()):
        document = process_tree.observe(engine)
        for name, image in selected.items():
            if observed[name] is None and process_tree.matching(document, image):
                observed[name] = document
        time.sleep(0.02)
    return {name: tree_outcome(observed[name], selected[name], engine['pid'], os.getuid()) for name in selected}


def mcp_family(app, fixtures: Fixtures) -> dict:
    provider = 'qualification-' + uuid.uuid4().hex[:12]
    command = fixtures.command('mcp')
    accepted(app, 'POST', '/v1/mcp/providers', {'source': 'custom', 'id': provider, 'command': command[0],
                                                'args': command[1:], 'enabled': True}, {201})
    try:
        deadline, tools = time.monotonic() + 30, []
        while not tools and time.monotonic() < deadline:
            tools = accepted(app, 'GET', f'/v1/mcp/providers/{provider}/tools', None, {200}).get('tools')
            if not isinstance(tools, list) or len(tools) > 1 or any(not isinstance(row, dict) for row in tools):
                return outcome('failed', 'mcp-tools-malformed', tools=tools)
            time.sleep(0.2)
        if not tools:
            return outcome('unverified', 'mcp-tools-not-discovered')
        engine = app.observations['engine_identity']
        document = process_tree.observe(engine)
        observed = tree_outcome(document, interpreter(), engine['pid'], os.getuid())
        if observed['status'] != 'passed':
            return observed
        reported = in_domain(app, fixtures, json.loads(tools[0].get('description', 'null')), project=False)
        return outcome(reported['status'], reported.get('reason'), helpers=observed['helpers'], in_domain=reported)
    finally:
        accepted(app, 'DELETE', f'/v1/mcp/providers/{provider}', None, {200, 204})


def cli_family(binding: dict, verb: str, paths: list[Path]) -> tuple[dict, dict]:
    """Runs one engine diagnostic and observes its helper against the diagnostic's own namespaces."""
    selected = images(binding.get('package_images', binding['images']), paths)
    me = process_identity(os.getpid())
    with ThreadPoolExecutor(max_workers=1) as executor:
        future = executor.submit(run, binding, 'pw', ['diagnostics', verb], timeout=180)
        document = sample(me, selected, lambda: not future.done(), limit=180)
        result = future.result()
    streams = {f'{verb}-{name}': result[name] for name in ('stdout', 'stderr')}
    if result['exit_code'] != 0 or result['timed_out'] or result['output_limit']:
        return outcome('failed', 'diagnostic-failed', exit_code=result['exit_code'], timed_out=result['timed_out']), streams
    if document is not None:
        pw = [row['identity']['pid'] for row in document['processes']
              if row['identity']['parent_pid'] == me['pid'] and row['identity']['pid'] == result['launch_identity']['pid']]
        if len(pw) == 1:
            return tree_outcome(document, selected, pw[0], os.getuid()), streams
    return outcome('unverified', 'helper-not-observed'), streams


def browser_family(binding: dict) -> tuple[dict, dict]:
    status, facts, streams = browser_sandbox(binding)
    return (outcome(status, None if status == 'passed' else 'browser-boundary-' + status, observation=facts),
            {'browser-' + name: value for name, value in streams.items()})


def helper_family_boundaries(binding: dict) -> tuple[str, dict, dict]:
    results, streams = {}, {}
    with tempfile.TemporaryDirectory(prefix='paintedwolf-helper-families-') as directory, ExitStack() as stack:
        root = Path(directory)
        fixtures = stack.enter_context(Fixtures())
        app = stack.enter_context(InstalledApp(binding))
        results['agent-command'] = guarded(lambda: command_family(app, fixtures, root, background=False))
        results['background-job'] = guarded(lambda: command_family(app, fixtures, root, background=True))
        results['terminal'] = guarded(lambda: terminal_family(app, fixtures, root))
        scanners = guarded(lambda: scanner_families(binding, app, root))
        for name in ('scanner-opengrep', 'scanner-bialy'):
            # A refused scan pass is one outcome shared by both scanner families.
            results[name] = scanners if 'status' in scanners else scanners[name]
        results['local-mcp'] = guarded(lambda: mcp_family(app, fixtures))
        stack.close()
        streams.update(app.streams())
    for name, verb, paths in (('document-core', 'document-core', [Path(binding['install_root']) / 'bin/pw-document-core']),
                              ('ocr-engine', 'ocr-engine', None)):
        def family(verb=verb, paths=paths):
            selected = paths or [probe_path(binding, 'ocr-observer')]
            result, captured = cli_family(binding, verb, selected)
            streams.update(captured)
            return result
        results[name] = guarded(family)

    def browser():
        result, captured = browser_family(binding)
        streams.update(captured)
        return result
    results['managed-browser'] = guarded(browser)
    return aggregate(results), {'families': results}, streams
