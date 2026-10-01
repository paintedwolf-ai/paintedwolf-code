"""Grade custom workflows from committed application receipts."""
from episode_evidence import read, member


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
    matches = {r['id']: [i for i, actual in enumerate(receipts) if receipt_matches(actual, r)] for r in expected}
    checks.append({'id': 'workflow-receipt-set', 'passed': len(receipts) == len(expected)
                   and all(len(found) == 1 for found in matches.values())
                   and all(sum(receipt_matches(actual, r) for r in expected) == 1 for actual in receipts)})
    revisions = [r['source_revision'] for r in receipts]
    ordered = (all(type(revision) is int and revision > 0 for revision in revisions)
               and len(set(revisions)) == len(revisions)
               and all(len(matches[r['id']]) == 1 and len(matches[parent]) == 1
                       and receipts[matches[parent][0]]['source_revision'] < receipts[matches[r['id']][0]]['source_revision']
                       for r in expected for parent in r['after']))
    checks.append({'id': 'workflow-receipt-order', 'passed': ordered})
    for required in expected:
        found = matches[required['id']]
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
