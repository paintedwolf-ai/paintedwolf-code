"""Validate the live launcher's host tools before provider admission."""
import shutil
import subprocess


def require_launcher_runtime():
    for name in ('jq', 'node'):
        if shutil.which(name) is None:
            raise RuntimeError('Missing evaluation prerequisite: ' + name)
    try:
        subprocess.run(['node', '-e', "require('node:net'); require('node:fs');"],
                       check=True, capture_output=True, text=True)
    except (OSError, subprocess.CalledProcessError) as error:
        raise RuntimeError('The evaluation Node runtime cannot load node:net and node:fs; '
                           'use the supported Node runtime from .node-version on PATH.') from error


if __name__ == '__main__':
    require_launcher_runtime()
