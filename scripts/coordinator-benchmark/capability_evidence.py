"""Capability-request discipline as a causal chain: declaration, grant, execution, artifact."""
import shlex
import ledger
from timestamps import instant

RETRY_SUBJECTS = {'SANDBOX_TRY_LOOPBACK_CONNECT': 'loopback_connect'}


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
