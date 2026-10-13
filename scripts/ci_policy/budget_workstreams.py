"""Bounded cleanup workstreams and evidence-preserving migration from artifact issues."""
import base64
import hashlib
import html
import json
import re
import zlib
from urllib.parse import quote

from .budget_issues import END, START as LEGACY_START, expected, legacy, tracked
from .budget_review import retained, unchanged
from .budget_snapshot import key, relative

START = '<!-- paintedwolf-maintainability:v2 -->'
SUPERSEDED = '<!-- paintedwolf-maintainability-superseded:'


def module(row):
    """Use the source module boundary, independent of metrics and nested packages."""
    parts = row['id'].split('/')
    for marker in ('internal', 'src', 'test'):
        if marker in parts:
            index = parts.index(marker)
            # Test contracts form one review area; production modules own their tests.
            end = index + 1 if marker == 'test' else index + 2
            if end <= len(parts) and '.' not in parts[end - 1]:
                return '/'.join(parts[:end])
            return '/'.join(parts[:index + 1])
    return parts[0] if len(parts) > 1 else 'repository'


def workstream(row):
    parts = row['id'].split('/')
    for marker in ('internal', 'src', 'test'):
        if marker in parts:
            return '/'.join(parts[:parts.index(marker) + 1])
    return parts[0] if len(parts) > 1 else 'repository'


def group_state(issue):
    if issue.get('user', {}).get('login') != 'github-actions[bot]' or 'pull_request' in issue:
        return None
    body = issue.get('body') or ''
    if START not in body:
        return None
    if body.count(START) != 1 or body.count(END) != 1 or body.index(START) >= body.index(END):
        raise ValueError('invalid workstream tracking block')
    match = re.match(r'\n<!-- state:(\{[^\n]*\}) -->\n', body[body.index(START) + len(START):])
    state = json.loads(match[1]) if match else {}
    if not relative(state.get('workstream')) or not isinstance(state.get('inventory'), str):
        raise ValueError('invalid workstream tracking state')
    try:
        decoder = zlib.decompressobj()
        raw = decoder.decompress(base64.b64decode(state['inventory'], validate=True), 8 * 1024 * 1024)
        if not decoder.eof or decoder.unused_data or decoder.unconsumed_tail:
            raise ValueError('workstream inventory exceeds the bound or has trailing data')
        state['artifacts'] = json.loads(raw)
    except (ValueError, zlib.error) as error:
        raise ValueError('invalid workstream inventory') from error
    if not isinstance(state['artifacts'], list):
        raise ValueError('invalid workstream artifacts')
    seen = set()
    for row in state['artifacts']:
        # Reuse the validated v1 artifact identity grammar inside the new envelope.
        encoded = json.dumps(dict(row, key=key(row['category'], row['id'])), separators=(',', ':')).replace('<', '\\u003c')
        parsed = tracked({**issue, 'body': LEGACY_START + '\n<!-- state:' + encoded + ' -->\n' + END})
        if (set(row) != {'category', 'id', 'evidence_digest'}
                or not re.fullmatch('[0-9a-f]{64}', row.get('evidence_digest', ''))
                or parsed['key'] in seen or workstream(row) != state['workstream']):
            raise ValueError('duplicate or misplaced workstream artifact')
        seen.add(parsed['key'])
    return state


def evidence(row):
    full = {k: v for k, v in row.items() if k not in {'spans', 'touched'}}
    return {'category': row['category'], 'id': row['id'],
            'evidence_digest': hashlib.sha256(json.dumps(full, sort_keys=True, separators=(',', ':')).encode()).hexdigest()}


def block(identity, rows, observation):
    inventory = json.dumps([evidence(row) for row in rows], sort_keys=True, separators=(',', ':')).encode()
    state = {'workstream': identity, 'inventory': base64.b64encode(zlib.compress(inventory)).decode(), **observation}
    encoded = json.dumps(state, sort_keys=True, separators=(',', ':')).replace('<', '\\u003c').replace('>', '\\u003e')
    modules = {}
    for row in rows:
        modules.setdefault(module(row), []).append(row)
    lines = [START, '<!-- state:' + encoded + ' -->',
             f"**{html.escape(identity)}**: {len(rows)} findings across {len(modules)} modules.", '',
             'Review one module at a time. Related metrics are evidence for one design decision, '
             'not separate refactor tasks. Preserve operation contracts; choose refactoring, consolidation, or bounded retention.', '',
             '| Module | Findings | Hard-limit breaches | Renewed reviews |',
             '| --- | ---: | ---: | ---: |']
    for name, findings in sorted(modules.items()):
        hard = sum(r['measured'] > r['limit'] for r in findings)
        renewed = sum(r.get('review', {}).get('state') in {'boundary_changed', 'shape_changed', 'ceiling_exceeded'} for r in findings)
        lines.append(f"| `{html.escape(name)}` | {len(findings)} | {hard} | {renewed} |")
    for name, findings in sorted(modules.items()):
        detail_start = len(lines)
        lines += ['', '<details>', f'<summary>{html.escape(name)} — {len(findings)} findings</summary>', '',
                  '| Artifact | Metric | Measured | Warn | Hard limit | Effective cap | Review |',
                  '| --- | --- | ---: | ---: | ---: | ---: | --- |']
        for row in findings:
            url = f"https://github.com/{observation['repository']}/blob/{observation['source_sha']}/{quote(row['sources'][0], safe='/')}"
            label = html.escape(row['id'][len(name) + 1:]).replace('|', '&#124;')
            status = row.get('review', {}).get('state', 'required')
            lines.append(f"| [{label}]({url}) | {row['category']} | {row['measured']} | {row['warn']} | {row['limit']} | {row['effective_cap']} | {status} |")
            review = row.get('review')
            if review:
                lines.append(f"\nReview `{html.escape(row['id'])}`: {html.escape(review['reason'])} (workstream `{review['workstream']}`).\n")
            if row.get('exception_reason'):
                lines.append('\nException: ' + html.escape(row['exception_reason']) + '\n')
        lines += ['', '</details>']
        if len('\n'.join(lines).encode()) > 55000:
            del lines[detail_start:]
            lines += ['', f'Details for `{html.escape(name)}` are in the linked complete qualification report.']
    lines += ['', f"[Measured main snapshot and complete report]({observation['run_url']}) at `{observation['source_sha']}`.", '',
              'The qualification report retains full source membership, exceptions, and review evidence. '
              'Tracking fingerprints cover all those facts. This issue closes only when all admitted findings '
              'are removed, below threshold, or retained by valid bounded review.', END]
    return '\n'.join(lines)


def body_for(issue, managed):
    body = issue.get('body') or ''
    marker = START if START in body else LEGACY_START
    if marker in body:
        return body[:body.index(marker)] + managed + body[body.index(END) + len(END):]
    return body.rstrip() + ('\n\n' if body.strip() else '') + managed


def plan(issues, artifacts, observation, backfill=False, intake_keys=None):
    intake_keys = intake_keys if intake_keys is not None else {k for k, r in artifacts.items() if r['touched']}
    groups, old, previous = {}, {}, {}
    for issue in issues:
        state = group_state(issue)
        if state:
            groups.setdefault(state['workstream'], []).append(issue)
            for row in state['artifacts']:
                previous[key(row['category'], row['id'])] = (issue, row)
        elif SUPERSEDED not in (issue.get('body') or ''):
            row = tracked(issue) or legacy(issue, artifacts)
            if row:
                old.setdefault(workstream(row), []).append(issue)
                previous.setdefault(row['key'], (issue, row))
    active, suppressed = {}, 0
    for identity, row in sorted(artifacts.items()):
        prior = previous.get(identity)
        if retained(row) or (prior and prior[0]['state'] == 'closed' and prior[0].get('state_reason') == 'not_planned'
                            and identity not in intake_keys and not row.get('review')
                            and (evidence(row) == prior[1] if 'evidence_digest' in prior[1]
                                 else unchanged(row, prior[1]))):
            suppressed += 1
            continue
        renewed = row.get('review', {}).get('state') in {'boundary_changed', 'shape_changed', 'ceiling_exceeded'}
        if not prior and not backfill and identity not in intake_keys and not renewed:
            continue
        active.setdefault(workstream(row), []).append(row)
    operations = []
    for identity in sorted(set(groups) | set(old) | set(active)):
        # Existing v2 issues win, then the oldest artifact issue is adopted in place.
        candidates = sorted(groups.get(identity, []), key=lambda i: i['number'])
        ancestors = sorted(old.get(identity, []), key=lambda i: i['number'])
        canonical = (candidates or ancestors or [None])[0]
        rows = active.get(identity, [])
        if canonical is None:
            if rows:
                operations.append({'action': 'create', 'key': identity, 'body': {
                    'title': 'Maintainability review: ' + identity, 'body': block(identity, rows, observation)}})
            continue
        state = group_state(canonical)
        desired = 'open' if rows else 'closed'
        same = state and [evidence(r) for r in rows] == [
            r for r in state['artifacts']]
        if not rows and not candidates and canonical['state'] == 'closed':
            continue
        if not same or canonical['state'] != desired:
            change = {'body': body_for(canonical, block(identity, rows, observation))}
            if not state:
                change['title'] = 'Maintainability review: ' + identity
            action = 'update'
            if canonical['state'] != desired:
                action = 'reopen' if rows else 'resolve'
                change.update(state=desired, state_reason='reopened' if rows else 'completed')
            operations.append({'action': action, 'key': identity, 'number': canonical['number'], 'expected': expected(canonical), 'body': change})
        for duplicate in candidates + ancestors:
            if duplicate['number'] == canonical['number'] or duplicate['state'] != 'open':
                continue
            operations.append({'action': 'consolidate', 'key': identity, 'number': duplicate['number'], 'expected': expected(duplicate), 'body': {
                'state': 'closed', 'state_reason': 'not_planned', 'body': (duplicate.get('body') or '') +
                f"\n\nConsolidated into #{canonical['number']}; the debt remains tracked there.\n{SUPERSEDED}{identity} -->"}})
    if any(len(op['body']['body'].encode()) > 60000 for op in operations):
        raise ValueError('workstream summary exceeds the issue size bound')
    return operations, suppressed
