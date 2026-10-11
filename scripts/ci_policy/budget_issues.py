"""Plan idempotent cleanup-issue transitions from one complete main snapshot."""
import html
import json
import re
from urllib.parse import quote

from .budget_snapshot import CATEGORIES, key, relative
from .budget_review import dismissed, retained, unchanged

START = '<!-- paintedwolf-maintainability:v1 -->'
END = '<!-- /paintedwolf-maintainability -->'
STATE = re.compile(r'<!-- state:(\{[^\n]*\}) -->\n')


def automated(issue):
    return issue.get('user', {}).get('login') == 'github-actions[bot]' and 'pull_request' not in issue


def tracked(issue):
    if not automated(issue):
        return None
    body = issue.get('body') or ''
    if START not in body:
        return None
    if body.count(START) != 1 or body.count(END) != 1 or body.index(START) >= body.index(END):
        raise ValueError(f"issue #{issue['number']} has an invalid tracking block")
    match = STATE.match(body, body.index(START) + len(START) + 1)
    if not match:
        raise ValueError(f"issue #{issue['number']} has no tracking state")
    state = json.loads(match.group(1))
    if (state.get('category') not in CATEGORIES
            or not relative(state.get('id')) or state.get('key') != key(state['category'], state['id'])):
        raise ValueError(f"issue #{issue['number']} has an invalid tracking identity")
    return state


def legacy(issue, artifacts):
    if not automated(issue):
        return None
    for identity, row in artifacts.items():
        if issue['title'] == f"Maintainability debt: {row['category']} {row['id']}":
            return {**row, 'key': identity}
    return None


def block(row, observation, active, previous=None):
    state = {**{k: v for k, v in row.items() if k != 'spans'}, 'sources': row['sources'][:20], **observation, 'key': key(row['category'], row['id']), 'active': active}
    state['measurement_source'] = observation['source_sha'] if active else (previous or {}).get('measurement_source', (previous or {}).get('source_sha', observation['source_sha']))
    if previous is not None and active:
        prior = previous.get('prior_observation') if previous.get('source_sha') == observation['source_sha'] else {
            'measured': previous['measured'], 'source_sha': previous.get('measurement_source', previous['source_sha'])}
        if prior:
            state['prior_observation'] = prior
    encoded = json.dumps(state, sort_keys=True, separators=(',', ':')).replace('<', '\\u003c').replace('>', '\\u003e')
    status = ('Retained by bounded architectural review' if retained(row) else 'Review required') if active else 'Within the cleanup threshold or removed'
    measurement_label = 'Latest measurement' if active else 'Last above-threshold measurement'
    lines = [START, '<!-- state:' + encoded + ' -->',
             f"**{status}**: `{html.escape(row['category'])}` / `{html.escape(row['id'])}`.", '',
             f"{measurement_label}: **{row['measured']}**. Warning threshold: **{row['warn']}**. "
             f"Hard limit: **{row['limit']}**. Effective cap: **{row['effective_cap']}**.", '',
             f"[Main observation]({observation['run_url']}) at `{observation['source_sha']}`."]
    prior = state.get('prior_observation')
    if prior:
        lines += ['', f"Previous observation: **{prior['measured']}** at `{prior['source_sha']}`; delta **{row['measured'] - prior['measured']:+d}**."]
    review = row.get('review')
    if review:
        lines += ['', f"Workstream: `{review['workstream']}`. Classification: `{review['kind']}`.",
                  f"Design decision: **{review['disposition']}**; review state: **{review['state']}**.",
                  html.escape(review['reason']), f"Reviewed source: `{review['reviewed_commit']}`."]
        if review['disposition'] == 'retain':
            lines += [f"Review ceiling: **{review['ceiling']}**. Boundary changes or growth renew review; hard limits remain unchanged."]
    if row['exception_reason']:
        lines += ['', 'Exception: ' + html.escape(row['exception_reason'])]
    links = [f"[{html.escape(path)}](https://github.com/{observation['repository']}/blob/"
             f"{observation['source_sha']}/{quote(path, safe='/')})" for path in row['sources'][:20]]
    lines += ['', 'Sources: ' + ', '.join(links)]
    if len(row['sources']) > 20:
        lines += [f"The report lists {len(row['sources'])} source paths."]
    lines += ['', 'Review cohesion, dependencies, and lifecycle before choosing a refactor or a bounded design decision. '
              'Preserve operation contracts and verify through the repository task runner.', END]
    return '\n'.join(lines)


def body_for(issue, row, observation, active):
    managed = block(row, observation, active, tracked(issue))
    body = issue.get('body') or ''
    if START in body:
        start, end = body.index(START), body.index(END) + len(END)
        return body[:start] + managed + body[end:]
    return body.rstrip() + ('\n\n' if body.strip() else '') + managed


def title(row):
    text = f"Maintainability debt: {row['category']} {row['id']}"
    return text if len(text) <= 240 else text[:237] + '...'


def expected(issue):
    return {name: issue.get(name) for name in ['body', 'state', 'state_reason']}


def plan(issues, artifacts, observation, backfill=False, intake_keys=None):
    if intake_keys is None:
        intake_keys = {identity for identity, row in artifacts.items() if row['touched']}
    indexed = {}
    for issue in issues:
        state = tracked(issue) or legacy(issue, artifacts)
        if state:
            indexed.setdefault(state['key'], []).append((issue, state))
    operations = []
    suppressed = 0
    for identity in sorted(set(indexed) | set(artifacts)):
        matches = sorted(indexed.get(identity, []), key=lambda pair: pair[0]['number'])
        row = artifacts.get(identity)
        if not matches:
            if retained(row):
                suppressed += 1
                continue
            if not backfill and identity not in intake_keys:
                continue
            operations.append({'action': 'create', 'key': identity, 'body': {
                'title': title(row),
                'body': block(row, observation, True)}})
            continue
        issue, previous = matches[0]
        for duplicate, _ in matches[1:]:
            if duplicate['state'] == 'open':
                operations.append({'action': 'deduplicate', 'number': duplicate['number'], 'expected': expected(duplicate), 'body': {
                    'state': 'closed', 'state_reason': 'not_planned',
                    'body': (duplicate.get('body') or '') + f"\n\nTracked by #{issue['number']}."}})
        if dismissed(issue, row, previous, intake_keys, identity):
            suppressed += 1
            continue
        active = row is not None
        desired_state = 'closed' if not active or retained(row) else 'open'
        if (active and issue['state'] == desired_state and START in (issue.get('body') or '')
                and identity not in intake_keys and unchanged(row, previous)):
            continue
        if not active:
            # Already resolved issues retain the observation that resolved them.
            if issue['state'] == 'closed' and not previous.get('active', True):
                continue
            row = {k: previous[k] for k in ['category', 'id', 'measured', 'warn', 'limit',
                                           'effective_cap', 'exception_reason', 'sources']}
        body = body_for(issue, row, observation, active)
        if body == issue.get('body') and desired_state == issue['state']:
            continue
        change = {'body': body}
        action = 'update'
        if desired_state != issue['state']:
            action = 'reopen' if desired_state == 'open' else ('retain' if active else 'resolve')
            change.update(state=desired_state, state_reason='reopened' if desired_state == 'open' else 'completed')
        operations.append({'action': action, 'number': issue['number'], 'expected': expected(issue), 'key': identity, 'body': change})
    if any(len(op['body']['body'].encode()) > 60000 for op in operations):
        raise ValueError('planned issue body exceeds the size bound')
    return operations, suppressed
