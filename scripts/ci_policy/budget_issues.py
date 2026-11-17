"""Read artifact-scoped cleanup issues that workstream issues supersede."""
import json
import re

from .budget_snapshot import CATEGORIES, key, relative

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


def expected(issue):
    return {name: issue.get(name) for name in ['title', 'body', 'state', 'state_reason']}
