"""Select and verify non-publishing native Linux package candidates."""

from __future__ import annotations

import argparse
import io
import json
import lzma
import os
from pathlib import Path
import re
import subprocess
import sys
from urllib.parse import urlencode
import zipfile
import zlib

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from release_distribution_contract.platforms import parse_platforms

REQUEST_PATHS = (
    '.github/workflows/linux-candidates.yml', '.github/workflows/linux-candidate-requests.yml',
    '.github/workflows/release.yml', '.github/actions/setup-*/**', 'packaging/**',
    'scripts/*linux*', 'scripts/linux_release/**', 'scripts/release_distribution_contract/**',
    'scripts/*engine*', 'scripts/*bundle*', 'scripts/release-stage-platform-artifacts.sh',
    'scripts/release-metadata.py', 'scripts/release_semver.py', 'scripts/update_keys.py', 'scripts/sha256.sh',
    'scripts/*opengrep*', 'VERSION', 'RELEASE_BUILD', 'Taskfile.yml', 'task', 'lycaon/go.mod', 'lycaon/go.sum',
    'lycaon-den/package.json', 'lycaon-den/bun.lock', 'lycaon-den/src-tauri/Cargo.*',
    'lycaon-den/src-tauri/tauri*.json', 'rust-toolchain.toml', '.bun-version', '.node-version',
)
SOURCE_FIELDS = {'version', 'repository', 'run_id', 'run_attempt', 'controller_sha', 'source_sha', 'source_tree'}
SOURCE_LIMIT = 8192


def git(*arguments):
    return subprocess.check_output(['git', *arguments], text=True).strip()


def source_record(environment):
    return dict(version=1, repository=environment['GITHUB_REPOSITORY'],
                run_id=int(environment['GITHUB_RUN_ID']), run_attempt=int(environment['GITHUB_RUN_ATTEMPT']),
                controller_sha=environment['GITHUB_WORKFLOW_SHA'], source_sha=git('rev-parse', 'HEAD'),
                source_tree=git('rev-parse', 'HEAD^{tree}'))


def read_source(raw, run, repository):
    if len(raw) > SOURCE_LIMIT:
        return None
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        entries = archive.infolist()
        if len(entries) != 1 or entries[0].filename != 'candidate-source.json' or entries[0].file_size > SOURCE_LIMIT:
            return None
        record = json.loads(archive.read(entries[0]))
    references = [row['sha'] for row in run.get('referenced_workflows', [])
                  if row['path'].partition('@')[0] == repository + '/.github/workflows/linux-candidates.yml']
    if (not isinstance(record, dict) or set(record) != SOURCE_FIELDS or type(record['version']) is not int
            or record['version'] != 1 or record['repository'] != repository
            or type(record['run_id']) is not int or record['run_id'] != run['id']
            or type(record['run_attempt']) is not int or record['run_attempt'] != run['run_attempt']
            or any(not isinstance(record[key], str) or not re.fullmatch('[a-f0-9]{40}', record[key])
                   for key in ('controller_sha', 'source_sha', 'source_tree'))
            or references != [record['controller_sha']] or record['source_sha'] != record['controller_sha']):
        return None
    return record


def same_inputs(record, current):
    source = record['source_sha']
    if subprocess.run(['git', 'cat-file', '-e', source + '^{commit}'], capture_output=True).returncode:
        subprocess.run(['git', 'fetch', '--no-tags', 'origin', source], check=True, capture_output=True, timeout=30)
    if git('rev-parse', source + '^{tree}') != record['source_tree']:
        return False
    return subprocess.run(['git', 'diff', '--quiet', source, current, '--',
                           *[':(glob)' + path for path in REQUEST_PATHS]], capture_output=True).returncode == 0


def request_build(environment, event):
    if environment['GITHUB_EVENT_NAME'] != 'pull_request' or event.get('action') != 'enqueued':
        return True
    from ci_policy.github import api
    try:
        repository = environment['GITHUB_REPOSITORY']
        pull = event['pull_request']
        query = urlencode(dict(event='pull_request', branch=pull['head']['ref'], status='success', per_page=20))
        runs = api(f'repos/{repository}/actions/workflows/linux-candidate-requests.yml/runs?{query}')['workflow_runs']
        candidates = [run for run in runs if run['id'] < int(environment['GITHUB_RUN_ID'])
                      and run.get('event') == 'pull_request' and run.get('status') == 'completed'
                      and run.get('conclusion') == 'success' and run.get('path') == '.github/workflows/linux-candidate-requests.yml'
                      and run.get('repository', {}).get('full_name') == repository
                      and any(row.get('number') == pull['number'] for row in run.get('pull_requests', []))]
        if not candidates:
            return True
        for run in sorted(candidates, key=lambda row: row['id'], reverse=True):
            artifacts = api(f"repos/{repository}/actions/runs/{run['id']}/artifacts?per_page=100")['artifacts']
            matching = [row for row in artifacts if row.get('name') == f"linux-candidate-source-{run['run_attempt']}"
                        and row.get('expired') is False and 0 < row.get('size_in_bytes', 0) <= SOURCE_LIMIT]
            if not matching:
                continue
            if len(matching) != 1:
                return True
            raw = subprocess.check_output(['gh', 'api', f"repos/{repository}/actions/artifacts/{matching[0]['id']}/zip"], timeout=30)
            record = read_source(raw, run, repository)
            current = git('rev-parse', 'HEAD')
            return not (current == environment['GITHUB_SHA'] and record and same_inputs(record, current))
        return True
    except (KeyError, TypeError, AttributeError, ValueError, OSError, RuntimeError,
            subprocess.SubprocessError, zipfile.BadZipFile, zlib.error, lzma.LZMAError):
        return True


def matrix(catalog: dict) -> dict:
    platforms = parse_platforms(json.dumps(catalog).encode())
    rows = [row for row in platforms.values() if row.get("goos") == "linux"]
    if {row.get("goarch") for row in rows} != {"amd64", "arm64"} or len(rows) != 2:
        raise ValueError("Linux candidates require exactly one native row for each architecture")
    for row in rows:
        verify_row(row, row["updater_key"], row["runner"], row["goarch"])
    return {"include": rows}


def verify(catalog: dict, key: str, runner: str, architecture: str) -> dict:
    platforms = parse_platforms(json.dumps(catalog).encode())
    if key not in platforms:
        raise ValueError(f"candidate {key!r} must resolve to exactly one catalog row")
    return verify_row(platforms[key], key, runner, architecture)


def verify_row(row: dict, key: str, runner: str, architecture: str) -> dict:
    expected = {"amd64": "x86_64", "arm64": "aarch64"}
    if (architecture not in expected or row.get("goos") != "linux"
            or row.get("goarch") != architecture or row.get("artifact_arch") != expected[architecture]
            or row.get("runner") != runner or not isinstance(runner, str) or not runner
            or row.get("package_extensions") not in (["deb", "rpm"], ["deb", "rpm", "pkg.tar.zst"])
            or row.get("updater_extension") is not None):
        raise ValueError(f"candidate {key!r} disagrees with its native package contract")
    return row


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--catalog", type=Path, default=Path("packaging/release-platforms.json"))
    parser.add_argument("--platform")
    parser.add_argument("--runner-label")
    parser.add_argument("--architecture")
    parser.add_argument('--request', action='store_true')
    parser.add_argument('--source-record', type=Path)
    args = parser.parse_args()
    try:
        if args.source_record:
            args.source_record.write_text(json.dumps(source_record(os.environ), separators=(',', ':')) + '\n')
            return 0
        if args.request:
            event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
            build = request_build(os.environ, event)
            with open(os.environ['GITHUB_OUTPUT'], 'a') as output:
                output.write(f"build={str(build).lower()}\nsource={git('rev-parse', 'HEAD')}\n")
            return 0
        catalog = json.loads(args.catalog.read_text(encoding="utf-8"))
        result = (verify(catalog, args.platform, args.runner_label, args.architecture)
                  if args.platform else matrix(catalog))
    except (OSError, ValueError, KeyError, TypeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
