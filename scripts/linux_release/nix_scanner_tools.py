"""Resolve the exact native scanner build tool reachable from the selected application."""
import json
from pathlib import Path
import platform
import re
import subprocess

PNAME = 'painted-wolf-code-scanner-build-tools'
STORE = r'/nix/store/[0-9abcdfghijklmnpqrsvwxyz]{32}-[A-Za-z0-9+._-]+'
SYSTEMS = {'x86_64': 'x86_64-linux', 'aarch64': 'aarch64-linux'}


def scanner_derivation(graph: dict, root: str, system: str) -> dict:
    if (system not in SYSTEMS.values() or not isinstance(graph, dict)
            or not 0 < len(graph) <= 100000 or root not in graph):
        raise ValueError('scanner selection requires the complete selected native derivation graph')
    if not isinstance(graph[root], dict) or graph[root].get('system') != system:
        raise ValueError('application derivation is not for the selected native system')
    pending, visited, matches = [root], set(), []
    while pending:
        path = pending.pop()
        if path in visited:
            continue
        visited.add(path)
        if not isinstance(path, str) or re.fullmatch(STORE + r'\.drv', path) is None:
            raise ValueError('derivation graph contains an invalid store identity')
        row = graph.get(path)
        if not isinstance(row, dict) or not isinstance(row.get('inputDrvs'), dict):
            raise ValueError('derivation graph is incomplete')
        pending.extend(row['inputDrvs'])
        environment = row.get('env')
        if not isinstance(environment, dict):
            raise ValueError('derivation graph omitted its typed environment')
        if environment.get('pname') != PNAME:
            continue
        output = row.get('outputs', {}).get('out', {})
        value = output.get('path')
        if (row.get('system') != system or not isinstance(value, str)
                or re.fullmatch(STORE, value) is None
                or not Path(value).name[33:].startswith(PNAME + '-')
                or environment.get('out') != value):
            raise ValueError('scanner tools do not have one fixed native output identity')
        matches.append({'derivation': path, 'output': value,
                        'executable': value + '/bin/opengrep-artifact', 'system': system})
    if len(matches) != 1:
        raise ValueError('selected application must depend on exactly one native scanner build tool')
    return matches[0]



def command(nix, source, environment):
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
