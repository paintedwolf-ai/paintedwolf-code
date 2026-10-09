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
