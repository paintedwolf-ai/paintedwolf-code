"""Hosted preparation and retention keep the package, contract and evidence under administrator custody."""
import json
import os
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

from linux_release.hosted import installed as runtime
from linux_release.hosted.desktop import USER

MARKER = Path('/etc/paintedwolf-qualification.json')
ARTIFACT = {'id': 'painted-wolf-code_1.0.3_arm64.deb', 'installation': 'deb', 'architecture': 'arm64',
            'identity': {'sha256': 'c' * 64}}
PRODUCER = {'source': {'source_commit': '1' * 40, 'source_tree': '2' * 40}, 'partial': False,
            'qualification_authority': False, 'publication_authority': False,
            'provenance': {'run_id': 12, 'artifact_id': 34, 'controller_commit': 'f' * 40}}
VARIANT = {'id': 'ubuntu-24.04-100', 'release': {'version_id': '24.04'}, 'scales_percent': [100]}


def selected():
    return {'format_version': 1, 'controller_commit': 'a' * 40, 'controller_tree': 'b' * 40,
            'architecture': 'arm64', 'candidate_run_id': 12, 'candidate_artifact_id': 34,
            'candidate_controller_commit': 'f' * 40, 'source_commit': '1' * 40, 'source_tree': '2' * 40, 'partial': False}


def facts(**changes):
    return {'controller_commit': 'a' * 40, 'workflow_commit': 'e' * 40, 'github_run_id': '77', 'user': USER,
            'uid': 1001, 'display_protocol': 'x11', 'application_qualified': False, 'physical_gpu_qualified': False,
            'architecture': 'aarch64', **changes}


class ContextTests(unittest.TestCase):
    def observe(self, observed):
        account = SimpleNamespace(pw_uid=1001, pw_dir='/home/qa')
        with patch.object(runtime, 'validate_host') as host, patch.object(runtime, 'controller') as controller, \
                patch.object(runtime, 'release', return_value={'ID': 'ubuntu'}), \
                patch.object(runtime.pwd, 'getpwnam', return_value=account), \
                patch.dict(runtime.os.environ, {'GITHUB_SHA': 'e' * 40, 'GITHUB_RUN_ID': '77'}):
            result = runtime.context(selected(), Path('/controller'), observed)
        host.assert_called_once()
        self.assertEqual(host.call_args.args[1:], (os.getuid(), host.call_args.args[2], {'ID': 'ubuntu'}))
        controller.assert_called_once_with(Path('/controller'), selected())
        return result, account

    def test_the_separately_observed_desktop_must_match_the_selection_and_workflow(self):
        result, account = self.observe(facts())
        self.assertIs(result, account)
        for change in [{'controller_commit': 'b' * 40}, {'workflow_commit': 'a' * 40}, {'github_run_id': '78'},
                       {'user': 'root'}, {'uid': 0}, {'display_protocol': 'wayland'}, {'application_qualified': True},
                       {'physical_gpu_qualified': None}, {'architecture': 'x86_64'}]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                self.observe(facts(**change))


class CandidatePackageTests(unittest.TestCase):
    def package(self, candidate, artifacts, identity, producer=PRODUCER):
        with patch.object(runtime, 'read', side_effect=[{'final': True}, producer]), \
                patch.object(runtime, 'artifacts', return_value=artifacts), \
                patch.object(runtime, 'file_identity', return_value=identity):
            return runtime.candidate_package(selected(), candidate)

    def test_exactly_one_selected_debian_package_with_the_candidate_bytes_is_admitted(self):
        with tempfile.TemporaryDirectory() as temporary:
            candidate = Path(temporary)
            (candidate / 'packages').mkdir()
            source = candidate / 'packages' / ARTIFACT['id']
            source.write_bytes(b'deb')
            identity = {'sha256': 'c' * 64, 'bytes': 3}
            final, producer, artifact, path, actual = self.package(candidate, {'x': ARTIFACT}, identity)
            self.assertEqual((final, producer, artifact, path, actual), ({'final': True}, PRODUCER, ARTIFACT, source, identity))
            other = dict(ARTIFACT, id='other.deb')
            with self.assertRaises(ValueError):
                self.package(candidate, {'x': ARTIFACT, 'y': other}, identity)
            with self.assertRaises(ValueError):
                self.package(candidate, {'x': dict(ARTIFACT, architecture='amd64')}, identity)
            with self.assertRaises(ValueError):
                self.package(candidate, {'x': ARTIFACT}, {'sha256': 'd' * 64, 'bytes': 3})
            link = candidate / 'packages' / 'linked.deb'
            link.symlink_to(source)
            with self.assertRaises(ValueError):
                self.package(candidate, {'x': dict(ARTIFACT, id='linked.deb')}, identity)

    def test_a_producer_with_release_authority_or_another_source_is_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            for producer in [{**PRODUCER, 'publication_authority': True}, {**PRODUCER, 'partial': True},
                             {**PRODUCER, 'source': {'source_commit': '1' * 40, 'source_tree': '3' * 40}},
                             {**PRODUCER, 'provenance': {**PRODUCER['provenance'], 'run_id': 13}},
                             {**PRODUCER, 'provenance': {**PRODUCER['provenance'], 'controller_commit': 'a' * 40}}]:
                with self.subTest(producer=producer), self.assertRaises(ValueError):
                    self.package(Path(temporary), {'x': ARTIFACT}, {'sha256': 'c' * 64}, producer)


class Custody:
    """A disposable rehearsal root, install path, graphical home and candidate under the module's names."""

    def __init__(self, marker=None, variants=(VARIANT,), fields=('painted-wolf-code', 'arm64')):
        self.marker = marker
        self.variants, self.fields = list(variants), list(fields)

    def __enter__(self):
        self.temporary = tempfile.TemporaryDirectory()
        base = Path(self.temporary.name)
        self.root, self.install, self.home = base / 'rehearsal', base / 'painted-wolf-code', base / 'home'
        if self.marker is None:
            self.marker = {'format_version': 1, 'desktop': 'gnome', 'package': str(self.install)}
        self.home.mkdir()
        candidate = base / 'candidate'
        (candidate / 'packages').mkdir(parents=True)
        self.source = candidate / 'packages' / ARTIFACT['id']
        self.source.write_bytes(b'deb')
        self.identity = {'sha256': 'c' * 64, 'bytes': 3}
        self.catalog = {'environments': []}
        native = Path.lstat

        def lstat(path):
            if path == MARKER:
                return SimpleNamespace(st_mode=stat.S_IFREG | 0o644, st_uid=0)
            return native(path)

        def read(path):
            if path == MARKER:
                return self.marker
            if path == runtime.CATALOG:
                return self.catalog
            return json.loads(path.read_text())
        self.patches = [patch.object(runtime, 'ROOT', self.root), patch.object(runtime, 'INSTALL', self.install),
                        patch.object(runtime, 'context', return_value=SimpleNamespace(pw_uid=1001, pw_dir=str(self.home))),
                        patch.object(runtime, 'candidate_package',
                                     return_value=({'final': True}, PRODUCER, ARTIFACT, self.source, self.identity)),
                        patch.object(Path, 'lstat', lstat), patch.object(runtime, 'read', side_effect=read),
                        patch.object(runtime, 'file_identity', return_value=self.identity),
                        patch.object(runtime, 'command', side_effect=self.fields),
                        patch.object(runtime, 'requirements', return_value=self.variants),
                        patch.object(runtime.secrets, 'token_hex', return_value='9' * 32)]
        self.mocks = [item.start() for item in self.patches]
        return self

    def __exit__(self, *_):
        for item in self.patches:
            item.stop()
        self.temporary.cleanup()


class PrepareTests(unittest.TestCase):
    def test_preparation_copies_the_package_into_custody_and_writes_the_contract(self):
        with Custody() as custody:
            contract = runtime.prepare(selected(), Path('/controller'), custody.source.parent.parent, facts())
            target = custody.root / 'packages' / ARTIFACT['id']
            self.assertEqual(target.read_bytes(), b'deb')
            self.assertEqual(stat.S_IMODE(target.stat().st_mode), 0o444)
            self.assertEqual([call.args[0][1:] for call in custody.mocks[7].call_args_list],
                             [['--field', str(target), 'Package'], ['--field', str(target), 'Architecture']])
            self.assertEqual(custody.mocks[8].call_args.args[1:], ('ubuntu-floor-gnome-x11', 'windowing'))
            for name in ('final-artifacts.json', 'case-inputs.json', 'execution-request.json', 'contract.json'):
                self.assertEqual(stat.S_IMODE((custody.root / name).stat().st_mode), 0o444)
            self.assertEqual(json.loads((custody.root / 'contract.json').read_text()), contract)
            request = json.loads((custody.root / 'execution-request.json').read_text())
            self.assertEqual(request, {'format_version': 1, 'execution_nonce': '9' * 32, 'source_commit': '1' * 40,
                                       'driver_tree': 'b' * 40, 'artifact_id': ARTIFACT['id'],
                                       'environment_id': 'ubuntu-floor-gnome-x11', 'variant_id': VARIANT['id']})
            self.assertEqual(json.loads((custody.root / 'case-inputs.json').read_text())['native_packages']['current_package'], str(target))
            self.assertEqual(contract['output'], str(custody.home / 'installed-observations'))
            self.assertEqual(contract['bootstrap_output'], str(custody.home / 'native-bootstrap'))
            self.assertEqual((contract['snapshot'], contract['bootstrap_frozen'], contract['user']), (None, False, USER))
            self.assertEqual((contract['controller_commit'], contract['candidate_selection']), ('a' * 40, PRODUCER))

    def test_a_changed_marker_package_metadata_or_baseline_refuses_before_custody_files(self):
        for custody in [Custody(marker={'format_version': 1, 'desktop': 'kde', 'package': '/opt/painted-wolf-code'}),
                        Custody(fields=('painted-wolf-code', 'amd64')), Custody(fields=('other', 'arm64')),
                        Custody(variants=()), Custody(variants=(VARIANT, dict(VARIANT, id='second')))]:
            with custody, self.assertRaises(ValueError):
                runtime.prepare(selected(), Path('/controller'), Path('/candidate'), facts())
            self.assertFalse((custody.root / 'contract.json').exists())

    def test_an_account_that_already_owns_a_campaign_or_bootstrap_is_refused(self):
        for name in ('installed-observations', 'native-bootstrap'):
            with Custody() as custody:
                (custody.home / name).mkdir()
                with self.subTest(name=name), self.assertRaises(ValueError):
                    runtime.prepare(selected(), Path('/controller'), Path('/candidate'), facts())
                self.assertFalse((custody.root / 'contract.json').exists())


class RetainTests(unittest.TestCase):
    def rehearsal(self, base: Path, contract_changes=None):
        root, home, checkout = base / 'rehearsal', base / 'home', base / 'checkout'
        root.mkdir()
        home.mkdir()
        checkout.mkdir()
        contract = {'controller_commit': 'a' * 40, 'source_commit': '1' * 40, 'artifact_id': ARTIFACT['id'],
                    'output': str(home / 'installed-observations'), 'bootstrap_frozen': True, **(contract_changes or {})}
        (root / 'contract.json').write_text(json.dumps(contract))
        (root / 'final-artifacts.json').write_text(json.dumps({'final': True}))
        (root / 'execution-request.json').write_text(json.dumps({'execution_nonce': '9' * 32}))
        for name in ('bootstrap-supervision.json', 'bootstrap-invocation.json'):
            (root / name).write_text('{}')
        campaign = {'results': [{'cases': [{'status': 'passed'}, {'status': 'failed'}]},
                                {'cases': [{'status': 'unverified'}, {'status': 'passed'}]}]}
        catalog = {'environments': []}

        def read(path):
            if path == runtime.CATALOG:
                return catalog
            if path.name == 'campaign.json':
                return campaign
            return json.loads(path.read_text())
        patches = [patch.object(runtime, 'ROOT', root),
                   patch.object(runtime, 'context', return_value=SimpleNamespace(pw_uid=1001, pw_dir=str(home))),
                   patch.object(runtime, 'read', side_effect=read),
                   patch.object(runtime, 'limits', return_value={'max_files': 8, 'max_bytes': 1 << 20, 'max_file': 1 << 20, 'other': 1}),
                   patch.object(runtime, 'retain_tree'), patch.object(runtime, 'verify_execution_receipt'),
                   patch.object(runtime, 'admit', return_value={'accepted': False, 'reasons': ['failed']})]
        return patches, root, home, checkout, contract, catalog

    def test_retention_copies_bootstrap_and_leaf_evidence_read_only_and_reports_counts(self):
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            patches, root, home, checkout, contract, catalog = self.rehearsal(base)
            output = base / 'evidence'
            mocks = [item.start() for item in patches]
            try:
                report = runtime.retain(selected(), checkout, facts(), output)
            finally:
                for item in patches:
                    item.stop()
            self.assertEqual(report['case_counts'], {'passed': 2, 'failed': 1, 'unverified': 1})
            self.assertIs(report['complete_matrix_accepted'], False)
            self.assertEqual((report['selection'], report['desktop']), (selected(), facts()))
            for key in ('application_qualified', 'physical_gpu_qualified', 'publication_authority', 'qualification_authority'):
                self.assertIs(report[key], False)
            self.assertEqual(json.loads((output / 'rehearsal.json').read_text()), report)
            self.assertEqual(json.loads((output / 'admission.json').read_text()), {'accepted': False, 'reasons': ['failed']})
            self.assertEqual(json.loads((output / 'final-artifacts.json').read_text()), {'final': True})
            for name in ('bootstrap-supervision.json', 'bootstrap-invocation.json'):
                self.assertEqual((output / name).read_text(), '{}')
            self.assertEqual(json.loads((output / 'target' / 'execution-request.json').read_text()), {'execution_nonce': '9' * 32})
            for path in output.rglob('*'):
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o755 if path.is_dir() else 0o444, path)
            trees = [call.args[:2] for call in mocks[4].call_args_list]
            self.assertEqual(trees, [(root / 'bootstrap', output / 'bootstrap'),
                                     (Path(contract['output']), output / 'target' / 'leaf')])
            self.assertEqual(mocks[4].call_args_list[1].kwargs['limits'], runtime.Limits(max_files=8, max_bytes=1 << 20, max_file=1 << 20))
            mocks[5].assert_called_once_with(output / 'target', contract, '1' * 40, 'b' * 40)
            self.assertEqual(mocks[6].call_args.args[0], catalog)
            self.assertEqual(mocks[6].call_args.args[4], output / 'target' / 'leaf' / 'evidence')

    def test_a_foreign_contract_or_an_unsafe_evidence_path_refuses_before_copying(self):
        cases = [({'controller_commit': 'b' * 40}, 'evidence'), ({'source_commit': '0' * 40}, 'evidence'),
                 ({'bootstrap_frozen': False}, 'evidence'), ({'output': '/elsewhere'}, 'evidence'),
                 ({}, 'checkout/evidence'), ({}, 'home')]
        for changes, relative in cases:
            with tempfile.TemporaryDirectory() as temporary, self.subTest(changes=changes, output=relative):
                base = Path(temporary)
                patches, root, home, checkout, contract, catalog = self.rehearsal(base, changes)
                mocks = [item.start() for item in patches]
                try:
                    with self.assertRaises(ValueError):
                        runtime.retain(selected(), checkout, facts(), base / relative)
                finally:
                    for item in patches:
                        item.stop()
                mocks[4].assert_not_called()


class EntryTests(unittest.TestCase):
    def arguments(self, mode, directory, *extra):
        return [mode, '--selection', str(directory / 's.json'), '--checkout', '/controller', '--facts', str(directory / 'f.json'), *extra]

    def test_each_mode_dispatches_to_its_owner_with_its_required_path(self):
        from linux_release.hosted import bootstrap as supervisor, diagnostic
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            with patch.object(runtime, 'selection', return_value=selected()), patch.object(runtime, 'read', return_value={'facts': 1}), \
                    patch.object(runtime, 'context', return_value='account'), patch.object(supervisor, 'supervise') as supervise, \
                    patch.object(runtime, 'prepare') as prepare, patch.object(runtime, 'freeze') as freeze, \
                    patch.object(diagnostic, 'prepare') as diagnose:
                self.assertEqual(runtime.main(self.arguments('bootstrap', directory)), 0)
                supervise.assert_called_once_with(runtime.ROOT, selected(), 'account')
                self.assertEqual(runtime.main(self.arguments('prepare', directory, '--candidate', '/candidate')), 0)
                prepare.assert_called_once_with(selected(), Path('/controller'), Path('/candidate'), {'facts': 1})
                self.assertEqual(runtime.main(self.arguments('freeze', directory)), 0)
                freeze.assert_called_once_with(selected(), Path('/controller'), {'facts': 1})
                self.assertEqual(runtime.main(self.arguments('prepare-diagnostic', directory, '--candidate', '/candidate')), 0)
                diagnose.assert_called_once_with(selected(), Path('/controller'), Path('/candidate'), {'facts': 1})
                for mode in ('prepare', 'retain', 'prepare-diagnostic', 'retain-diagnostic'):
                    with self.subTest(mode=mode), self.assertRaises(SystemExit) as refused:
                        runtime.main(self.arguments(mode, directory))
                    self.assertEqual(refused.exception.code, 1)
            with patch.object(runtime, 'selection', return_value={**selected(), 'partial': True}), \
                    patch.object(runtime, 'read', return_value={}), patch.object(supervisor, 'supervise') as supervise:
                with self.assertRaises(SystemExit) as refused:
                    runtime.main(self.arguments('bootstrap', directory))
                self.assertEqual(refused.exception.code, 1)
                supervise.assert_not_called()


if __name__ == '__main__':
    unittest.main()
