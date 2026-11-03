"""Reclaim unused SDKs on disposable Ubuntu verification runners."""
import json
import os
from pathlib import Path
import shutil
import subprocess

GIB = 1024 ** 3
UNUSED_SDKS = ('/usr/local/lib/android', '/usr/share/dotnet')


def prepare(environment):
    if (environment.get('RUNNER_ENVIRONMENT') != 'github-hosted'
            or environment.get('RUNNER_OS') != 'Linux' or environment.get('ImageOS') != 'ubuntu24'):
        return {'state': 'unchanged', 'reason': 'outside disposable Ubuntu 24 runner'}
    workspace = environment['GITHUB_WORKSPACE']
    before = shutil.disk_usage(workspace).free
    removed = []
    if before < 32 * GIB:
        # Desktop verification declares no Android or .NET toolchain. Preserve
        # every compiler cache, source slot, runner service and selected tool.
        removed = [path for path in UNUSED_SDKS if Path(path).exists()]
        if removed:
            subprocess.run(['sudo', '-n', 'rm', '-rf', '--', *removed], check=True)
    after = shutil.disk_usage(workspace).free
    return {'state': 'prepared', 'free_before_bytes': before, 'free_after_bytes': after,
            'reclaimed_bytes': after - before, 'removed_unused_sdks': removed}


if __name__ == '__main__':
    print(json.dumps(prepare(os.environ), sort_keys=True))
