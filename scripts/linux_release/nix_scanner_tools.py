"""Resolve the exact native scanner build tool reachable from the selected application."""
import json
from pathlib import Path
import platform
import re
import subprocess

PNAME = 'painted-wolf-code-scanner-build-tools'
STORE = r'/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._?=-]+'
SYSTEMS = {'x86_64': 'x86_64-linux', 'aarch64': 'aarch64-linux'}


def scanner_derivation(graph: dict, root: str, system: str) -> dict:
    if (system not in SYSTEMS.values() or not isinstance(graph, dict)
            or set(graph) != {'version', 'derivations'} or graph['version'] != 4
            or not isinstance(graph['derivations'], dict)
            or not 0 < len(graph['derivations']) <= 100000):
        raise ValueError('scanner selection requires the complete schema-4 native derivation graph')
    rows = graph['derivations']
    root = Path(root).name
    if root not in rows or rows[root].get('system') != system:
        raise ValueError('application derivation is not for the selected native system')
    pending, visited, matches = [root], set(), []
    while pending:
        path = pending.pop()
        if path in visited:
            continue
        visited.add(path)
        if not isinstance(path, str) or re.fullmatch(STORE.removeprefix('/nix/store/') + r'\.drv', path) is None:
            raise ValueError('derivation graph contains an invalid store identity: ' + repr(path)[:256])
        row = rows.get(path)
        if not isinstance(row, dict) or row.get('version') != 4 or not isinstance(row.get('inputs', {}).get('drvs'), dict):
            raise ValueError('derivation graph is incomplete')
        pending.extend(row['inputs']['drvs'])
        environment = row.get('env')
        if not isinstance(environment, dict):
            raise ValueError('derivation graph omitted its typed environment')
        if environment.get('pname') != PNAME:
            continue
        output = row.get('outputs', {}).get('out', {})
        value = output.get('path')
        if isinstance(value, str):
            value = '/nix/store/' + value
        if (row.get('system') != system or not isinstance(value, str)
                or re.fullmatch(STORE, value) is None
                or not Path(value).name[33:].startswith(PNAME + '-')
                or environment.get('out') != value):
            raise ValueError('scanner tools do not have one fixed native output identity')
        matches.append({'derivation': '/nix/store/' + path, 'output': value,
                        'executable': value + '/bin/opengrep-artifact', 'system': system})
    if len(matches) != 1:
        raise ValueError('selected application must depend on exactly one native scanner build tool')
    return matches[0]



def command(nix, source, environment):
    if platform.system() != 'Linux' or platform.machine() not in SYSTEMS:
        raise ValueError('scanner preparation requires a supported native Linux host')
    system = SYSTEMS[platform.machine()]
    selected = source + '#packages.' + system + '.default'
    result = subprocess.run(nix + ['eval', '--raw', '--no-write-lock-file', selected + '.drvPath'],
                            env=environment, text=True, stdout=subprocess.PIPE, check=True)
    root = result.stdout.strip()
    result = subprocess.run(nix + ['derivation', 'show', '--recursive', root],
                            env=environment, text=True, stdout=subprocess.PIPE, check=True)
    metadata = scanner_derivation(json.loads(result.stdout), root, system)
    invocation = nix + ['build', '--json', '--no-link', '--no-write-lock-file',
                        '--option', 'sandbox', 'true', '--max-jobs', '1', '--cores', '1',
                        metadata['derivation'] + '^out']
    return invocation, metadata
