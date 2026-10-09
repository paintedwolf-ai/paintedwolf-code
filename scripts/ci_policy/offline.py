"""Run a required lane with loopback networking only on an ephemeral Linux runner."""
import os
import subprocess
import sys


def command(arguments, uid, gid):
    # Namespace setup is privileged; test execution immediately returns to the runner account.
    return ['sudo', '--preserve-env', 'unshare', '--net', '--', 'env', 'PATH=' + os.environ['PATH'],
            'HOME=' + os.environ['HOME'], 'sh', '-c',
            'set -e; ip link set lo up; uid="$1"; gid="$2"; shift 2; exec setpriv --reuid="$uid" --regid="$gid" --init-groups -- "$@"',
            'offline-lane', str(uid), str(gid), *arguments]


def main():
    if os.environ.get('GITHUB_ACTIONS') != 'true' or not sys.platform.startswith('linux'):
        raise SystemExit('offline lane requires an ephemeral Linux Actions runner')
    # Pass identities separately so no source path or lane name becomes shell text.
    arguments = [sys.executable, 'scripts/ci_verification.py', 'run', *sys.argv[1:]]
    invocation = command(arguments, os.getuid(), os.getgid())
    environment = {**os.environ, 'GOPROXY': 'off', 'GOSUMDB': 'off', 'CARGO_NET_OFFLINE': 'true', 'NPM_CONFIG_OFFLINE': 'true'}
    environment.pop('GH_TOKEN', None)
    environment.pop('GITHUB_TOKEN', None)
    return subprocess.call(invocation, env=environment)


if __name__ == '__main__':
    raise SystemExit(main())
