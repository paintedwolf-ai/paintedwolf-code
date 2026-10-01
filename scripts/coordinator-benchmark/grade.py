"""Run independent checks against a completed application fixture, without provider calls."""
import argparse
import json
import pathlib
import subprocess
import sys
import tempfile
from artifacts import copy_project
from oracle_process import remove_container

class GradingUnavailable(RuntimeError):
    """The container service could not execute the oracle."""


IMAGE = 'python@sha256:9d2e5553305c7c7b0097999bb17187c69b921ccd6bc9d40e4bb5ebe652c00285'


def grade_snapshot(project, baseline, case):
    from focused_outcomes import FOCUSED, grade as focused_grade
    if case in FOCUSED:
        return focused_grade(project, baseline, case)
    root = pathlib.Path(__file__).resolve().parent
    command = ['docker', 'run', '--rm', '--network=none', '--read-only', '--cap-drop=ALL',
               '--security-opt=no-new-privileges', '--pids-limit=64', '--memory=256m', '--cpus=1',
               '--user=65534:65534', '--tmpfs=/tmp:rw,nosuid,nodev,noexec,size=16m',
               '--mount', f'type=bind,src={project.resolve()},dst=/candidate,readonly',
               '--mount', f'type=bind,src={baseline.resolve()},dst=/baseline,readonly',
               '--mount', f'type=bind,src={root},dst=/grader,readonly',
               IMAGE, 'python3', '-B', '/grader/outcomes.py', case]
    # The container ID is an ownership receipt, so timeout cleanup stays exact.
    with tempfile.TemporaryDirectory(prefix='paintedwolf-grader-') as temporary:
        cid = pathlib.Path(temporary) / 'container-id'
        command[2:2] = ['--cidfile', str(cid)]
        try:
            completed = subprocess.run(command, text=True, capture_output=True, timeout=600)
        finally:
            if cid.exists():
                container = cid.read_text().strip()
                if not remove_container(container):
                    raise GradingUnavailable('grading container cleanup is unconfirmed: ' + container)
    if completed.returncode == 125:
        raise GradingUnavailable('grading container service unavailable')
    if completed.returncode:
        raise RuntimeError('grading container failed: ' + completed.stderr[-2000:])
    result = json.loads(completed.stdout)
    if 'measurement_error' in result:
        raise RuntimeError('independent oracle did not complete: ' + result['measurement_error']['kind'])
    return result


def grade(project, baseline, case):
    checkout = pathlib.Path(__file__).resolve().parents[2]
    sys.path.append(str(checkout / 'scripts'))
    from artifact_paths import artifact_root
    staging = artifact_root(checkout) / 'coordinator-grading'
    staging.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(dir=staging) as temporary:
        candidate = pathlib.Path(temporary) / 'candidate'
        copy_project(project, candidate)
        return grade_snapshot(candidate, baseline, case)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--project', required=True, type=pathlib.Path)
    p.add_argument('--baseline', required=True, type=pathlib.Path)
    from focused_outcomes import FOCUSED
    p.add_argument('--case', required=True, choices=[*sorted(FOCUSED), 'repair', 'service', 'planner', 'orientation', 'revision', 'changed-limit'])
    p.add_argument('--out', required=True, type=pathlib.Path)
    args = p.parse_args()
    result = grade(args.project, args.baseline, args.case)
    args.out.write_text(json.dumps(result, indent=2) + '\n')
    print(f"{sum(c['passed'] for c in result['checks'])}/{len(result['checks'])} checks passed; outcome={'passed' if result['passed'] else 'failed'}")


if __name__ == '__main__':
    main()
