"""Workflow commands preserve machine-readable plans, required-job verdicts and gate errors."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[3]


class WorkflowCommandTests(unittest.TestCase):
    def test_release_builds_declared_package_families_and_preserves_failures(self):
        workflow = yaml.safe_load((ROOT / '.github/workflows/release.yml').read_text())
        steps = workflow['jobs']['build-release']['steps']
        build = next(step for step in steps if step.get('name') == 'Build, sign, and notarize')
        self.assertEqual(build['env']['PACKAGE_EXTENSIONS'], "${{ join(matrix.package_extensions, ',') }}")
        dependencies = next(step for step in steps if step.get('name') == 'Install Linux bundle dependencies')
        self.assertEqual(dependencies['uses'], './.github/actions/setup-apt')
        self.assertIn('libarchive-tools', dependencies['with']['packages'].split())
        catalog = json.loads((ROOT / 'packaging/release-platforms.json').read_text())
        cases = [(row['goos'], ','.join(row['package_extensions']), 0) for row in catalog['platforms']]
        cases += [('linux', 'deb,rpm', 0), ('linux', 'deb', 0), ('linux', 'deb,rpm,pkg.tar.zst', 37)]
        for goos, extensions, task_exit in cases:
            with self.subTest(goos=goos, extensions=extensions, task_exit=task_exit), tempfile.TemporaryDirectory() as directory:
                scratch = Path(directory)
                record = scratch / 'task-arguments'
                task = scratch / 'task'
                task.write_text('#!/bin/sh\nprintf "%s\\n" "$@" > "$RECORD"\nexit "$TASK_EXIT"\n')
                task.chmod(0o700)
                notary = scratch / '.release-controller/scripts/macos-notary-tools/notary.py'
                notary.parent.mkdir(parents=True)
                notary.write_text('')
                environment = {**os.environ, 'GOOS': goos, 'PACKAGE_EXTENSIONS': extensions,
                               'GITHUB_WORKSPACE': str(scratch), 'RUNNER_TEMP': str(scratch),
                               'RECORD': str(record), 'TASK_EXIT': str(task_exit)}
                for name in ('TAURI_SIGNING_PRIVATE_KEY', 'APPLE_SIGNING_IDENTITY', 'APPLE_API_ISSUER',
                             'APPLE_API_KEY', 'AZURE_CLIENT_ID', 'AZURE_CLIENT_SECRET', 'AZURE_TENANT_ID',
                             'AZURE_ARTIFACT_SIGNING_ENDPOINT', 'AZURE_ARTIFACT_SIGNING_ACCOUNT',
                             'AZURE_ARTIFACT_SIGNING_PROFILE'):
                    environment[name] = 'fixture'
                result = subprocess.run(['bash', '-c', build['run']], cwd=scratch, env=environment,
                                        capture_output=True, text=True, timeout=10)
                if goos == 'linux' and extensions not in {'deb,rpm', 'deb,rpm,pkg.tar.zst'}:
                    self.assertNotEqual(result.returncode, 0)
                    self.assertFalse(record.exists(), 'unsupported package forms must not start a build')
                else:
                    self.assertEqual(result.returncode, task_exit, result.stderr)
                    expected = ['den:bundle']
                    if goos == 'linux' and extensions == 'deb,rpm,pkg.tar.zst':
                        expected += ['--', '--pacman-container']
                    self.assertEqual(record.read_text().splitlines(), expected)

    def command(self, *arguments, **environment):
        return subprocess.run([sys.executable, 'scripts/ci_verification.py', *arguments],
                              cwd=ROOT, env={**os.environ, **environment},
                              capture_output=True, text=True, timeout=30)

    def test_required_job_verdict_reaches_the_process_exit(self):
        results = json.dumps({'verification': {'result': 'success'}, 'platform': {'result': 'skipped'}})
        passed = self.command('gate', '--skipped', 'platform', NEEDS_JSON=results)
        self.assertEqual(passed.returncode, 0, passed.stderr)
        draft = self.command('gate', '--skipped', 'platform', '--draft', 'true', NEEDS_JSON=results)
        self.assertNotEqual(draft.returncode, 0)
        self.assertIn('draft pull requests are not verified', draft.stderr)

    def test_schedule_summary_preserves_matrix_json_output(self):
        with tempfile.TemporaryDirectory() as directory:
            summary = Path(directory) / "summary.md"
            measured = Path(directory) / "timings.json"
            measured.write_text(json.dumps({"runs": [42], "lanes": {"ready": 250, "ready-lint": 200, "ready-tests 1/2": 240, "ready-tests 2/2": 240}}))
            result = self.command('matrix', 'fast', '--timings', str(measured),
                                  GITHUB_STEP_SUMMARY=str(summary))
            self.assertEqual(result.returncode, 0, result.stderr)
            rows = json.loads(result.stdout)['include']
            self.assertEqual(rows[0]['timing_basis'], 'measured')
            self.assertEqual(rows[0]['expected_seconds'], 250)
            self.assertIn('Estimate basis: `measured`', summary.read_text())
            self.assertIn('Nightly timing sources: 42', summary.read_text())


class PlatformEvidenceTests(unittest.TestCase):
    def test_linux_confinement_legs_execute_once_before_and_after_the_build(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/platform-verification.yml").read_text())
        steps = {step.get("id"): step for step in workflow["jobs"]["upgrade-corpus"]["steps"]}
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            task = root / "task"
            task.write_text("#!/bin/sh\nprintf '%s\\n' \"$@\"\n")
            task.chmod(0o755)
            legs = []
            for name in ("launch", "confinement"):
                result = subprocess.run(["bash", "-e", "-c", steps[name]["run"]], cwd=root,
                                        capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                arguments = result.stdout.splitlines()
                self.assertEqual(arguments[:2], ["test:confine:linux", "--"])
                legs.extend(arguments[2:])
            self.assertEqual(legs, ["launch", "sandbox", "supervisor", "browser", "helpers"])
            self.assertEqual(steps["build"]["if"], "${{ !cancelled() }}")
            self.assertEqual(steps["corpus"]["if"], "${{ !cancelled() && steps.build.outcome == 'success' }}")

    def test_upgrade_output_survives_success_and_failure(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/platform-verification.yml").read_text())
        step = next(item for item in workflow["jobs"]["upgrade-corpus"]["steps"]
                    if item.get("name") == "Boot upgrade corpus fixtures")
        for code in (0, 23):
            with self.subTest(code=code), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                (root / "scripts").mkdir()
                shutil.copyfile(ROOT / "scripts/artifact_paths.py", root / "scripts/artifact_paths.py")
                task = root / "task"
                task.write_text("#!/bin/sh\n[ \"$#\" -eq 1 ] && [ \"$1\" = upgrade:corpus:boot ] || exit 98\n"
                                f"echo fixture-start\necho fixture-diagnostic >&2\nexit {code}\n")
                task.chmod(0o755)
                artifacts = root / "evidence"
                environment = {**os.environ, "GITHUB_WORKSPACE": str(root),
                               "PW_TEST_ARTIFACT_ROOT": str(artifacts)}
                result = subprocess.run(["bash", "-e", "-c", step["run"]], cwd=root, env=environment,
                                        capture_output=True, text=True, timeout=20)
                self.assertEqual(result.returncode, code, result.stderr)
                expected = "fixture-start\nfixture-diagnostic\n"
                self.assertEqual(result.stdout, expected)
                self.assertEqual((artifacts / "ci/upgrade-corpus.log").read_text(), expected)
