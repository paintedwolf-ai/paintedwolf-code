"""Own application controls while preserving host tools and provider credentials."""
import os
import sys
import shutil
import subprocess


def application_environment(overrides=None):
    environment = {name: value for name, value in os.environ.items()
                   if not name.startswith('LYCAON_')}
    environment.update({'LYCAON_DEV': '1', 'LYCAON_HARNESS': '1',
                        'LYCAON_COMMAND_PATH': environment.get('PATH', ''),
                        'LYCAON_LLM_MOCK': '0', 'LYCAON_LLM_MANUAL': '0'})
    environment.update(overrides or {})
    return environment


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


def isolated_command(command, source):
    return [sys.executable, str(source / 'scripts/coordinator-benchmark/application_environment.py'),
            '--', *command]


if __name__ == '__main__':
    if sys.argv[1:] == ['--check-prerequisites']:
        require_launcher_runtime()
        raise SystemExit(0)
    if len(sys.argv) < 3 or sys.argv[1] != '--':
        raise SystemExit('usage: application_environment.py -- COMMAND [ARG ...]')
    os.execvpe(sys.argv[2], sys.argv[2:], application_environment())
