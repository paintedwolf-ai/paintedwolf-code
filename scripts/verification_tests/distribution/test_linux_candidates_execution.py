"""Linux candidates use catalog identities without inheriting publication authority."""

import copy
import io
import json
import lzma
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile
import zlib
import yaml

from linux_release import candidates
from linux_release.candidates import matrix, verify

ROOT = Path(__file__).resolve().parents[3]


class LinuxCandidateTests(unittest.TestCase):
    def test_pacman_family_is_declared_without_changing_native_runner_identity(self):
        catalog = copy.deepcopy(self.catalog)
        for row in catalog["platforms"]:
            if row.get("goos") == "linux":
                row["package_extensions"] = ["deb", "rpm", "pkg.tar.zst"]
        result = matrix(catalog)
        self.assertEqual(len(result["include"]), 2)
        self.assertTrue(all(row["package_extensions"] == ["deb", "rpm", "pkg.tar.zst"] for row in result["include"]))

    def setUp(self):
        self.catalog = json.loads((ROOT / "packaging/release-platforms.json").read_text())

    def test_matrix_uses_both_catalog_architectures_and_no_other_platform(self):
        rows = matrix(self.catalog)["include"]
        self.assertEqual({row["goarch"] for row in rows}, {"amd64", "arm64"})
        self.assertTrue(all(row["goos"] == "linux" for row in rows))
        for row in rows:
            self.assertEqual(verify(self.catalog, row["updater_key"], row["runner"], row["goarch"]), row)

    def test_missing_duplicate_or_changed_native_contract_fails(self):
        for field, value in [("runner", "wrong"), ("goarch", "386"), ("artifact_arch", "armhf"),
                             ("goos", "darwin"), ("publication", "unknown"),
                             ("package_extensions", ["deb"]), ("updater_extension", "app.tar.gz")]:
            catalog = copy.deepcopy(self.catalog)
            row = next(row for row in catalog["platforms"] if row["goarch"] == "amd64")
            row[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                verify(catalog, "linux-x86_64", "ubuntu-24.04", "amd64")
        for rows in ([], [self.catalog["platforms"][1]] * 2):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                matrix({"schema_version": 2, "platforms": rows})

    def test_candidates_share_full_catalog_admission(self):
        malformed = []
        catalog = copy.deepcopy(self.catalog)
        catalog["schema_version"] = 2.0
        malformed.append(catalog)
        catalog = copy.deepcopy(self.catalog)
        catalog["platforms"].append(copy.deepcopy(catalog["platforms"][0]))
        malformed.append(catalog)
        for field, value in [("package_extensions", ["dmg", "dmg"]),
                             ("updater_extension", "invalid extension"),
                             ("publication", "unknown")]:
            catalog = copy.deepcopy(self.catalog)
            catalog["platforms"][0][field] = value
            malformed.append(catalog)
        catalog = copy.deepcopy(self.catalog)
        del catalog["platforms"][1]["updater_extension"]
        malformed.append(catalog)
        for catalog in malformed:
            with self.subTest(catalog=catalog):
                with self.assertRaises(ValueError):
                    matrix(catalog)
                with self.assertRaises(ValueError):
                    verify(catalog, "linux-x86_64", "ubuntu-24.04", "amd64")

    def test_runner_identity_variable_does_not_replace_the_catalog_label(self):
        command = [sys.executable, str(ROOT / "scripts/linux_release/candidates.py"),
                   "--catalog", str(ROOT / "packaging/release-platforms.json"),
                   "--platform", "linux-x86_64", "--runner-label", "ubuntu-24.04",
                   "--architecture", "amd64"]
        environment = {**os.environ, "RUNNER_NAME": "GitHub Actions 1000000000"}
        result = subprocess.run(command, env=environment, capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["runner"], "ubuntu-24.04")
        command[command.index("--runner-label") + 1] = "different-label"
        result = subprocess.run(command, env=environment, capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")

    def test_public_linux_rows_still_have_nonpublishing_candidate_builds(self):
        for row in self.catalog["platforms"]:
            if row["goos"] == "linux":
                row["publication"] = "public"
        self.assertEqual(len(matrix(self.catalog)["include"]), 2)


class CandidateRequestTests(unittest.TestCase):
    def test_request_path_filters_match_the_packaging_comparison(self):
        workflow = yaml.load((ROOT / '.github/workflows/linux-candidate-requests.yml').read_text(), Loader=yaml.BaseLoader)
        self.assertEqual(workflow['on']['pull_request']['paths'], list(candidates.REQUEST_PATHS))

    def setUp(self):
        self.environment = dict(GITHUB_REPOSITORY='o/r', GITHUB_RUN_ID='30', GITHUB_RUN_ATTEMPT='2',
                                GITHUB_EVENT_NAME='pull_request', GITHUB_SHA='a' * 40, GITHUB_WORKFLOW_SHA='a' * 40)
        self.event = dict(action='enqueued', pull_request=dict(number=7, head=dict(ref='fix/a&b')))
        self.run = dict(id=20, run_attempt=2, event='pull_request', status='completed', conclusion='success',
                        path='.github/workflows/linux-candidate-requests.yml', repository=dict(full_name='o/r'),
                        pull_requests=[dict(number=7)], referenced_workflows=[
                            dict(path='o/r/.github/workflows/linux-candidates.yml@refs/pull/7/merge', sha='b' * 40)])
        self.record = dict(version=1, repository='o/r', run_id=20, run_attempt=2, controller_sha='b' * 40,
                           source_sha='b' * 40, source_tree='c' * 40)
        self.artifact = dict(id=40, name='linux-candidate-source-2', expired=False, size_in_bytes=400)

    def archive(self, record=None, name='candidate-source.json'):
        output = io.BytesIO()
        with zipfile.ZipFile(output, 'w') as archive:
            archive.writestr(name, json.dumps(self.record if record is None else record))
        return output.getvalue()

    def request(self, runs=None, artifacts=None, raw=None, same=True, current='a' * 40):
        with patch('ci_policy.github.api', side_effect=[{'workflow_runs': [self.run] if runs is None else runs},
                   {'artifacts': [self.artifact] if artifacts is None else artifacts}]) as api, \
                patch.object(candidates.subprocess, 'check_output', return_value=self.archive() if raw is None else raw), \
                patch.object(candidates, 'git', return_value=current), \
                patch.object(candidates, 'same_inputs', return_value=same) as compare:
            result = candidates.request_build(self.environment, self.event)
        return result, api, compare

    def test_enqueue_uses_same_pr_success_and_exact_source_metadata(self):
        for same in (False, True):
            result, api, compare = self.request(same=same)
            self.assertEqual(result, not same)
            compare.assert_called_once_with(self.record, 'a' * 40)
            self.assertIn('branch=fix%2Fa%26b', api.call_args_list[0].args[0])
            self.assertIn('per_page=20', api.call_args_list[0].args[0])
        mutations = [dict(conclusion='failure'), dict(status='in_progress'), dict(event='workflow_dispatch'),
                     dict(id=30), dict(path='.github/workflows/other.yml'), dict(repository=dict(full_name='foreign/r')),
                     dict(pull_requests=[dict(number=8)])]
        for changed in mutations:
            with self.subTest(changed=changed):
                result, api, compare = self.request(runs=[{**self.run, **changed}])
                self.assertTrue(result)
                self.assertEqual(api.call_count, 1)
                compare.assert_not_called()

    def test_source_record_requires_exact_attempt_controller_and_bounded_shape(self):
        mutations = [dict(version=True), dict(repository='foreign/r'), dict(run_id=21), dict(run_attempt=1),
                     dict(controller_sha='a' * 40), dict(source_sha='a' * 40), dict(source_tree='short'), dict(extra=True)]
        for changed in mutations:
            with self.subTest(changed=changed):
                self.assertIsNone(candidates.read_source(self.archive({**self.record, **changed}), self.run, 'o/r'))
        for references in ([], self.run['referenced_workflows'] * 2):
            self.assertIsNone(candidates.read_source(self.archive(), {**self.run, 'referenced_workflows': references}, 'o/r'))
        self.assertIsNone(candidates.read_source(self.archive(name='../candidate-source.json'), self.run, 'o/r'))
        self.assertIsNone(candidates.read_source(b'x' * (candidates.SOURCE_LIMIT + 1), self.run, 'o/r'))

    def test_unavailable_or_uncertain_evidence_builds_normally(self):
        for artifacts, raw in [([], b''), ([{**self.artifact, 'expired': True}], b''),
                               ([self.artifact, self.artifact], b''),
                               ([{**self.artifact, 'size_in_bytes': candidates.SOURCE_LIMIT + 1}], b''),
                               ([self.artifact], b'not a zip'), ([self.artifact], self.archive({}))]:
            with self.subTest(artifacts=artifacts):
                result, _, compare = self.request(artifacts=artifacts, raw=raw)
                self.assertTrue(result)
                compare.assert_not_called()
        for problem in (OSError, subprocess.CalledProcessError(1, ['gh']), ValueError):
            with patch('ci_policy.github.api', side_effect=problem):
                self.assertTrue(candidates.request_build(self.environment, self.event))
        for problem in (NotImplementedError('compression'), zlib.error('compressed data'), lzma.LZMAError('compressed data')):
            with patch.object(candidates, 'read_source', side_effect=problem):
                result, _, compare = self.request()
                self.assertTrue(result)
                compare.assert_not_called()
        result, _, compare = self.request(current='d' * 40)
        self.assertTrue(result)
        compare.assert_not_called()
        with patch.object(candidates, 'same_inputs', side_effect=OSError), \
                patch('ci_policy.github.api', side_effect=[{'workflow_runs': [self.run]}, {'artifacts': [self.artifact]}]), \
                patch.object(candidates.subprocess, 'check_output', return_value=self.archive()), \
                patch.object(candidates, 'git', return_value='a' * 40):
            self.assertTrue(candidates.request_build(self.environment, self.event))

    def test_skipped_enqueue_run_does_not_hide_the_last_built_candidate(self):
        newer = {**self.run, 'id': 21}
        with patch('ci_policy.github.api', side_effect=[{'workflow_runs': [self.run, newer]},
                   {'artifacts': []}, {'artifacts': [self.artifact]}]), \
                patch.object(candidates.subprocess, 'check_output', return_value=self.archive()), \
                patch.object(candidates, 'git', return_value='a' * 40), \
                patch.object(candidates, 'same_inputs', return_value=True):
            self.assertFalse(candidates.request_build(self.environment, self.event))

    def test_ready_opened_schedule_and_manual_requests_do_not_consult_history(self):
        for event, action in [('pull_request', 'opened'), ('pull_request', 'ready_for_review'),
                              ('schedule', None), ('workflow_dispatch', None)]:
            with self.subTest(event=event), patch('ci_policy.github.api') as api:
                self.assertTrue(candidates.request_build({**self.environment, 'GITHUB_EVENT_NAME': event}, dict(action=action)))
                api.assert_not_called()

    def test_source_record_and_request_cli_bind_checkout_and_attempt(self):
        with patch.object(candidates, 'git', side_effect=['b' * 40, 'c' * 40]):
            self.assertEqual(candidates.source_record({**self.environment, 'GITHUB_RUN_ID': '20',
                             'GITHUB_WORKFLOW_SHA': 'b' * 40}), self.record)
        with tempfile.TemporaryDirectory() as directory:
            source, event, output = [Path(directory) / name for name in ('source', 'event', 'output')]
            event.write_text(json.dumps(self.event))
            with patch.object(sys, 'argv', ['candidates', '--source-record', str(source)]), \
                    patch.object(candidates, 'source_record', return_value=self.record):
                self.assertEqual(candidates.main(), 0)
            self.assertEqual(json.loads(source.read_text()), self.record)
            with patch.object(sys, 'argv', ['candidates', '--request']), \
                    patch.dict(os.environ, GITHUB_EVENT_PATH=str(event), GITHUB_OUTPUT=str(output)), \
                    patch.object(candidates, 'request_build', return_value=False), \
                    patch.object(candidates, 'git', return_value='a' * 40):
                self.assertEqual(candidates.main(), 0)
            self.assertEqual(output.read_text(), 'build=false\nsource=' + 'a' * 40 + '\n')

    def test_packaging_comparison_uses_built_merge_tree_and_recursive_paths(self):
        previous = Path.cwd()
        with tempfile.TemporaryDirectory() as directory:
            os.chdir(directory)
            try:
                subprocess.run(['git', 'init', '-q'], check=True)
                path = Path('scripts/linux_release/candidates.py')
                path.parent.mkdir(parents=True)
                path.write_text('original')
                candidates.git('add', '.')
                candidates.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test', 'commit', '-qm', 'Initial')
                source = candidates.source_record(self.environment)
                Path('application.go').write_text('changed')
                candidates.git('add', '.')
                candidates.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test', 'commit', '-qm', 'Application')
                current = candidates.git('rev-parse', 'HEAD')
                self.assertTrue(candidates.same_inputs(source, current))
                path.write_text('packaging changed on main')
                candidates.git('add', '.')
                candidates.git('-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.test', 'commit', '-qm', 'Packaging')
                current = candidates.git('rev-parse', 'HEAD')
                self.assertFalse(candidates.same_inputs(source, current))
                self.assertFalse(candidates.same_inputs({**source, 'source_tree': 'd' * 40}, current))
            finally:
                os.chdir(previous)
