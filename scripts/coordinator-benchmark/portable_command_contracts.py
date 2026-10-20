"""Frozen portable-command inputs. Loading or preparing them never runs a program."""
import base64
import hashlib
import pathlib
from focused_outcomes import read_json

ROOT = pathlib.Path(__file__).resolve().parents[2]
DIRECTORY = ROOT / 'lycaon/test/fixtures/eval/portable-command'
SUITE = DIRECTORY / 'suite.json'
PROFILES = {'implement', 'worker_readonly', 'explore_readonly', 'plan_review_readonly'}
STOPS = {None, 'rejected', 'conflict', 'cancelled', 'incomplete', 'budget', 'interrupted'}


def digest(value):
    return hashlib.sha256(value).hexdigest()


def fixture_path(name):
    if not isinstance(name, str):
        raise ValueError('fixture path must be a string')
    path = pathlib.PurePosixPath(name)
    if (not name or path.is_absolute()
            or '..' in path.parts or '.' == name or '\\' in name):
        raise ValueError('fixture path must be exact repository-relative data')
    return path


def bytes_value(value):
    if isinstance(value, str):
        return value.encode('utf-8')
    if isinstance(value, dict) and set(value) == {'base64'}:
        return base64.b64decode(value['base64'], validate=True)
    raise ValueError('fixture bytes require text or explicit base64')


def load_suite(path=SUITE):
    suite = read_json(path)
    validate_suite(suite)
    validate_auxiliary(suite)
    return suite


def validate_auxiliary(suite):
    language = read_json(ROOT / fixture_path(suite['language_source']))
    if not language or language.get('version') != 1:
        raise ValueError('missing version 1 prerequisite language contract')
    cases = {case['id']: case for case in suite['cases']}
    matrix = read_json(DIRECTORY / 'oar-matrix.json')
    if not matrix or matrix.get('version') != 1 or not matrix.get('cases'):
        raise ValueError('missing OAR matrix')
    for row in matrix['cases']:
        if row.get('automatic_execution') is not False or row['case_id'] not in cases:
            raise ValueError('invalid OAR admission contract')
        if cases[row['case_id']]['expected'].get('recovery') != row:
            raise ValueError('OAR matrix differs from frozen case recovery')
    controls = read_json(DIRECTORY / 'controls.json')
    behavior = {name for name, case in cases.items() if case['expected'].get('behavior_contract')}
    if set(controls or {}) != behavior:
        raise ValueError('behavior controls do not close over independent oracles')
    for control in controls.values():
        if not control.get('positive') or not control.get('negative'):
            raise ValueError('missing positive/negative behavior control')


def validate_suite(suite):
    if not isinstance(suite, dict) or suite.get('version') != 1:
        raise ValueError('unsupported portable-command fixture suite')
    if suite.get('acceptance_revision') != 1 or not suite.get('cases'):
        raise ValueError('missing frozen acceptance revision/cases')
    seen = set()
    for case in suite['cases']:
        validate_case(case)
        if case['id'] in seen:
            raise ValueError('duplicate portable-command case')
        seen.add(case['id'])
    if set(suite['required_cases']) != seen or len(suite['required_cases']) != len(seen):
        raise ValueError('fixture inventory does not close over required cases')
    covered = {q for case in suite['cases'] for q in case['qualification_cases']}
    if covered != {f'Q{i:02}' for i in range(1, 23)}:
        raise ValueError('qualification case coverage is incomplete')
    validate_resources(suite['resource_profiles'])


def validate_case(case):
    if not case.get('id') or case.get('profile') not in PROFILES:
        raise ValueError('missing case identity/profile')
    if not case.get('qualification_cases') or not case.get('turns'):
        raise ValueError('missing qualification/program sequence')
    setup, expected = case['setup'], case['expected']
    for group in ('files', 'drafts'):
        for name, value in setup[group].items():
            fixture_path(name)
            bytes_value(value['bytes'] if group == 'drafts' else value)
            if group == 'drafts' and not value.get('revision'):
                raise ValueError('draft lacks captured revision')
    for turn in case['turns']:
        if turn.get('expected_outcome') not in {'completed', 'rejected', 'interrupted', 'cancelled', 'conflict', 'incomplete', 'budget'}:
            raise ValueError('missing invocation terminal outcome')
        if not isinstance(turn['program'], str) or not turn['program']:
            raise ValueError('empty fixture program')
        if 'progress_update' in turn and set(turn['progress_update']) != {'content'}:
            raise ValueError('progress must reuse update_progress content schema')
    if type(expected['status']) is not int or expected['host_stop'] not in STOPS:
        raise ValueError('invalid expected status/host stop')
    if type(expected['complete']) is not bool:
        raise ValueError('completeness must be explicit')
    bytes_value(expected['stdout'])
    bytes_value(expected['stderr'])
    for name, value in expected['files'].items():
        fixture_path(name)
        bytes_value(value)
    for name in expected['absent']:
        fixture_path(name)
    for group in ('receipts', 'effects'):
        if not isinstance(expected[group], list):
            raise ValueError('expected receipt/effect set must be explicit')
        for entry in expected[group]:
            if not entry.get('operation') or not entry.get('outcome'):
                raise ValueError('missing authoritative operation outcome')
    if not case.get('required_evidence') or 'allowed_partial' not in case:
        raise ValueError('missing evidence and partial-outcome contract')
    for generator in setup.get('generators', []):
        fixture_path(generator['directory'])
        if (generator['kind'] != 'numbered-files' or type(generator['count']) is not int
                or not 0 < generator['count'] <= 10001):
            raise ValueError('unbounded fixture generator')


def validate_resources(profiles):
    if not profiles:
        raise ValueError('missing dataset/hardware resource contracts')
    for profile in profiles:
        if not profile.get('dataset') or not profile.get('hardware_class'):
            raise ValueError('resource profile lacks dataset/hardware')
        for name in ('wall_ms', 'peak_rss_bytes', 'spool_bytes', 'cancel_ms'):
            if type(profile[name]) is not int or profile[name] <= 0:
                raise ValueError('resource ceiling must be a positive integer')


def materialize(case, directory):
    """Prepare disk fixtures only; drafts/host injections require real harness APIs later."""
    directory = pathlib.Path(directory)
    if directory.exists():
        raise ValueError('fixture destination must be new')
    directory.mkdir(parents=True)
    files = dict(case['setup']['files'])
    for generator in case['setup'].get('generators', []):
        for index in range(generator['count']):
            files[f"{generator['directory']}/{index:05}.txt"] = generator['body']
    for name, value in files.items():
        path = directory.joinpath(*fixture_path(name).parts)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(bytes_value(value))
    return {name: digest(bytes_value(value)) for name, value in files.items()}
