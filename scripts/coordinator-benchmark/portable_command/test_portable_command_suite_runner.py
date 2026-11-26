"""Unit tests for the Portable Command Q01-Q22 suite runner, envelope probe, provenance, and Q aggregation."""
import hashlib
import io
import json
import pathlib
import sqlite3
import tempfile
import unittest
from unittest.mock import patch
from portable_command import cli, harness, observe, provenance, reporting, suite_runner
from portable_command.contracts import ROOT, load_suite
from portable_command.test_portable_command_harness import FakeApp, FakeSSE, create_audit_db


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
            pu = (tc.get('args') or {}).get('progress_update') or {}
            prog_text = pu.get('content', '')
            if self.reject:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','rejected',0)", (f'rcpt-{cid}', self.session_id, cid))
                res = {'outcome': 'rejected', 'codes': ['COMMAND_ARGS_INVALID'], 'content': json.dumps({'refusal': {'code': 'COMMAND_ARGS_INVALID'}})}
            elif self.native_progress and self.turn >= 1 and pu.get('expected_revision') != 1:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','rejected',0)", (f'rcpt-{cid}', self.session_id, cid))
                res = {'outcome': 'rejected', 'codes': ['PROGRESS_REVISION_REQUIRED'], 'content': json.dumps({'refusal': {'code': 'PROGRESS_REVISION_REQUIRED'}})}
            elif self.native_progress and '- [ ]' not in prog_text:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','rejected',0)", (f'rcpt-{cid}', self.session_id, cid))
                res = {'outcome': 'rejected', 'codes': ['PROGRESS_NO_OPEN_STEP'], 'content': json.dumps({'refusal': {'code': 'PROGRESS_NO_OPEN_STEP'}})}
            else:
                db.execute("INSERT INTO invocation_receipts VALUES (?,?,?,'command','processes','completed',1)", (f'rcpt-cmd-{cid}', self.session_id, cid))
                db.execute("INSERT INTO invocation_programs VALUES (?,?,'completed',0,'')", (f'prog-{cid}', f'rcpt-cmd-{cid}'))
                if self.native_progress:
                    db.execute("INSERT INTO invocation_program_progress VALUES (?,?,?,'committed',1,'')", (f'prog-exec-{cid}', f'prog-{cid}', self.session_id))
                res = {'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'status': 0, 'state': 'completed'})}
            db.execute("INSERT INTO messages VALUES (?,?,?,'tool',?,?)", (f'msg-{cid}', self.session_id, self.turn * 10 + idx, cid, json.dumps(res)))
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
    (out_dir / 'raw' / 'receipt-documents.json').write_text('{}')
    (out_dir / 'raw' / 'db-audit.json').write_text('{}')
    (out_dir / 'app' / 'sidecar.log').write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')


def _write_verified_engine(root, sha_override=None, path_override=None, head_override=None, tree_override=None):
    fake_bin = root / 'lycaon-dev'
    data = b'fake-engine-binary'
    fake_bin.write_bytes(data)
    git_head = head_override or provenance._git(ROOT, 'rev-parse', 'HEAD') or ('a' * 40)
    git_tree = tree_override or provenance._git(ROOT, 'rev-parse', 'HEAD^{tree}') or ('b' * 40)
    manifest = root / 'engine-manifest.json'
    manifest.write_text(json.dumps({
        'git_head': git_head, 'git_tree': git_tree,
        'executable_path': path_override or str(fake_bin),
        'executable_sha256': sha_override or hashlib.sha256(data).hexdigest(),
        'build_command': './task build:lycaon-dev', 'built_at': '2026-11-26T20:00:00Z',
    }))
    return fake_bin, manifest


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
            self.assertEqual([c['name'] for c in app_no_prop[0].recorded_calls], ['command', 'command'])
            self.assertIn('- [ ]', app_no_prop[0].recorded_calls[0]['args']['progress_update']['content'])
            self.assertNotIn('- [ ]', app_no_prop[0].recorded_calls[1]['args']['progress_update']['content'])
            self.assertTrue((root / 'p1' / 'envelope-probe.json').is_file())
            self.assertTrue(r2['schema_has_progress_update'])
            self.assertEqual(r2['envelope_status'], 'supported')
            self.assertTrue(r2['native_envelope_qualified'])
            self.assertTrue(r2['independent_receipts'])
            self.assertEqual(r2['all_checked_refusal_code'], 'PROGRESS_NO_OPEN_STEP')
            self.assertTrue(r2['all_checked_control']['benign_refusal_verified'])
            self.assertEqual(r3['envelope_status'], 'unavailable')
            self.assertFalse(r3['native_envelope_qualified'])

    def test_unqualified_disposition_relative_bundles_and_discrepancy_breakdown(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_bin, manifest = _write_verified_engine(root)
            def fake_runner(case, out_dir, **kwargs):
                _write_fake_case_artifacts(out_dir, case['id'], passed=(case['id'] == 'first-mutation'))
                if case['id'] == 'draft-read-transform':
                    raise RuntimeError('simulated setup failure')
                gaps = {'same-file-transform': ['missing_observation:bounded_visits:filesystem_owner']}.get(case['id'], [])
                batches = [{'envelope_mode': 'sibling_update_progress'}] if case['id'] == 'first-mutation' else []
                return {
                    'case_id': case['id'], 'session_id': f"sess-{case['id']}",
                    'grade': {'qualification': 'native-runtime', 'passed': case['id'] == 'first-mutation', 'dimensions': {'artifacts': True}},
                    'audit': {'observation_gaps': gaps, 'turn_batches': batches},
                }

            report = suite_runner.run_suite(
                root / 'suite',
                case_ids=['first-mutation', 'draft-read-transform', 'same-file-transform', 'native-option-no-fallback'],
                engine_bin=fake_bin, engine_manifest=manifest, case_runner=fake_runner,
            )
            by_id = {c['case_id']: c for c in report['cases']}
            self.assertEqual(by_id['first-mutation']['disposition'], 'unqualified')
            self.assertIn('sibling_progress_substitute', by_id['first-mutation']['unqualified_reasons'])
            self.assertEqual(by_id['same-file-transform']['disposition'], 'unqualified')
            self.assertEqual(by_id['same-file-transform']['discrepancy_category'], 'missing_observation')
            self.assertEqual(report['discrepancy_breakdown']['cases']['proven_contract_mismatch'], ['first-mutation'])
            self.assertEqual(report['discrepancy_breakdown']['cases']['missing_setup'], ['draft-read-transform'])
            self.assertEqual(report['discrepancy_breakdown']['cases']['missing_observation'], ['same-file-transform'])
            self.assertEqual(report['discrepancy_breakdown']['cases']['runtime_issue'], ['native-option-no-fallback'])
            # Portable relative bundle locators (not absolute host paths) + receipt-documents.json retained.
            ev = by_id['native-option-no-fallback']['evidence']
            self.assertEqual(ev['bundle_dir'], 'native-option-no-fallback')
            self.assertEqual(ev['files']['raw/sidecar-slice.log']['path'], 'native-option-no-fallback/raw/sidecar-slice.log')
            self.assertEqual(ev['files']['raw/receipt-documents.json']['path'], 'native-option-no-fallback/raw/receipt-documents.json')
            self.assertFalse(pathlib.Path(ev['files']['observation.json']['path']).is_absolute())
            self.assertTrue((pathlib.Path(report['durable_bundle_root']) / ev['files']['raw/sidecar-slice.log']['path']).is_file())

    def test_missing_observation_fact_emits_named_gap_and_never_defaults_to_runtime_issue(self):
        cases = {c['id']: c for c in load_suite()['cases']}
        case = cases['archive-create-extract']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path = root / 'store.db'
            create_audit_db(db_path)
            log_path = root / 'sidecar.log'
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            proj = root / 'proj'
            proj.mkdir()
            _, audit = observe.build_observation(
                case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj,
                db_path=db_path, sidecar_log=log_path, turn_records=[], prelude_records=[],
            )
            self.assertIn('missing_observation:confined_archive_entries:archive_owner', audit['observation_gaps'])
            disp = reporting.classify_case_disposition(
                {'attempt': 'measured', 'qualification': 'native-runtime', 'passed': False, 'observation_gaps': audit['observation_gaps']},
                engine_provenance={'provenance_verified': True},
            )
            self.assertEqual(disp['disposition'], 'unqualified')
            self.assertEqual(disp['discrepancy_category'], 'missing_observation')

    def test_shared_q_id_and_verified_vs_unverified_engine_manifest(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_bin, good_manifest = _write_verified_engine(root)
            gm = json.loads(good_manifest.read_text())
            good_prov = reporting.inspect_engine_provenance(
                engine_bin=fake_bin, evaluator_git_head=gm['git_head'], evaluator_git_tree=gm['git_tree'], engine_manifest=good_manifest,
            )
            self.assertTrue(good_prov['provenance_verified'])
            self.assertTrue(good_prov['manifest_sha256_match'])
            self.assertTrue(good_prov['manifest_path_match'])
            self.assertTrue(good_prov['manifest_head_match'])
            self.assertTrue(good_prov['manifest_tree_match'])
            for idx, overrides in enumerate([
                {'sha_override': '0' * 64}, {'path_override': '/nonexistent/lycaon-dev'},
                {'head_override': 'c' * 40}, {'tree_override': 'd' * 40},
            ]):
                sub = root / f'bad-{idx}'
                sub.mkdir()
                _, bad_m = _write_verified_engine(sub, **overrides)
                bad_prov = reporting.inspect_engine_provenance(
                    engine_bin=fake_bin, evaluator_git_head=gm['git_head'], evaluator_git_tree=gm['git_tree'], engine_manifest=bad_m,
                )
                with self.subTest(mismatch=overrides):
                    self.assertFalse(bad_prov['provenance_verified'])
            bad_manifest = root / 'bad-0' / 'engine-manifest.json'

            def make_runner(oar_passed=True, oar_gaps=()):
                def runner(case, out_dir, **kwargs):
                    passed = True if case['id'] == 'large-tree-discovery' else oar_passed
                    gaps = [] if case['id'] == 'large-tree-discovery' else list(oar_gaps)
                    _write_fake_case_artifacts(out_dir, case['id'], passed=passed)
                    return {
                        'case_id': case['id'], 'session_id': f"s-{case['id']}",
                        'grade': {'qualification': 'native-runtime', 'passed': passed, 'dimensions': {'artifacts': passed}},
                        'audit': {'observation_gaps': gaps},
                    }
                return runner

            r_fail = suite_runner.run_suite(root / 's1', case_ids=['large-tree-discovery', 'oar-option-correction'], engine_bin=fake_bin, engine_manifest=good_manifest, case_runner=make_runner(oar_passed=False))
            self.assertNotIn('Q01', r_fail['summary']['qualification_cases_passed_selected'])
            r_unqual = suite_runner.run_suite(root / 's2', case_ids=['large-tree-discovery', 'oar-option-correction'], engine_bin=fake_bin, engine_manifest=good_manifest, case_runner=make_runner(oar_passed=True, oar_gaps=['incomplete_sidecar_log']))
            self.assertNotIn('Q01', r_unqual['summary']['qualification_cases_passed_selected'])
            r_part = suite_runner.run_suite(root / 's3', case_ids=['large-tree-discovery'], engine_bin=fake_bin, engine_manifest=good_manifest, case_runner=make_runner())
            self.assertIn('Q01', r_part['summary']['qualification_cases_passed_selected'])
            self.assertNotIn('Q01', r_part['summary']['qualification_cases_passed_full'])
            r_both = suite_runner.run_suite(root / 's4', case_ids=['large-tree-discovery', 'oar-option-correction'], engine_bin=fake_bin, engine_manifest=good_manifest, case_runner=make_runner())
            self.assertIn('Q01', r_both['summary']['qualification_cases_passed_full'])
            # Mismatched engine manifest prevents qualification even if all cases passed diagnostically.
            r_bad_prov = suite_runner.run_suite(root / 's5', case_ids=['large-tree-discovery', 'oar-option-correction'], engine_bin=fake_bin, engine_manifest=bad_manifest, case_runner=make_runner())
            self.assertNotIn('Q01', r_bad_prov['summary']['qualification_cases_passed_full'])
            self.assertIn('unverified_engine_provenance', r_bad_prov['cases'][0]['unqualified_reasons'])

    def test_draft_injection_and_cli_manifest_flag(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            fake_bin, manifest = _write_verified_engine(root)
            calls = []
            class DraftApp:
                def request(self, path, body=None, method=None):
                    calls.append((method or 'POST', path, body))
                    return {'id': 'doc-1', 'revision': 1 if method != 'PUT' else 2}
            ok, bindings = harness.inject_drafts(DraftApp(), 'proj-1', 'root-1', {'a.txt': {'revision': 'draft-r2', 'bytes': 'hi\n'}})
            self.assertTrue(ok)
            self.assertEqual(bindings['a.txt']['revision'], 2)
            with patch('sys.stdout', new=io.StringIO()):
                seen = {}
                def prober(out, engine_bin=None, engine_manifest=None):
                    seen.update(engine_bin=engine_bin, engine_manifest=engine_manifest)
                    return {
                        'session_id': 's-probe', 'schema_has_progress_update': False,
                        'command_schema_properties': ['command'], 'envelope_status': 'rejected',
                        'native_envelope_qualified': False, 'sibling_fallback_used': False,
                    }
                self.assertEqual(cli.main(['--probe-envelope', '--engine', str(fake_bin), '--engine-manifest', str(manifest), '--out', str(root / 'p')], envelope_prober=prober), 0)
            self.assertEqual(seen['engine_manifest'], manifest)

    def test_q21_pre_exec_refusal_recovery_and_stderr_separation(self):
        cases = {c['id']: c for c in load_suite()['cases']}
        case = cases['native-option-no-fallback']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path = root / 'store.db'
            create_audit_db(db_path)
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO invocation_receipts VALUES ('rcpt-1','sess1234','call-1','command','processes','rejected',0)")
            db.execute("INSERT INTO invocation_programs VALUES ('prog-1','rcpt-1','rejected',2,'COMMAND_OPTION_UNSUPPORTED')")
            payload = {
                'program_id': 'prog-1', 'state': 'rejected', 'status': 2, 'stop_code': 'COMMAND_OPTION_UNSUPPORTED',
                'stdout': '', 'stderr': 'cat: request: Supported form: catalogued option [COMMAND_OPTION_UNSUPPORTED]\n',
                'completed_effects': 0, 'uncertain_effects': 0, 'native_gaps': 0, 'replay_available': False,
                'refusal': {'code': 'COMMAND_OPTION_UNSUPPORTED', 'details': {
                    'source_start_byte': 4, 'source_end_byte': 28, 'expected_form': 'cat [-n] [FILE...]', 'program_replay_available': True,
                }},
            }
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','call-1',?)", (json.dumps({'outcome': 'rejected', 'content': '[command#1]\n' + json.dumps(payload)}),))
            db.commit()
            db.close()
            log_path = root / 'sidecar.log'
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            proj = root / 'proj'
            proj.mkdir()
            obs, audit = observe.build_observation(
                case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj,
                db_path=db_path, sidecar_log=log_path,
                turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'call-1', 'call_ids': ['call-1']}],
                prelude_records=[],
            )
            self.assertEqual(obs['program']['stderr'], '')
            self.assertIn('COMMAND_OPTION_UNSUPPORTED', audit['refusal_stderr'])
            self.assertTrue(obs['recovery']['whole_program_resubmit_allowed'])
            self.assertTrue(obs['facts']['no_external_launch'])
            # Omitting authoritative program_replay_available / replay_available leaves disposition None.
            omitted = observe._extract_recovery(case['id'], {'refusal': {'code': 'COMMAND_OPTION_UNSUPPORTED', 'details': {'source_start_byte': 4, 'source_end_byte': 28}}}, {}, {'invocation_program_executions': []}, [])
            self.assertIsNone(omitted['whole_program_resubmit_allowed'])

    def test_syntax_with_progress_no_effect_witness_and_progress_then_denial_facts(self):
        cases = {c['id']: c for c in load_suite()['cases']}
        syn_case = cases['syntax-with-progress']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            db_path = root / 'store.db'
            create_audit_db(db_path)
            db = sqlite3.connect(db_path)
            db.execute("CREATE TABLE session_progress (session_id TEXT PRIMARY KEY, document_version INTEGER)")
            db.execute("INSERT INTO invocation_receipts VALUES ('rcpt-syn','sess1234','call-1','command','processes','rejected',0)")
            raw_res = {
                'outcome': 'rejected', 'codes': ['COMMAND_SYNTAX_INVALID'], 'content': 'Rejected: syntax',
                'invocation': {'id': 'rcpt-syn', 'status': 'rejected', 'invoked': False, 'failure': {'code': 'COMMAND_SYNTAX_INVALID', 'retryable': False}},
                'feedback': [{'code': 'COMMAND_SYNTAX_INVALID', 'details': {
                    'command_execution': 'not_started', 'diagnostic_pre_evaluation': True, 'expected_form': 'complete Bash source',
                    'retryable': False, 'source_start_byte': 15, 'source_end_byte': 16,
                }}],
            }
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','call-1',?)", (json.dumps(raw_res),))
            db.commit()
            db.close()
            log_path = root / 'sidecar.log'
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
            proj = root / 'proj'
            proj.mkdir()
            obs, audit = observe.build_observation(
                syn_case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj,
                db_path=db_path, sidecar_log=log_path, baseline_hashes={},
                turn_records=[{'program': syn_case['turns'][0]['program'], 'command_call_id': 'call-1', 'call_ids': ['call-1']}],
                prelude_records=[],
            )
            self.assertEqual(audit['observation_gaps'], [])
            self.assertEqual(obs['program']['status'], 2)
            self.assertFalse(obs['recovery']['whole_program_resubmit_allowed'])
            self.assertTrue(obs['facts']['zero_progress_effects'])
            self.assertTrue(obs['facts']['zero_program_effects'])
            self.assertTrue(obs['facts']['syntax_span'])
            # Independent session_progress mutation witness breaks zero_progress_effects.
            db = sqlite3.connect(db_path)
            db.execute("INSERT INTO session_progress VALUES ('sess1234', 1)")
            db.commit()
            db.close()
            obs2, _ = observe.build_observation(
                syn_case, session_id='sess1234', identity={'git_head': 'a' * 40}, project_dir=proj,
                db_path=db_path, sidecar_log=log_path, baseline_hashes={},
                turn_records=[{'program': syn_case['turns'][0]['program'], 'command_call_id': 'call-1', 'call_ids': ['call-1']}],
                prelude_records=[],
            )
            self.assertFalse(obs2['facts']['zero_progress_effects'])
