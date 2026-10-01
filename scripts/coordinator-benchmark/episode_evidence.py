"""Read typed application facts produced by the frozen Go driver."""
import json
import os
import pathlib
import subprocess
import uuid
from progress import save
from snapshot import file_hash

VERSION = 1


def path_for(capture, session):
    return capture / 'episode-evidence' / (str(uuid.UUID(session)) + '.json')


def export(capture, case, source):
    target = path_for(capture, case['session_id'])
    database = capture / 'store.db'
    before = file_hash(database)
    wal = capture / 'store.db-wal'
    command = [str(source / '.bin/lycaon-debug'), 'eval', 'evidence', '--capture', str(capture),
               '--session', case['session_id']]
    project = pathlib.Path(case.get('project_dir') or '')
    if case.get('project_dir') and not case.get('invalid_artifact') and project.is_dir():
        command += ['--project', str(project)]
    environment={**os.environ,'LYCAON_DEV':'1','LYCAON_CONFIG_ROOT':str(source/'lycaon')}
    result = subprocess.run(command, capture_output=True, text=True, check=True, cwd=source, env=environment)
    facts = json.loads(result.stdout)
    if (facts['version'] != VERSION or facts['root_session_id'] != case['session_id']
            or file_hash(database) != before or (wal.exists() and wal.stat().st_size)):
        raise ValueError('application evidence changed during extraction')
    target.parent.mkdir(exist_ok=True)
    save(target, facts)
    return facts


def read(capture, session):
    facts = json.loads(path_for(capture, session).read_text())
    if facts['version'] != VERSION or facts['root_session_id'] != session:
        raise ValueError('application evidence does not identify the requested session')
    return facts


def member(facts, session):
    return next(s for s in facts['sessions'] if s['id'] == session)
