"""Match host-recorded suite execution to the delivered source, across delegations."""
import argparse
import fnmatch
import pathlib
import posixpath
import shlex
from episode_evidence import read


class DiscoveryParser(argparse.ArgumentParser):
    def error(self, message):
        raise ValueError(message)


def full_discovery(arguments, files, root):
    parser = DiscoveryParser(add_help=False, allow_abbrev=False)
    parser.add_argument('-s', '--start-directory', dest='start')
    parser.add_argument('-p', '--pattern', dest='pattern')
    parser.add_argument('-t', '--top-level-directory', dest='top')
    for field in ['start', 'pattern', 'top']:
        parser.add_argument(field, nargs='?', default=argparse.SUPPRESS)
    try:
        selected = parser.parse_args(arguments, argparse.Namespace(start='.', pattern='test*.py', top=None))
    except ValueError:
        return False
    allowed_roots = {'.', posixpath.normpath(root)}
    if any(posixpath.normpath(path) not in allowed_roots for path in [selected.start, selected.top or selected.start]):
        return False
    tests = {pathlib.PurePosixPath(name).name for name in files
             if fnmatch.fnmatchcase(pathlib.PurePosixPath(name).name, 'test*.py')}
    return all(fnmatch.fnmatchcase(name, selected.pattern) for name in tests)


def suite_command(command, files, root='.'):
    """Recognize full discovery or explicit selection of every delivered test module."""
    try:
        argv = shlex.split(command)
    except ValueError:
        return False
    if not argv or argv[0] not in {'python', 'python3'}:
        return False
    if argv[1:2] == ['-B']:
        argv.pop(1)
    if argv[1:3] != ['-m', 'unittest']:
        return False
    arguments = [arg for arg in argv[3:] if arg not in {'-v', '--verbose', '-q', '--quiet'}]
    if not arguments:
        return True
    if arguments[0] == 'discover':
        return full_discovery(arguments[1:], files, root)
    tests = {name for name in files if fnmatch.fnmatchcase(pathlib.PurePosixPath(name).name, 'test*.py')}
    selected = set()
    for argument in arguments:
        name = argument if argument.endswith('.py') else argument.replace('.', '/') + '.py'
        if name not in tests:
            return False
        selected.add(name)
    return bool(tests) and selected == tests


def verification_checks(capture, case, source):
    facts=read(capture,case['session_id'])
    lineage={s['id'] for s in facts['sessions']}
    current=any(matches(facts,record,case['session_id'],lineage) for record in facts['verification'] or [])
    return [{'id':'delivered-source-verification','passed':current}]


def matches(facts, record, session_id, lineage):
    if record['type']!='verify' or record['slot']!='tests' or record['run_id']!=session_id or record['verdict']!='passed':
        return False
    proof=record.get('artifacts') or {}
    if (proof.get('session_id') not in lineage or proof.get('is_check') is not True or proof.get('exit_code')!=0
        or proof.get('producer') not in {'verify','command'} or not proof.get('check_id')):
        return False
    snapshot=facts['snapshots'].get(proof.get('source_revision'))
    if not snapshot or not snapshot['matches_delivered'] or snapshot['roots_key']!=proof.get('source_root_digest'):
        return False
    root=snapshot['roots'][0]
    return proof.get('cwd') in {'.',root} and suite_command(proof.get('command',''),facts['delivered_paths'],root)
