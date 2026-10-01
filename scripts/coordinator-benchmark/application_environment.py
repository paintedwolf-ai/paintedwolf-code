"""Own application controls while preserving host tools and provider credentials."""
import os
import sys


def application_environment(overrides=None):
    environment = {name: value for name, value in os.environ.items()
                   if not name.startswith('LYCAON_')}
    environment.update({'LYCAON_DEV': '1', 'LYCAON_HARNESS': '1',
                        'LYCAON_COMMAND_PATH': environment.get('PATH', ''),
                        'LYCAON_LLM_MOCK': '0', 'LYCAON_LLM_MANUAL': '0'})
    environment.update(overrides or {})
    return environment


def isolated_command(command, source):
    return [sys.executable, str(source / 'scripts/coordinator-benchmark/application_environment.py'),
            '--', *command]


if __name__ == '__main__':
    if len(sys.argv) < 3 or sys.argv[1] != '--':
        raise SystemExit('usage: application_environment.py -- COMMAND [ARG ...]')
    os.execvpe(sys.argv[2], sys.argv[2:], application_environment())
