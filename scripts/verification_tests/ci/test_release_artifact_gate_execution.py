"""The release artifact gate runs before publication and stops staging when verification fails."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[3]
STAGE_INPUTS = (
    'scripts/release-stage-platform-artifacts.sh', 'scripts/sha256.sh', 'scripts/semver-compare.py',
    'scripts/release-metadata.py', 'scripts/release_semver.py', 'scripts/update_keys.py',
    'VERSION', 'RELEASE_BUILD', 'packaging/update-keys.json',
)


def job_ancestors(jobs, name):
    needs = jobs[name].get('needs', [])
    pending = [needs] if isinstance(needs, str) else list(needs)
    seen = set()
    while pending:
        job = pending.pop()
        if job in seen:
            continue
        seen.add(job)
        parent = jobs[job].get('needs', [])
        pending.extend([parent] if isinstance(parent, str) else parent)
    return seen


def jobs_running(jobs, script):
    return {name for name, job in jobs.items()
            if any(script in step.get('run', '') for step in job.get('steps', []))}


class ReleaseArtifactGateTests(unittest.TestCase):
    def test_publication_jobs_depend_on_the_artifact_gate(self):
        jobs = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_text())['jobs']
        gates = jobs_running(jobs, 'scripts/release-stage-platform-artifacts.sh')
        self.assertEqual(len(gates), 1, gates)
        gate = gates.pop()
        gate_steps = [step for step in jobs[gate]['steps']
                      if 'scripts/release-stage-platform-artifacts.sh' in step.get('run', '')]
        for step in gate_steps:
            self.assertFalse(step.get('continue-on-error', False), 'a swallowed verification reads as proof')
            self.assertNotIn('|| true', step['run'])
        publishers = [name for name, job in jobs.items()
                      if any('immutable' in step.get('name', '').lower() and 'publish' in step.get('name', '').lower()
                             or 'activate release channel' in step.get('name', '').lower()
                             for step in job.get('steps', []))]
        self.assertGreaterEqual(len(publishers), 2, publishers)
        for publisher in publishers:
            with self.subTest(publisher=publisher):
                self.assertIn(gate, job_ancestors(jobs, publisher))

    def stage_root(self, scratch, task_exit):
        for relative in STAGE_INPUTS:
            target = scratch / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / relative, target)
        record = scratch / 'task-calls'
        task = scratch / 'task'
        task.write_text(f'#!/bin/sh\nprintf "%s\\n" "$*" >> "{record}"\nexit {task_exit}\n')
        task.chmod(0o700)
        bundle = scratch / 'lycaon-den/src-tauri/target/aarch64-apple-darwin/release/bundle'
        app = bundle / 'macos/Painted Wolf Code.app'
        app.mkdir(parents=True)
        (bundle / 'macos/Painted Wolf Code.app.tar.gz').write_bytes(b'updater')
        dmg = bundle / 'dmg/Painted Wolf Code.dmg'
        dmg.parent.mkdir(parents=True)
        dmg.write_bytes(b'disk image')
        return record, app, dmg

    def run_stage(self, scratch, output):
        version = (ROOT / 'VERSION').read_text().strip()
        return subprocess.run(
            ['bash', str(scratch / 'scripts/release-stage-platform-artifacts.sh'), '--platform', 'darwin-aarch64',
             '--goos', 'darwin', '--arch', 'aarch64', '--version', version, '--package-extensions', 'dmg',
             '--updater-extension', 'app.tar.gz', '--output', str(output)],
            cwd=scratch, env={k: v for k, v in os.environ.items() if k != 'LYCAON_OPENGREP_CANDIDATE'},
            capture_output=True, text=True, timeout=60)

    def test_failed_bundle_verification_stages_nothing(self):
        with tempfile.TemporaryDirectory() as directory:
            scratch = Path(directory)
            record, app, dmg = self.stage_root(scratch, task_exit=1)
            output = scratch / 'release'
            result = self.run_stage(scratch, output)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(record.read_text().splitlines(),
                             [f'bundle:verify -- --app {app} --dmg {dmg} --require-signed'])
            self.assertFalse(output.exists() and any(output.iterdir()), 'a failed verification must stage nothing')

    def test_bundle_verification_runs_before_any_other_task(self):
        with tempfile.TemporaryDirectory() as directory:
            scratch = Path(directory)
            record, app, dmg = self.stage_root(scratch, task_exit=0)
            self.run_stage(scratch, scratch / 'release')
            calls = record.read_text().splitlines()
            self.assertEqual(calls[0], f'bundle:verify -- --app {app} --dmg {dmg} --require-signed')


if __name__ == '__main__':
    unittest.main()
