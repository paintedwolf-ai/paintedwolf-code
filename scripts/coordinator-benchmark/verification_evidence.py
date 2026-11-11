"""Match host-recorded suite, workflow, and capability execution to delivered source."""
import argparse
import fnmatch
import pathlib
import posixpath
import shlex
import ledger
from ledger import instant
from episode_evidence import read, member

RETRY_SUBJECTS = {'SANDBOX_TRY_LOOPBACK_CONNECT': 'loopback_connect'}


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


def citation_matches(actual, required):
    return (actual.get('verdict') in {'matched', 'traced'}
            and actual.get('path') == required['path']
            and (not actual.get('line') or 'line' not in required or actual['line'] == required['line']))


def receipt_matches(actual, required):
    verdict = actual['submission']['verdict']
    return actual['phase'] == required['phase'] and all(
        verdict.get(key) == value for key, value in required['identity'].items())


def validate_receipts(contract):
    receipts = contract['receipts']
    by_id = {r['id']: r for r in receipts}
    if not receipts or len(by_id) != len(receipts):
        raise ValueError('workflow receipt identities must be unique')
    for r in receipts:
        if not isinstance(r['identity'], dict) or any(r['verdict'].get(k) != v for k, v in r['identity'].items()):
            raise ValueError('workflow receipt identity must select its declared verdict')
        if any(parent not in by_id for parent in r['after']):
            raise ValueError('workflow ordering names an absent receipt')
        if any(other is not r and other['phase'] == r['phase'] and not any(
                key in other['identity'] and other['identity'][key] != value
                for key, value in r['identity'].items()) for other in receipts):
            raise ValueError('workflow receipt selectors overlap')
    pending = set(by_id)
    while pending:
        ready = {name for name in pending if not (set(by_id[name]['after']) & pending)}
        if not ready:
            raise ValueError('workflow receipt ordering contains a cycle')
        pending -= ready


def workflow_checks(capture, case, contract):
    facts = read(capture, case['session_id'])
    session = member(facts, case['session_id'])
    runs = [run for run in session['workflows'] if run['workflow_id'] == contract['id']]
    valid = (len(runs) == 1 and runs[0]['id'] == case.get('workflow_run_id') and runs[0]['workflow_version'] == contract['version']
             and runs[0]['status'] == 'complete' and runs[0]['current_phase'] == contract['terminal_phase'])
    checks = [{'id': 'workflow-completed', 'passed': valid},
              {'id': 'workflow-delivery-paths', 'passed': set(facts['delivered_paths']) == set(contract['source_paths'])}]
    run_id = runs[0]['id'] if len(runs) == 1 else None
    receipts = [r for r in session['verdicts'] if r['run_id'] == run_id and r['status'] == 'committed'
                and r['outcome']['Applied'] and r['outcome']['Valid']]
    expected = contract['receipts']
    found_matches = {r['id']: [i for i, actual in enumerate(receipts) if receipt_matches(actual, r)] for r in expected}
    checks.append({'id': 'workflow-receipt-set', 'passed': len(receipts) == len(expected)
                   and all(len(found) == 1 for found in found_matches.values())
                   and all(sum(receipt_matches(actual, r) for r in expected) == 1 for actual in receipts)})
    revisions = [r['source_revision'] for r in receipts]
    ordered = (all(type(revision) is int and revision > 0 for revision in revisions)
               and len(set(revisions)) == len(revisions)
               and all(len(found_matches[r['id']]) == 1 and len(found_matches[parent]) == 1
                       and receipts[found_matches[parent][0]]['source_revision'] < receipts[found_matches[r['id']][0]]['source_revision']
                       for r in expected for parent in r['after']))
    checks.append({'id': 'workflow-receipt-order', 'passed': ordered})
    for required in expected:
        found = found_matches[required['id']]
        receipt = receipts[found[0]] if len(found) == 1 else None
        terminal = required.get('terminal', True)
        value_ok = bool(receipt and receipt['phase'] == required['phase']
                        and receipt['submission']['session_id'] == case['session_id']
                        and receipt['submission']['verdict'] == required['verdict']
                        and receipt['outcome']['Terminal'] == terminal)
        audit = (receipt['outcome'].get('Grounding') or {}) if receipt else {}
        citations_ok = not required['citations'] or (audit.get('traced') is True and not audit.get('host_assembled', False)
            and all(any(citation_matches(cited, wanted) for cited in audit.get('cited_evidence', []))
                    for wanted in required['citations']))
        checks.append({'id': 'workflow-receipt-'+required['id'], 'passed': value_ok and citations_ok})
    return checks


def runs_program(result, program, arguments=()):
    """The fixture requests a standard Python script invocation, not an arbitrary shell program."""
    try:
        argv = shlex.split((result.get('args') or {}).get('command', ''))
    except ValueError:
        return False
    if not argv or argv[0] not in {'python', 'python3'}:
        return False
    if argv[1:2] == ['-B']:
        argv.pop(1)
    return (argv[1:] == [program, *arguments] and result['tool'] in {'command', 'verify'} and result['invoked'] is True
            and (result.get('args') or {}).get('cwd', '') in {'', '.'})


def capability_checks(capture, case, expected):
    db = ledger.read(capture, case['session_id'])
    scope = ledger.session_ids(db)
    cards = ledger.checkpoints(db, scope)
    results = ledger.tool_results(db, scope)
    receipts = {r['tool_call_id']: r for r in ledger.invocations(db, case['session_id'])}
    writes = {}
    for contract in (expected.get('executed_with'), expected.get('artifact_from_service')):
        if contract:
            for path in [contract['program'], *([contract['service_program']] if 'service_program' in contract else [])]:
                writes[path] = ledger.file_writes(db, case['session_id'], path)
    checks = []
    own = [r for r in results if r['session_id'] == case['session_id']]
    if expected.get('executed_with'):
        contract = expected['executed_with']
        wanted = set(contract['capabilities'])
        arguments = [a.replace('${sandbox_path}', (case.get('sandbox') or {}).get('path', '')) for a in contract.get('arguments', [])]
        declared = {r['tool_call_id'] for r in own if runs_program(r, contract['program'], arguments) and r['outcome'] == 'completed'
                    and wanted <= set(((r['args'] or {}).get('capability_request') or {}).keys())}
        executed = {call for call in declared if receipts.get(call, {}).get('status') == 'completed'
                    and receipts[call].get('invoked') == 1 and receipts[call].get('source_verdict') == 'passed'}
        checks.append({'id': 'supplied-program-executed-with-capabilities', 'passed': bool(executed)})
        checks.append({'id': 'service-program-unchanged', 'passed': not writes[contract['program']]})
    if expected.get('artifact_from_service'):
        contract = expected['artifact_from_service']
        clients = [r for r in own if runs_program(r, contract['program'])]
        successful = [r for r in clients if receipts.get(r['tool_call_id'], {}).get('status') == 'completed'
                      and r['outcome'] == 'completed' and receipts[r['tool_call_id']].get('invoked') == 1
                      and receipts[r['tool_call_id']].get('source_verdict') == 'passed']
        serving = [r for r in own if runs_program(r, contract['service_program'])
                   and r['outcome'] == 'completed'
                   and receipts.get(r['tool_call_id'], {}).get('status') == 'completed'
                   and receipts[r['tool_call_id']].get('invoked') == 1]
        ordered = any(instant(receipts[server['tool_call_id']]['started_at'])
                      < instant(receipts[client['tool_call_id']]['started_at'])
                      for server in serving for client in successful)
        checks.append({'id': 'supplied-service-started-before-client', 'passed': ordered})
        checks.append({'id': 'service-programs-unchanged', 'passed': not writes[contract['program']] and not writes[contract['service_program']]})
        checks.append({'id': 'supplied-client-succeeded', 'passed': bool(successful)})
    if expected.get('cards_at_most_once'):
        counts = {kind: sum(card['subject'] == kind for card in cards) for kind in expected['cards_at_most_once']}
        checks.append({'id': 'one-card-per-capability', 'passed': all(count <= 1 for count in counts.values())})
    if expected.get('granted'):
        granted = {card['subject'] for card in cards if card['status'] == 'approved'}
        checks.append({'id': 'capability-granted', 'passed': set(expected['granted']) <= granted})
    if expected.get('denied'):
        denied = {card['subject'] for card in cards if card['status'] == 'rejected'}
        checks.append({'id': 'capability-denied', 'passed': set(expected['denied']) <= denied})
    if expected.get('no_retry_codes_after_grant'):
        valid = True
        for code in expected['no_retry_codes_after_grant']:
            approved = [card for card in cards if card['status'] == 'approved' and card['subject'] == RETRY_SUBJECTS[code]]
            granted_calls = {card['tool_call_id'] for card in approved}
            boundary = min((r['ord'] for r in own if r['tool_call_id'] in granted_calls), default=None)
            stale = any(boundary is not None and r['ord'] > boundary and code in r['codes'] for r in own)
            valid = valid and boundary is not None and not stale
        checks.append({'id': 'no-boundary-retry-after-grant', 'passed': valid})
    return checks
