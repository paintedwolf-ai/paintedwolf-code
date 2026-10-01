"""Resume local execution and website handoff as separate durable stages."""
import argparse
import fcntl
import json
import os
import pathlib
import subprocess
import sys
from progress import save, timestamp
from completion import selected_measurements
from grading_state import report_lease
from snapshot import file_hash, identity


def publication_snapshot(run, control):
    with report_lease(run):
        plan = json.loads((run / 'plan.json').read_text())
        _, status = selected_measurements(run, plan)
        if not status or status.get('phase') not in {'completed', 'completed_with_unmeasured'} or not status.get('report_sha256'):
            raise ValueError('execution has no selected terminal report')
        source = pathlib.Path(status['report_path'])
        if file_hash(source) != status['report_sha256']:
            raise ValueError('selected report changed before handoff')
        report = json.loads(source.read_text())
        if report['mode'] != 'release' or plan['mode'] != 'release':
            raise ValueError('website handoff requires a release report')
        parent = control / 'reports'
        parent.mkdir(exist_ok=True)
        destination = parent / (identity(report) + '.json')
        if destination.exists():
            if json.loads(destination.read_text()) != report:
                raise ValueError('retained publication snapshot changed')
        else:
            save(destination, report)
        return destination


def pipeline(run, source_config, website=None, target_args=None, cadence='selected'):
    control = run.parent / (run.name + '-pipeline')
    control.mkdir(parents=True, exist_ok=True, mode=0o700)
    with (control / 'lease').open('a') as lease:
        fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
        path = control / 'status.json'
        state = json.loads(path.read_text()) if path.exists() else {'completed': [], 'run': str(run), 'website': str(website) if website else None}
        if state['run'] != str(run) or state['website'] != (str(website) if website else None):
            raise ValueError('pipeline destination changed')
        def stage(name, command, cwd, environment=None):
            if name in state['completed']:
                return True
            state.update(phase=name, updated_at=timestamp(), supervisor_pid=os.getpid())
            save(path, state)
            with (control / (name + '.log')).open('a') as log:
                result = subprocess.run(command, cwd=cwd, env=environment, stdout=log, stderr=subprocess.STDOUT)
            if result.returncode:
                state.update(phase='paused', failed_stage=name, returncode=result.returncode, updated_at=timestamp())
                save(path, state)
                return False
            state['completed'].append(name)
            state.pop('failed_stage', None)
            state.pop('returncode', None)
            state.pop('failure', None)
            save(path, state)
            return True

        try:
            source = run / 'source'
            resume = (run / 'plan.json').exists() or (run / 'selection.json').exists()
            if not resume:
                state['completed'] = [name for name in state['completed'] if name != 'evaluation']
            runner = source / 'scripts/coordinator-benchmark/run.py' if (source / 'source.json').exists() else pathlib.Path(__file__).with_name('run.py')
            command = [sys.executable, str(runner), '--allow-live', '--source-config', str(source_config), '--out', str(run)]
            command.extend(['--resume'] if resume else ['--mode', 'release', '--cadence', cadence, *(target_args or [])])
            if not stage('evaluation', command, runner.parents[2]):
                return False
            if not (run / 'plan.json').exists():
                state.update(phase='skipped', updated_at=timestamp())
                save(path, state)
                return True
            state['completed'] = [name for name in state['completed'] if name not in {'website-import', 'website-check'}]
            state.update(phase='publication', updated_at=timestamp())
            save(path, state)
            publication = publication_snapshot(run, control)
            if website is not None:
                # The website can change independently between invocations.
                checkout = pathlib.Path(__file__).resolve().parents[2]
                sys.path.append(str(checkout / 'scripts'))
                from artifact_paths import bin_dir
                env = {**os.environ, 'PATH': str(bin_dir(checkout)) + os.pathsep + os.environ['PATH']}
                if not stage('website-import', ['./task', 'benchmark:import', '--', str(publication)], website, env):
                    return False
                if not stage('website-check', ['./task', 'check'], website, env):
                    return False
            for key in ('failure', 'failed_stage', 'returncode'):
                state.pop(key, None)
            state.update(phase='completed', updated_at=timestamp())
            save(path, state)
            return True
        except BaseException as error:
            state.update(phase='paused', failed_stage=state.get('phase', 'evaluation'), updated_at=timestamp(),
                         failure={'type': type(error).__name__})
            state.pop('returncode', None)
            save(path, state)
            if isinstance(error, (KeyboardInterrupt, SystemExit)):
                raise
            return False


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--allow-live', action='store_true', required=True)
    parser.add_argument('--source-config', type=pathlib.Path, required=True)
    parser.add_argument('--out', type=pathlib.Path, required=True)
    parser.add_argument('--website', type=pathlib.Path)
    parser.add_argument('--cadence', choices=['selected', 'major-minor'], default='selected')
    from target_selection import add_arguments
    add_arguments(parser)
    args = parser.parse_args()
    target = []
    for name in ('application_ref', 'prepared', 'repair_from', 'harness_ref', 'reason'):
        if value := getattr(args, name):
            target.extend(['--' + name.replace('_', '-'), str(value.resolve()) if isinstance(value, pathlib.Path) else value])
    if args.application_worktree:
        target.append('--application-worktree')
    if args.harness_worktree:
        target.append('--harness-worktree')
    ok = pipeline(args.out.resolve(), args.source_config.resolve(), args.website.resolve() if args.website else None,
                  target, args.cadence)
    sys.exit(0 if ok else 1)
