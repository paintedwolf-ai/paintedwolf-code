"""Read bounded nonsecret identities from the captured GUI's disposable profile."""
from __future__ import annotations

from contextlib import ExitStack, contextmanager
import os
from pathlib import Path
import re
import sqlite3
import stat
import time
import uuid

from .products import require_owned_guest
from .app import engine_image
from .records import CaseUnavailable

TOOLS = frozenset({'command', 'read', 'write', 'edit', 'list', 'search', 'view_image', 'page_open',
                   'terminal_open', 'terminal_send', 'terminal_read', 'terminal_close'})
COLUMNS = {
    'projects': {'id': 'TEXT'},
    'project_roots': {'id': 'TEXT', 'project_id': 'TEXT', 'path': 'TEXT'},
    'sessions': {'id': 'TEXT', 'project_id': 'TEXT', 'status': 'TEXT', 'provider_id': 'TEXT', 'model': 'TEXT'},
    'messages': {'id': 'TEXT', 'session_id': 'TEXT', 'role': 'TEXT', 'tool_result_json': 'TEXT'},
}


def identifier(value):
    if not isinstance(value, str) or str(uuid.UUID(value)) != value:
        raise ValueError('profile observation requires a canonical durable UUID')
    return value


def physical(path, directory=False):
    info = path.lstat()
    expected = stat.S_ISDIR if directory else stat.S_ISREG
    if (not expected(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077
            or path.resolve(strict=True) != path or (not directory and info.st_size > 128 << 20)):
        raise ValueError('profile observation requires private physical owned storage')
    return info.st_dev, info.st_ino, info.st_uid, info.st_mode


def schema(connection, required=None):
    if connection.execute('PRAGMA user_version').fetchone() != (4,):
        raise ValueError('profile observer does not support this store revision')
    columns = {table: dict(fields) for table, fields in COLUMNS.items()}
    for table, fields in (required or {}).items():
        columns.setdefault(table, {}).update(fields)
    for table, required in columns.items():
        row = connection.execute('SELECT type, sql FROM sqlite_schema WHERE name = ?', (table,)).fetchone()
        if row is None or row[0] != 'table' or not re.match(r'CREATE TABLE\b', row[1], re.IGNORECASE):
            raise ValueError('profile observer requires the declared physical tables')
        observed = {row[1]: row[2] for row in connection.execute('PRAGMA table_xinfo(' + table + ')')}
        if any(observed.get(name) != kind for name, kind in required.items()):
            raise ValueError('profile observer required column contract differs')


@contextmanager
def profile(app):
    require_owned_guest(app.binding)
    app.require_running()
    app.require_engine()
    engine = app.observations.get('engine_identity')
    if not isinstance(engine, dict):
        raise ValueError('profile observer requires the captured ready engine')
    engine = dict(engine)
    expected = app.binding['images'][str(Path(app.binding['install_root']) / 'bin/pw')]
    image = expected['device'], expected['inode'], expected['bytes']
    if engine_image(engine['pid']) != image:
        raise ValueError('profile observer engine image differs from the installed artifact')
    directory = app.selected_storage().config
    for ancestor in (directory.parent, directory.parent.parent):
        info = ancestor.lstat()
        if (not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o022
                or ancestor.resolve(strict=True) != ancestor):
            raise ValueError('profile observation crosses an unsafe owned ancestor')
    before = physical(directory, True)
    yield directory
    app.require_running()
    app.require_engine()
    if (app.observations.get('engine_identity') != engine or engine_image(engine['pid']) != image
            or app.selected_storage().config != directory or physical(directory, True) != before):
        raise ValueError('captured profile or engine changed during observation')


def sidecar_identity(info, path):
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_nlink != 1
            or stat.S_IMODE(info.st_mode) not in {0o600, 0o644} or info.st_size > 128 << 20
            or path.resolve(strict=True) != path):
        raise CaseUnavailable('SQLite-sidecar-is-not-a-declared-physical-owned-file')
    return info.st_dev, info.st_ino, info.st_uid, info.st_gid, info.st_mode, info.st_nlink


@contextmanager
def sqlite_sidecars(path):
    parent = physical(path.parent, True)
    if stat.S_IMODE(parent[3]) != 0o700:
        raise CaseUnavailable('SQLite-sidecars-require-the-pinned-private-profile')
    with ExitStack() as custody:
        held = []
        for suffix in ('-wal', '-shm'):
            sidecar = Path(str(path) + suffix)
            try:
                descriptor = os.open(sidecar, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            except OSError:
                raise CaseUnavailable('SQLite-sidecar-file-unavailable') from None
            custody.callback(os.close, descriptor)
            try:
                before = sidecar_identity(os.fstat(descriptor), sidecar)
                if sidecar_identity(sidecar.lstat(), sidecar) != before:
                    raise CaseUnavailable('SQLite-sidecar-changed-before-observation')
            except FileNotFoundError:
                raise CaseUnavailable('SQLite-sidecar-disappeared-before-observation') from None
            held.append((descriptor, sidecar, before))
        try:
            yield
        finally:
            try:
                if physical(path.parent, True) != parent:
                    raise CaseUnavailable('SQLite-profile-changed-during-observation')
                for descriptor, sidecar, before in held:
                    if (sidecar_identity(os.fstat(descriptor), sidecar) != before
                            or sidecar_identity(sidecar.lstat(), sidecar) != before):
                        raise CaseUnavailable('SQLite-sidecar-changed-during-observation')
            except FileNotFoundError:
                raise CaseUnavailable('SQLite-sidecar-disappeared-during-observation') from None


@contextmanager
def _database(app, *, required=None):
    with profile(app) as directory:
        path = directory / 'store.db'
        before = physical(path)
        # Existing sidecars avoid creating an observer-owned WAL/SHM pair.
        # Their descriptor custody spans the readonly SQLite transaction.
        with sqlite_sidecars(path):
            connection = sqlite3.connect(path.as_uri() + '?mode=ro', uri=True, timeout=1)
            deadline, instructions = time.monotonic() + 2, 0
            def progress():
                nonlocal instructions
                instructions += 1000
                return int(instructions > 200000 or time.monotonic() > deadline)
            connection.set_progress_handler(progress, 1000)
            try:
                connection.execute('PRAGMA query_only = ON')
                connection.execute('PRAGMA trusted_schema = OFF')
                connection.execute('BEGIN')
                schema(connection, required)
                yield connection
                if physical(path) != before:
                    raise ValueError('captured profile or engine changed during observation')
            finally:
                connection.close()


def rows(cursor):
    result = cursor.fetchmany(257)
    if len(result) > 256:
        raise ValueError('profile observation exceeds its fixed row bound')
    return result


def project_for_owned_root(app, fixture_path):
    path = Path(fixture_path)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or path.resolve(strict=True) != path:
        raise ValueError('project observer requires its physical owned fixture root')
    with _database(app) as connection:
        found = rows(connection.execute('SELECT p.id,r.id FROM projects p JOIN project_roots r ON r.project_id=p.id WHERE r.path=? LIMIT 257', (str(path),)))
    if len(found) > 1:
        raise ValueError('fixture root has ambiguous project ownership')
    return None if not found else dict(zip(('project_id', 'root_id'), map(identifier, found[0])))


def sessions_for_project(app, project_id):
    identifier(project_id)
    with _database(app, required={'sessions': {'parent_session_id': 'TEXT'}}) as connection:
        found = rows(connection.execute('SELECT id,status,provider_id,model,parent_session_id FROM sessions WHERE project_id=? ORDER BY id LIMIT 257', (project_id,)))
    if any(status not in {'preparing', 'idle', 'busy', 'error'} for _, status, _, _, _ in found):
        raise ValueError('profile observer encountered an undeclared session state')
    if any(any(value is not None and (not isinstance(value, str) or not 1 <= len(value) <= 128)
               for value in (provider, model)) for _, _, provider, model, _ in found):
        raise ValueError('profile observer provider identity exceeds its declared bound')
    return [{'id': identifier(key), 'status': status, 'provider_id': provider, 'model': model,
             'parent_session_id': None if parent is None else identifier(parent)}
            for key, status, provider, model, parent in found]


def invocations(app, session_id, tool_names):
    identifier(session_id)
    if (not isinstance(tool_names, (list, tuple)) or not tool_names or len(tool_names) > len(TOOLS)
            or any(not isinstance(name, str) or name not in TOOLS for name in tool_names)
            or len(set(tool_names)) != len(tool_names)):
        raise ValueError('profile observer requires fixed declared fixture tools')
    with _database(app) as connection:
        found = rows(connection.execute("SELECT id,length(CAST(tool_result_json AS BLOB)),json_extract(CASE WHEN length(CAST(tool_result_json AS BLOB))<=1048576 THEN tool_result_json ELSE '{}' END,'$.tool_call_id'),json_extract(CASE WHEN length(CAST(tool_result_json AS BLOB))<=1048576 THEN tool_result_json ELSE '{}' END,'$.tool'),json_extract(CASE WHEN length(CAST(tool_result_json AS BLOB))<=1048576 THEN tool_result_json ELSE '{}' END,'$.outcome') FROM messages WHERE session_id=? AND role='tool' AND tool_result_json IS NOT NULL AND tool_result_json!='' ORDER BY id LIMIT 257", (session_id,)))
    result = []
    for message_id, length, call_id, tool, outcome in found:
        if type(length) is not int or length > 1 << 20:
            raise ValueError('profile observer tool result exceeds its fixed byte bound')
        if tool not in tool_names:
            continue
        if (not isinstance(call_id, str) or not re.fullmatch(r'[A-Za-z0-9_.:-]{1,128}', call_id)
                or outcome not in {'completed', 'rejected', 'error'}):
            raise ValueError('profile observer encountered an undeclared tool identity or outcome')
        result.append({'message_id': identifier(message_id), 'tool_call_id': call_id, 'tool': tool, 'outcome': outcome})
    return result
