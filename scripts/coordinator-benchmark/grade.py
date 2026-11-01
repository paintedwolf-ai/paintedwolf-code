"""Run independent checks against a completed application fixture, without provider calls."""
import argparse
import json
import pathlib
import re
import selectors
import subprocess
import sys
import tempfile
import time
from artifacts import copy_project

class GradingUnavailable(RuntimeError):
    """The container service could not execute the oracle."""


IMAGE = 'python@sha256:9d2e5553305c7c7b0097999bb17187c69b921ccd6bc9d40e4bb5ebe652c00285'


def capture(command, timeout=30, output_limit=1024*1024):
    process=subprocess.Popen(command,stdout=subprocess.PIPE,stderr=subprocess.PIPE)
    output={'stdout':bytearray(),'stderr':bytearray()}
    deadline=time.monotonic()+timeout
    selector=selectors.DefaultSelector()
    try:
        selector.register(process.stdout,selectors.EVENT_READ,'stdout')
        selector.register(process.stderr,selectors.EVENT_READ,'stderr')
        while selector.get_map():
            remaining=deadline-time.monotonic()
            if remaining<=0: raise subprocess.TimeoutExpired(command,timeout)
            for key,_ in selector.select(remaining):
                chunk=key.fileobj.read1(8192)
                if not chunk:
                    selector.unregister(key.fileobj);continue
                output[key.data].extend(chunk)
                if sum(len(body) for body in output.values())>output_limit:
                    return None
        remaining=deadline-time.monotonic()
        if remaining<=0: raise subprocess.TimeoutExpired(command,timeout)
        code=process.wait(timeout=remaining)
        return subprocess.CompletedProcess(command,code,bytes(output['stdout']),bytes(output['stderr']))
    finally:
        selector.close()
        if process.poll() is None: process.kill()
        process.wait()
        process.stdout.close();process.stderr.close()


def remove_container(container):
    if not re.fullmatch('[a-f0-9]{64}', container):
        raise ValueError('invalid oracle container ownership receipt')
    removed = subprocess.run(['docker', 'rm', '--force', container], capture_output=True, timeout=30)
    if removed.returncode == 0:
        return True
    listed = subprocess.run(['docker', 'container', 'ls', '--all', '--no-trunc',
                             '--filter', 'id=' + container, '--format', '{{json .ID}}'],
                            capture_output=True, timeout=30)
    if listed.returncode != 0:
        return False
    identities = [json.loads(line) for line in listed.stdout.splitlines()]
    return container not in identities


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
