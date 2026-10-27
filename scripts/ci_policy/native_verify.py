"""Compile native test targets or execute their complete test recipe."""
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]


def commands(component, arguments):
    if arguments not in ([], ['--compile-only']):
        raise ValueError('native verification accepts only --compile-only')
    compile_flags = ['--no-run'] if arguments else []
    if component == 'den':
        return [['bash', str(ROOT / 'scripts/ensure-tauri-binaries.sh')],
                ['cargo', 'test', '--locked', '--workspace', '--all-targets', '--quiet', *compile_flags]]
    if component == 'decide':
        return [['bash', str(ROOT / 'scripts/toolchain/decide-test.sh'), '--quiet', *compile_flags]]
    raise ValueError('unknown native verification component')


def run(component, arguments):
    for command in commands(component, arguments):
        code = subprocess.call(command)
        if code:
            return code
    return 0


if __name__ == '__main__':
    try:
        raise SystemExit(run(sys.argv[1], sys.argv[2:]))
    except (ValueError, IndexError) as error:
        print(str(error), file=sys.stderr)
        raise SystemExit(2)
