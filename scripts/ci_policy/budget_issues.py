"""Plan idempotent cleanup-issue transitions from one complete main snapshot."""
import html
import json
import re
from urllib.parse import quote

from .budget_snapshot import CATEGORIES, key, relative
from .budget_review import retained

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


def expected(issue):
    return {name: issue.get(name) for name in ['title', 'body', 'state', 'state_reason']}
