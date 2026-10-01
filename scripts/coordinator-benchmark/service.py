"""Run a prepared benchmark under the host's process supervisor."""
import argparse
import fcntl
import hashlib
import json
import os
import pathlib
import plistlib
import signal
import shutil
import subprocess
import sys
import tempfile
from progress import save, timestamp


def job_directory(out):
    return out.parent / ('.benchmark-service-' + hashlib.sha256(str(out).encode()).hexdigest()[:16])


def service_name(out):
    return 'org.paintedwolf.benchmark.' + hashlib.sha256(str(out).encode()).hexdigest()[:16]


def unit_argument(value, command=False):
    escaped = str(value).replace('\\', '\\\\').replace('"', '\\"').replace('%', '%%').replace('\n', '\\n')
    return '"' + (escaped.replace('$', '$$') if command else escaped) + '"'


def definition(job, platform):
    root = pathlib.Path(job['prepared']) / 'source'
    directory = job_directory(pathlib.Path(job['out']))
    command = [job['service_python'], str(directory / 'service.py'), 'execute', '--out', job['out']]
    log = str(directory / 'service.log')
    if platform == 'darwin':
        return plistlib.dumps({'Label': service_name(job['out']), 'ProgramArguments': command,
            'WorkingDirectory': str(root), 'EnvironmentVariables': {'PATH': job['path']},
            'RunAtLoad': True, 'KeepAlive': {'SuccessfulExit': False}, 'ThrottleInterval': 60,
            'StandardOutPath': log, 'StandardErrorPath': log})
    if platform == 'linux':
        return ('[Unit]\nDescription=Painted Wolf benchmark\nStartLimitIntervalSec=0\n'
                '[Service]\nType=exec\nRestart=on-failure\nRestartSec=60\n'
                'KillMode=process\nTimeoutStopSec=60\n'
                'WorkingDirectory=' + unit_argument(root) + '\n'
                'Environment=' + unit_argument('PATH=' + job['path']) + '\n'
                'ExecStart=' + ' '.join(unit_argument(arg, command=True) for arg in command) + '\n'
                '[Install]\nWantedBy=default.target\n').encode()
    raise ValueError('benchmark services require macOS or Linux')


def install(job, platform=sys.platform):
    name = service_name(job['out'])
    body = definition(job, platform)
    if platform == 'darwin':
        loaded = subprocess.run(['launchctl', 'print', f'gui/{os.getuid()}/{name}'],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if loaded.returncode == 0:
            return
        target = pathlib.Path.home() / 'Library/LaunchAgents' / (name + '.plist')
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(body)
        target.chmod(0o600)
        subprocess.run(['launchctl', 'bootstrap', f'gui/{os.getuid()}', str(target)], check=True)
    else:
        target = pathlib.Path.home() / '.config/systemd/user' / (name + '.service')
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(body)
        target.chmod(0o600)
        subprocess.run(['systemctl', '--user', 'daemon-reload'], check=True)
        subprocess.run(['systemctl', '--user', 'enable', '--now', name + '.service'], check=True)


def uninstall(job, platform=sys.platform):
    name = service_name(job['out'])
    if platform == 'darwin':
        subprocess.run(['launchctl', 'bootout', f'gui/{os.getuid()}/{name}'], check=True)
        (pathlib.Path.home() / 'Library/LaunchAgents' / (name + '.plist')).unlink(missing_ok=True)
    elif platform == 'linux':
        subprocess.run(['systemctl', '--user', 'disable', '--now', name + '.service'], check=True)
        (pathlib.Path.home() / '.config/systemd/user' / (name + '.service')).unlink(missing_ok=True)
        subprocess.run(['systemctl', '--user', 'daemon-reload'], check=True)
    else:
        raise ValueError('benchmark services require macOS or Linux')


def controller_command(job):
    out = pathlib.Path(job['out'])
    prepared = pathlib.Path(job['prepared'])
    started = (out / 'selection.json').exists()
    root = out / 'source' if (out / 'plan.json').exists() else prepared / 'source'
    script = root / 'scripts/coordinator-benchmark/run.py'
    runtime = [job['python'], str(script.with_name('python_runtime.py'))] if job['controller_bootstrap'] else [job['python']]
    command = [*runtime, str(script),
               '--allow-live', '--out', str(out), '--source-config', job['source_config']]
    return command + (['--resume'] if started else ['--prepared', str(prepared), *job['selection']])


def execute(job):
    directory = job_directory(pathlib.Path(job['out']))
    with (directory / 'lease').open('a') as lease:
        fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        current = json.loads((directory / 'job.json').read_text())
        if current['desired'] != 'running':
            return 0
        command = controller_command(job)
        with (directory / 'controller.log').open('a') as log:
            child = subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=log, stderr=subprocess.STDOUT,
                                     start_new_session=True)
            # Only this invocation's controller receives a requested stop.
            def stop(*unused):
                if child.poll() is None:
                    child.terminate()
            handlers = {sig: signal.signal(sig, stop) for sig in (signal.SIGTERM, signal.SIGINT)}
            try:
                save(directory / 'status.json', {'phase': 'running', 'controller_pid': child.pid, 'updated_at': timestamp()})
                code = child.wait()
            finally:
                for sig, handler in handlers.items():
                    signal.signal(sig, handler)
        current = json.loads((directory / 'job.json').read_text())
        phase = 'completed' if code == 0 else 'waiting_for_restart' if current['desired'] == 'running' else 'stopped'
        save(directory / 'status.json', {'phase': phase, 'returncode': code, 'updated_at': timestamp()})
        if code == 0:
            current['desired'] = 'completed'
            save(directory / 'job.json', current)
        return int(code != 0 and current['desired'] == 'running')



def adopted_interpreter(out):
    with (out / 'runner.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    interpreters = {json.loads(path.read_text())['command'][0]
                    for path in (out / 'captures').glob('episode-*/attempt-*/command.json')}
    if len(interpreters) != 1:
        raise ValueError('retained admissions do not identify one interpreter')
    interpreter = pathlib.Path(interpreters.pop())
    if not interpreter.is_file() or not interpreter.is_relative_to(out / 'source/.bin'):
        raise ValueError('retained interpreter is unavailable or outside the frozen runtime')
    return str(interpreter)

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['start', 'adopt', 'resume', 'stop', 'status', 'execute'])
    parser.add_argument('--out', type=pathlib.Path, required=True)
    parser.add_argument('--prepared', type=pathlib.Path)
    parser.add_argument('--source-config', type=pathlib.Path)
    parser.add_argument('--allow-live', action='store_true')
    args, selection = parser.parse_known_args()
    out = args.out.resolve()
    directory = job_directory(out)
    if args.action in {'start', 'adopt'}:
        from prepare import verify_prepared
        if args.action == 'adopt':
            args.prepared = out
        if not args.allow_live or not args.prepared or not args.source_config:
            raise ValueError('start requires --allow-live, --prepared, and --source-config')
        verify_prepared(args.prepared.resolve())
        if args.action == 'start' and out.exists() and not directory.exists():
            raise ValueError('start requires a new output directory')
        selection = selection[1:] if selection[:1] == ['--'] else selection
        forbidden = {'--resume', '--prepared', '--out', '--source-config', '--application-ref', '--application-worktree', '--repair-from'}
        if any(arg.split('=')[0] in forbidden for arg in selection):
            raise ValueError('selection cannot override the service target')
        job = {'out': str(out), 'prepared': str(args.prepared.resolve()),
               'source_config': str(args.source_config.resolve()), 'selection': selection,
               'python': str(pathlib.Path(sys._base_executable).resolve()), 'path': os.environ['PATH'], 'desired': 'running',
               'service_python': str(pathlib.Path(sys._base_executable).resolve()), 'controller_bootstrap': True}
        if args.action == 'adopt':
            if selection or not (out / 'plan.json').exists():
                raise ValueError('adopt requires an existing execution plan and no new selection')
            job.update(python=adopted_interpreter(out), controller_bootstrap=False)
            job['path'] = str(pathlib.Path(job['python']).parent) + os.pathsep + job['path']
        if directory.exists():
            retained = json.loads((directory / 'job.json').read_text())
            if any(retained[key] != job[key] for key in ('out', 'prepared', 'source_config', 'selection', 'python')):
                raise ValueError('service inputs differ from its retained job')
            if retained['desired'] != 'running':
                raise ValueError('service is not awaiting installation or execution')
            job = retained
        else:
            with tempfile.TemporaryDirectory(dir=out.parent, prefix='.benchmark-service-admission-') as temporary:
                staged = pathlib.Path(temporary) / 'service'
                staged.mkdir(mode=0o700)
                for name in ('service.py', 'progress.py', 'provider_backoff.py'):
                    shutil.copy2(pathlib.Path(__file__).with_name(name), staged / name)
                save(staged / 'job.json', job)
                staged.rename(directory)
        install(job)
    else:
        if selection:
            raise ValueError('selection belongs only to start')
        job = json.loads((directory / 'job.json').read_text())
        if args.action == 'execute':
            return execute(job)
        if args.action == 'status':
            print((directory / 'status.json').read_text() if (directory / 'status.json').exists() else json.dumps({'phase': 'starting'}))
        elif args.action == 'stop':
            job['desired'] = 'stopped'
            save(directory / 'job.json', job)
            uninstall(job)
        elif args.action == 'resume':
            if not args.allow_live:
                raise ValueError('resume requires --allow-live')
            if job['desired'] != 'stopped':
                raise ValueError('resume requires an explicitly stopped service')
            job['desired'] = 'running'
            save(directory / 'job.json', job)
            install(job)
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
