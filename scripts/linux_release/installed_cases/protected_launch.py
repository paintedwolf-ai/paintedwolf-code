"""Attest the installed protected launch (#1116, #1121) from kernel facts.

The case launches the installed desktop through the setuid pw-launch
bootstrap and reads, as the administrator, every process of that launch. It
applies only to a candidate whose sealed manifest lists bin/pw-launch-entry
and whose administrator selected a generation in /etc/paintedwolf/launch.json;
otherwise it reports unverified with a typed not-applicable reason. Unit-level
proof: the test:confine:linux launch leg
(TestProtectedLaunchSealsTheDesktopInARootOwnedNamespace,
TestProtectedLaunchRoutesEngineLaunchesThroughTheBroker,
TestProtectedLaunchRefusesWithoutTheSetuidTransition,
TestFullIdentityMapRejectsRemappedOrAdditionalRanges) and the relay contracts
(TestEveryLaunchIsRelayedInsideTheProtectedLaunch, TestRelayedSupervisorsStaySupervised)."""
from __future__ import annotations

from contextlib import ExitStack
import json
import os
from pathlib import Path
import shlex
import stat
import struct
import sys
import tempfile
import time
import uuid

from . import process_tree
from .app import InstalledApp, process_identity
from .approval import approve_host_execution
from .descendant_cleanup import abandon, gone, witness
from .lifecycle import SCRIPT, capture, cleanup
from .provider import MODEL, ProviderFixture
from .records import CaseUnavailable
from .workflow import ToolTurn, accepted
from ..campaign.runtime import SETUID_LAUNCHER as BOOTSTRAP, file_identity

POLICY = Path('/etc/paintedwolf/launch.json')
ENTRY = 'bin/pw-launch-entry'
POLICY_LIMIT = 1 << 20


def trusted(info: os.stat_result) -> bool:
    return info.st_uid == 0 and not stat.S_IMODE(info.st_mode) & 0o022


def read_policy(path: Path = POLICY) -> dict:
    """The administrator's launch policy, refused unless root-controlled and typed."""
    info, parent = path.lstat(), path.parent.lstat()
    if not stat.S_ISREG(info.st_mode) or not trusted(info) or not stat.S_ISDIR(parent.st_mode) or not trusted(parent):
        raise ValueError('launch policy is not a root-controlled regular file')
    if info.st_size > POLICY_LIMIT:
        raise ValueError('launch policy exceeds its bound')
    policy = json.loads(path.read_bytes())
    if not isinstance(policy, dict):
        raise ValueError('launch policy is not an object')
    for field in ('system_executables', 'library_roots'):
        values = policy.get(field, [])
        if (not isinstance(values, list) or len(values) > 256
                or any(not isinstance(value, str) or not value.startswith('/') or '\0' in value for value in values)):
            raise ValueError(f'launch policy {field} is not a list of absolute paths')
    return policy


def applicability(binding: dict) -> tuple[dict | None, str | None]:
    """The selected policy and bootstrap, or the typed reason the case does not apply."""
    root = Path(binding['install_root'])
    entry = binding['images'].get(str(root / ENTRY))
    if entry is None:
        return None, 'protected-launch-entry-not-in-manifest'
    if entry.get('mode', 0o755) != 0o755:
        raise ValueError('installed launch entry is not a 0755 program')
    try:
        policy = read_policy()
    except FileNotFoundError:
        return None, 'protected-launch-policy-absent'
    expected = binding['images'].get(str(BOOTSTRAP))
    if expected is None:
        return None, 'protected-launch-bootstrap-absent'
    try:
        info = BOOTSTRAP.lstat()
    except FileNotFoundError:
        return None, 'protected-launch-bootstrap-absent'
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_gid != 0
            or stat.S_IMODE(info.st_mode) != 0o4755):
        raise ValueError('installed launch bootstrap is not a root-owned setuid program')
    bootstrap = file_identity(BOOTSTRAP)
    bootstrap.pop('header')
    if expected and bootstrap != expected:
        raise ValueError('installed launch bootstrap differs from its sealed manifest')
    return {'policy': policy, 'bootstrap': {'path': str(BOOTSTRAP), **bootstrap}}, None


def interpreter(path: Path) -> str | None:
    """The ELF program interpreter (PT_INTERP) of a 64-bit little-endian image."""
    with path.open('rb') as stream:
        header = stream.read(64)
        if header[:6] != b'\x7fELF\x02\x01':
            raise ValueError('desktop image is not a 64-bit little-endian ELF')
        offset, size, count = struct.unpack_from('<Q', header, 32)[0], *struct.unpack_from('<HH', header, 54)
        for index in range(min(count, 128)):
            stream.seek(offset + index * size)
            kind, _, file_offset, _, _, length = struct.unpack('<IIQQQQ', stream.read(40))
            if kind == 3:
                stream.seek(file_offset)
                return stream.read(min(length, 4096)).rstrip(b'\0').decode()
    return None


def within(path: str, roots: list[str]) -> bool:
    return any(path == root or path.startswith(root.rstrip('/') + '/') for root in roots)


def attest(document: dict, *, engine: int, desktop: list[int], engine_image: list[int],
           system: list[str], exec_roots: list[str], memory: dict[int, str], uid: int) -> dict:
    """Every protected-launch property, decided from one observed launch tree."""
    rows = document['processes']
    desktops = [row for row in rows if row['image'] == desktop]
    engines = [row for row in rows if row['identity']['pid'] == engine]
    webkit = [row for row in rows if row['exe'] in system]
    protected = desktops + engines + webkit
    checks = {'desktop-observed': len(desktops) == 1, 'engine-observed': len(engines) == 1, 'webkit-observed': bool(webkit)}
    if not all(checks.values()):
        return {'checks': checks, 'passed': False}
    namespace = desktops[0]['namespaces']['user']
    checks.update({
        'memory-open-refused': all(memory.get(row['identity']['pid']) == 'EACCES' for row in protected),
        'no-root-process': all(0 not in row['uids'] for row in rows),
        'shared-protected-namespace': (all(row['namespaces']['user'] == namespace for row in protected)
                                       and namespace != document['initial']['namespaces']['user']),
        'namespace-owned-by-root': all(row['user_namespace_owner'] == 0 for row in protected),
        'full-identity-maps': all(row['uid_map'] == process_tree.FULL_MAP and row['gid_map'] == process_tree.FULL_MAP for row in protected),
        'no-new-privileges': all(row['no_new_privs'] == 1 for row in protected),
        'no-capabilities': all(not any(row['capabilities'].values()) for row in protected),
        'caller-identity': all(row['uids'] == [uid] * 4 for row in protected),
        # Anything else inside would be an unrouted program, including a desktop handler.
        'only-closure-inside': all(row['image'] in (desktop, engine_image) or row['exe'] in system
                                   for row in rows if row['namespaces']['user'] == namespace),
        'execution-only-from-closure': bool(desktops[0].get('mounts')) and all(
            'noexec' in options.split(',') or within(point, exec_roots) for point, options in desktops[0]['mounts']),
    })
    return {'checks': checks, 'passed': all(checks.values()), 'protected_namespace': namespace,
            'webkit': [row['exe'] for row in webkit]}


def await_tree(launch: dict, desktop: list[int], system: list[str], limit: float = 60) -> dict:
    """Observes the launch once WebKit runs, with the desktop's mount table."""
    deadline = time.monotonic() + limit
    while time.monotonic() < deadline:
        document = process_tree.observe(launch)
        desktops = [row['identity']['pid'] for row in document['processes'] if row['image'] == desktop]
        if len(desktops) == 1 and any(row['exe'] in system for row in document['processes']):
            return process_tree.observe(launch, mounts=tuple(desktops))
        time.sleep(0.2)
    raise CaseUnavailable('protected launch did not start its desktop and WebKit processes')


def running_outside(app, project: Path, namespace: list[int], initial: list[int], owned: list[dict], *,
                    host_execution: bool) -> dict:
    """Runs one command and reads which user namespace its live process occupies."""
    script = project / 'lifecycle.py'
    script.write_text(SCRIPT)
    arguments = {'command': shlex.join([str(Path(sys.executable).resolve()), str(script), 'single']),
                 'background': True, 'wait_ms': 1, 'timeout_ms': 90000}
    if host_execution:
        arguments['capability_request'] = {'host_execution': True}
    with ProviderFixture([{'name': 'command', 'arguments': arguments}]) as provider, ToolTurn(app, provider, project) as turn:
        turn.submit()
        if host_execution:
            approve_host_execution(turn, arguments)
        turn.wait(timeout=90)
        members = capture(witness(turn.workspace, 30), host_execution)
        owned.extend(members)
        if len(members) != 1:
            raise ValueError('relayed command fixture was not live for observation')
        member = process_identity(members[0]['pid'])
        if str(member['start_time_ticks']) != members[0]['start_ticks']:
            raise ValueError('relayed command changed before observation')
        row = [row for row in process_tree.observe(member)['processes'] if row['identity'] == member][0]
        (turn.workspace / 'qualification-release').write_text('release fixed fixture\n')
        user = row['namespaces']['user']
        outside = user != namespace and (user == initial if host_execution else True)
        return {'outside': outside, 'user_namespace': user, 'members': members, 'turn': turn.observations}


def vault_release(app) -> dict:
    """The identity service attests this launch: a credential write reaches the vault."""
    from .security import service_pid
    service = service_pid()
    provider = 'qualification-vault-' + uuid.uuid4().hex[:12]
    accepted(app, 'POST', '/v1/providers', {
        'id': provider, 'kind': 'openai-compatible', 'base_url': 'http://127.0.0.1:9/v1', 'requires_api_key': True,
        'models': [{'id': MODEL, 'context_length': 131072, 'capabilities': {
            name: {'state': 'supported' if name in {'chat', 'streaming', 'tools'} else 'unsupported', 'sources': ['installed-fixture']}
            for name in ('chat', 'streaming', 'tools', 'vision', 'reasoning', 'structured_output', 'prompt_caching')}}]}, {201})
    try:
        accepted(app, 'PUT', f'/v1/providers/{provider}/credential', {'api_key': 'installed-vault-fixture-' + uuid.uuid4().hex}, {200})
        rows = accepted(app, 'GET', '/v1/providers', None, {200}).get('providers')
        if not isinstance(rows, list):
            raise ValueError('installed provider list has an invalid shape')
        stored = [row for row in rows if isinstance(row, dict) and row.get('id') == provider]
        present = len(stored) == 1 and stored[0].get('credential_present') is True
    finally:
        accepted(app, 'DELETE', f'/v1/providers/{provider}', None, {200, 204})
    return {'service_pid': service, 'service_stable': service_pid() == service, 'credential_present': present,
            'released': present and service_pid() == service}


def protected_launch(binding: dict) -> tuple[str, dict, dict]:
    selected, reason = applicability(binding)
    if reason is not None:
        return 'unverified', {'reason': reason, 'applicability': 'not-applicable'}, {}
    root = Path(binding['install_root'])
    policy, system = selected['policy'], selected['policy'].get('system_executables', [])
    desktop_path = root / 'bin/painted-wolf-code'
    record, engine_record = binding['images'][str(desktop_path)], binding['images'][str(root / 'bin/pw')]
    desktop = [record['device'], record['inode'], record['bytes']]
    engine_image = [engine_record['device'], engine_record['inode'], engine_record['bytes']]
    exec_roots = [str(root), *system, *policy.get('library_roots', []), *filter(None, [interpreter(desktop_path)])]
    owned, tree = [], None
    try:
        with tempfile.TemporaryDirectory(prefix='paintedwolf-protected-launch-') as directory, ExitStack() as stack:
            work = Path(directory)
            app = stack.enter_context(InstalledApp(binding, launcher=selected['bootstrap']))
            launch, engine = app.observations['launch_identity'], app.observations['engine_identity']
            tree = await_tree(launch, desktop, system)
            memory = {row['identity']['pid']: process_tree.memory_open_errno(row['identity']['pid']) for row in tree['processes']}
            attestation = attest(tree, engine=engine['pid'], desktop=desktop, engine_image=engine_image, system=system,
                                 exec_roots=exec_roots, memory=memory, uid=os.getuid())
            namespace, initial = attestation.get('protected_namespace'), tree['initial']['namespaces']['user']
            relayed = {}
            for name, host in (('agent-command', False), ('host-execution', True)):
                (work / name).mkdir()
                relayed[name] = running_outside(app, work / name, namespace, initial, owned, host_execution=host)
            (work / 'abandoned').mkdir()
            abandoned, abandoned_facts = abandon(app, work / 'abandoned', owned)
            vault = vault_release(app)
        launched = [{'pid': row['identity']['pid'], 'start_ticks': str(row['identity']['start_time_ticks'])} for row in tree['processes']]
        ended = gone(launched)
        passed = (attestation['passed'] and all(row['outside'] for row in relayed.values())
                  and abandoned == 'passed' and vault['released'] and ended)
        return ('passed' if passed else 'failed',
                {'applicability': 'applicable', 'bootstrap': selected['bootstrap'], 'attestation': attestation,
                 'memory_open': memory, 'relayed': relayed, 'abandoned_broker_call': abandoned_facts, 'vault': vault,
                 'launch_tree_ended': ended, 'app': app.observations}, app.streams())
    finally:
        cleanup(owned)
