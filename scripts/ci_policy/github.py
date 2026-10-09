"""Small paginated GitHub client with structured request bodies."""
import json
import os
import subprocess


def api(path, method='GET', body=None):
    command = ['gh', 'api', '--method', method, path]
    if body is not None:
        command += ['--input', '-']
    result = subprocess.run(command, input=json.dumps(body) if body is not None else None,
                            capture_output=True, text=True, check=True)
    return json.loads(result.stdout) if result.stdout.strip() else None


def pages(path, key=None):
    separator = '&' if '?' in path else '?'
    page = 1
    while True:
        data = api(f'{path}{separator}per_page=100&page={page}')
        rows = data[key] if key else data
        yield from rows
        if len(rows) < 100:
            break
        page += 1


def repository():
    return 'repos/' + os.environ['GITHUB_REPOSITORY']


def ensure_issue(title, body):
    root = repository()
    # Exact machine-generated titles deduplicate without relying on search-index freshness.
    for issue in pages(f'{root}/issues?state=open'):
        if 'pull_request' not in issue and issue['title'] == title:
            return issue
    return api(f'{root}/issues', 'POST', {'title': title, 'body': body})
