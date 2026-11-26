"""Unit tests for the Portable Command Q01-Q22 suite runner, envelope probe, provenance, and Q aggregation."""
import io
import json
import pathlib
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
from portable_command import cli, harness, reporting, suite_runner
from portable_command.test_portable_command_harness import FakeApp, FakeSSE


class ProbeFakeApp(FakeApp):
    """Fake app with configurable offered command tool schema and envelope behavior."""
    def __init__(self, directory, *, schema_props, native_progress=False, reject=False, offer_command=True):
        super().__init__(directory, mode='publish')
        self.schema_props = schema_props
        self.native_progress = native_progress
        self.reject = reject
        self.offer_command = offer_command
        self.recorded_calls = []

    def request(self, path, body=None, missing=False, method=None):
        if path.endswith('/prompts'):
            tools = ['command', 'update_progress'] if self.offer_command else ['update_progress']
            schemas = [{'name': 'command', 'description': 'Run command', 'schema': {'type': 'object', 'properties': self.schema_props}}] if self.offer_command else []
            self.pending_req = {'pending': True, 'id': 'req-1', 'session_id': self.session_id, 'tools': tools, 'tool_schemas': schemas}
            return {'operation_id': body['operation_id']}
        return super().request(path, body=body, missing=missing, method=method)

    def _handle_tool_calls(self, calls):
        self.recorded_calls.extend(calls)
        db = sqlite3.connect(self.directory / 'store.db')
        for idx, tc in enumerate(calls, start=1):
            cid = tc['id']
            if self.reject:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','rejected',0)", ('rcpt-cmd', self.session_id, cid))
                res = {'outcome': 'rejected', 'codes': ['COMMAND_ARGS_INVALID'], 'content': json.dumps({'refusal': {'code': 'COMMAND_ARGS_INVALID'}})}
            else:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','completed',1)", ('rcpt-cmd', self.session_id, cid))
                if self.native_progress:
                    db.execute("INSERT INTO invocation_receipts VALUES ('rcpt-prog',?,?, 'update_progress','progress','completed',1)", (self.session_id, cid))
                res = {'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'status': 0, 'state': 'completed'})}
            db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, idx, cid, json.dumps(res)))
        db.commit()
        db.close()


def _write_fake_case_artifacts(out_dir, case_id, passed=True):
    out_dir = pathlib.Path(out_dir)
    (out_dir / 'raw').mkdir(parents=True, exist_ok=True)
    (out_dir / 'app').mkdir(parents=True, exist_ok=True)
    (out_dir / 'observation.json').write_text(json.dumps({'case_id': case_id}))
    (out_dir / 'grade.json').write_text(json.dumps({'passed': passed}))
    (out_dir / 'raw' / 'messages.json').write_text('{"messages":[]}')
    (out_dir / 'raw' / 'events.jsonl').write_text('{"topic":"activity"}\n')
    (out_dir / 'raw' / 'db-audit.json').write_text('{}')
    (out_dir / 'app' / 'sidecar.log').write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')


class PortableCommandSuiteRunnerTests(unittest.TestCase):
    def test_probe_command_envelope_reports_schema_and_never_uses_sibling_fallback(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            with patch.object(suite_runner, 'checkpoint_closed_store', return_value={}):
                app_no_prop = []
                def make_no_prop(d):
                    a = ProbeFakeApp(d, schema_props={'command': {'type': 'string'}}, native_progress=False, reject=False)
                    app_no_prop.append(a)
                    return a
                r1 = suite_runner.probe_command_envelope(root / 'p1', app_factory=make_no_prop, sse_factory=FakeSSE)
                r2 = suite_runner.probe_command_envelope(
                    root / 'p2',
                    app_factory=lambda d: ProbeFakeApp(d, schema_props={'command': {'type': 'string'}, 'progress_update': {'type': 'object'}}, native_progress=True),
                    sse_factory=FakeSSE,
                )
                r3 = suite_runner.probe_command_envelope(
                    root / 'p3',
                    app_factory=lambda d: ProbeFakeApp(d, schema_props={}, offer_command=False),
                    sse_factory=FakeSSE,
                )
            self.assertFalse(r1['schema_has_progress_update'])
            self.assertEqual(r1['envelope_status'], 'accepted_without_progress_receipt')
            self.assertFalse(r1['native_envelope_qualified'])
            self.assertFalse(r1['sibling_fallback_used'])
            self.assertEqual([c['name'] for c in app_no_prop[0].recorded_calls], ['command'])
            self.assertIn('progress_update', app_no_prop[0].recorded_calls[0]['args'])
            self.assertTrue((root / 'p1' / 'envelope-probe.json').is_file())
            self.assertTrue(r2['schema_has_progress_update'])
            self.assertEqual(r2['envelope_status'], 'supported')
            self.assertTrue(r2['native_envelope_qualified'])
            self.assertEqual(r3['envelope_status'], 'unavailable')
            self.assertFalse(r3['native_envelope_qualified'])

    def test_unqualified_disposition_durable_bundles_and_discrepancy_breakdown(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            def fake_runner(case, out_dir, *, export_dir=None, app_factory=None, sse_factory=None):
                _write_fake_case_artifacts(out_dir, case['id'], passed=(case['id'] == 'first-mutation'))
                if case['id'] == 'draft-read-transform':
                    raise RuntimeError('simulated setup failure')
                gaps = {
                    'read-only-static-refusal': ['profile_binding_approximate'],
                    'same-file-transform': ['incomplete_sidecar_log'],
                }.get(case['id'], [])
                return {
                    'case_id': case['id'], 'session_id': f"sess-{case['id']}",
                    'grade': {'qualification': 'native-runtime', 'passed': case['id'] == 'first-mutation', 'dimensions': {'artifacts': True}},
                    'audit': {'observation_gaps': gaps},
                }

            report = suite_runner.run_suite(
                root / 'suite',
                case_ids=['first-mutation', 'draft-read-transform', 'same-file-transform', 'native-option-no-fallback'],
                case_runner=fake_runner,
            )
            by_id = {c['case_id']: c for c in report['cases']}
            # first-mutation uses sibling_update_progress -> diagnostic pass, MUST be unqualified and not pass Q20.
            self.assertTrue(by_id['first-mutation']['passed'])
            self.assertEqual(by_id['first-mutation']['disposition'], 'unqualified')
            self.assertIn('sibling_progress_substitute', by_id['first-mutation']['unqualified_reasons'])
            self.assertFalse(by_id['first-mutation']['qualified_pass'])
            self.assertNotIn('Q20', report['summary']['qualification_cases_passed_selected'])
            self.assertNotIn('Q20', report['summary']['qualification_cases_passed_full'])
            # All 4 discrepancy categories populated accurately.
            self.assertEqual(report['discrepancy_breakdown']['cases']['proven_contract_mismatch'], ['first-mutation'])
            self.assertEqual(report['discrepancy_breakdown']['cases']['missing_setup'], ['draft-read-transform'])
            self.assertEqual(report['discrepancy_breakdown']['cases']['missing_observation'], ['same-file-transform'])
            self.assertEqual(report['discrepancy_breakdown']['cases']['runtime_issue'], ['native-option-no-fallback'])
            # Durable per-case bundles with locators and SHA-256 hashes.
            ev = by_id['native-option-no-fallback']['evidence']
            self.assertIn('raw/sidecar-slice.log', ev['files'])
            self.assertEqual(len(ev['files']['observation.json']['sha256']), 64)
            self.assertTrue(pathlib.Path(ev['files']['raw/sidecar-slice.log']['path']).is_file())

    def test_shared_q_id_requires_all_cases_qualified_and_distinguishes_partial_vs_full(self):
        # Q01 is shared by ['large-tree-discovery', 'oar-option-correction'].
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            def make_runner(oar_passed=True, oar_gaps=()):
                def runner(case, out_dir, *, export_dir=None, app_factory=None, sse_factory=None):
                    passed = True if case['id'] == 'large-tree-discovery' else oar_passed
                    gaps = [] if case['id'] == 'large-tree-discovery' else list(oar_gaps)
                    _write_fake_case_artifacts(out_dir, case['id'], passed=passed)
                    return {
                        'case_id': case['id'], 'session_id': f"s-{case['id']}",
                        'grade': {'qualification': 'native-runtime', 'passed': passed, 'dimensions': {'artifacts': passed}},
                        'audit': {'observation_gaps': gaps},
                    }
                return runner

            # One passing + one failing sharing Q01 -> Q01 does NOT pass.
            r_fail = suite_runner.run_suite(root / 's1', case_ids=['large-tree-discovery', 'oar-option-correction'], case_runner=make_runner(oar_passed=False))
            self.assertNotIn('Q01', r_fail['summary']['qualification_cases_passed_selected'])
            self.assertNotIn('Q01', r_fail['summary']['qualification_cases_passed_full'])
            # One passing + one unqualified sharing Q01 -> Q01 does NOT pass.
            r_unqual = suite_runner.run_suite(root / 's2', case_ids=['large-tree-discovery', 'oar-option-correction'], case_runner=make_runner(oar_passed=True, oar_gaps=['incomplete_sidecar_log']))
            self.assertNotIn('Q01', r_unqual['summary']['qualification_cases_passed_selected'])
            self.assertNotIn('Q01', r_unqual['summary']['qualification_cases_passed_full'])
            # Partial selection (only 1 of 2 Q01 cases) -> selected_partial only, NOT full.
            r_part = suite_runner.run_suite(root / 's3', case_ids=['large-tree-discovery'], case_runner=make_runner())
            self.assertEqual(r_part['summary']['selection_mode'], 'partial')
            self.assertIn('Q01', r_part['summary']['qualification_cases_passed_selected'])
            self.assertNotIn('Q01', r_part['summary']['qualification_cases_passed_full'])
            self.assertEqual(r_part['qualification_by_id']['Q01']['status'], 'passed_selected_partial')
            # Both Q01 cases selected and qualified-passing -> passed_full.
            r_both = suite_runner.run_suite(root / 's4', case_ids=['large-tree-discovery', 'oar-option-correction'], case_runner=make_runner())
            self.assertIn('Q01', r_both['summary']['qualification_cases_passed_full'])
            self.assertEqual(r_both['qualification_by_id']['Q01']['status'], 'passed_full')

    def test_engine_provenance_draft_injection_and_cli_modes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_bin = root / 'lycaon-dev'
            fake_bin.write_bytes(b'fake-engine-binary')
            prov = reporting.inspect_engine_provenance(engine_bin=fake_bin, evaluator_git_head='a' * 40)
            self.assertEqual(prov['executable_path'], str(fake_bin.resolve()))
            self.assertEqual(prov['build_source'], 'cli:--engine')
            self.assertEqual(len(prov['executable_sha256']), 64)

            calls = []
            class DraftApp:
                def request(self, path, body=None, method=None):
                    calls.append((method or 'POST', path, body))
                    return {'id': 'doc-1', 'revision': 1 if method != 'PUT' else 2}
            ok, bindings = harness.inject_drafts(DraftApp(), 'proj-1', 'root-1', {'a.txt': {'revision': 'draft-r2', 'bytes': 'hi\n'}})
            self.assertTrue(ok)
            self.assertEqual(bindings['a.txt']['revision'], 2)
            self.assertEqual(calls[0][1], '/v1/projects/proj-1/editor-documents')
            self.assertEqual(calls[1][0], 'PUT')

            with patch('sys.stdout', new=io.StringIO()):
                rc_probe = cli.main(
                    ['--probe-envelope', '--engine', str(fake_bin), '--out', str(root / 'probe')],
                    envelope_prober=lambda out, engine_bin=None: {
                        'session_id': 's-probe', 'schema_has_progress_update': False,
                        'command_schema_properties': ['command'], 'envelope_status': 'rejected',
                        'native_envelope_qualified': False, 'sibling_fallback_used': False,
                    },
                )
                rc_suite = cli.main(
                    ['--suite', '--cases', 'first-mutation', '--out', str(root / 's1')],
                    suite_runner=lambda out, case_ids=None, export_dir=None: {'summary': {'total': 1, 'failed': 1}},
                )
            self.assertEqual(rc_probe, 0)
            self.assertEqual(rc_suite, 1)
