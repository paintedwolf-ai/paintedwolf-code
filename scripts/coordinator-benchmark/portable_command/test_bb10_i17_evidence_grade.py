"""BB10 I17 systemic invariant tests: evidence determines per-case grade, suite totals, and retained bundles."""
import contextlib
import copy
import json
import pathlib
import sqlite3
import tempfile
import unittest
from portable_command import grading, live, observe, reporting
from portable_command.contracts import bytes_value, load_suite, materialize
from portable_command.profile_session import explicit_effect_certainty
from portable_command.test_portable_command_harness import create_audit_db

SUITE = load_suite()
CASES = {c['id']: c for c in SUITE['cases']}
IDENT = {'git_head': 'a' * 40}


def _sql(db_path, script):
    with contextlib.closing(sqlite3.connect(db_path)) as db:
        db.executescript(script)


def _valid_first_mutation_db(root):
    case = CASES['first-mutation']
    proj, db_path, log_path = root / 'proj', root / 'store.db', root / 'sidecar.log'
    base = materialize(case, proj)
    for rel, body in case['expected']['files'].items():
        (proj / rel).write_bytes(bytes_value(body))
    create_audit_db(db_path)
    log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="shutdown signal received"\n')
    with contextlib.closing(sqlite3.connect(db_path)) as db:
        db.execute("INSERT INTO invocation_receipts VALUES ('r0','sess1234','c0','update_progress','progress','completed',1), ('r1','sess1234','c1','command','processes','completed',1)")
        db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
        db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,effect_certainty,accepted_revision,published_revision) VALUES ('e0','p1',0,'printf','printf','processes','completed','none','',''), ('e1','p1',1,'write','','filesystem','completed','completed','rev1','rev2')")
        db.execute("INSERT INTO invocation_program_revision_pins VALUES ('p1','e1',0,'destination','absent',?)", (next(iter(case['expected']['files'])),))
        db.execute("INSERT INTO invocation_program_publications VALUES ('p1','sess1234','committed',1)")
        db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (
            json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': ''})}),
        ))
        db.commit()
    turns = [{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c0', 'c1']}]
    return case, proj, base, db_path, log_path, turns


class BB10I17EvidenceGradeTests(unittest.TestCase):
    def test_one_field_at_a_time_mutation_matrix_in_grade_totals_and_retained_bundle(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            case, proj, base, db_path, log_path, turns = _valid_first_mutation_db(root / 'ok')
            obs, audit = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes=base, turn_records=turns, prelude_records=[])
            self.assertEqual(audit['observation_gaps'], [])
            grade = grading.grade_case(case, obs, proj, IDENT)
            self.assertTrue(grade['passed'])
            good_rec = {'case_id': case['id'], 'attempt': 'measured', 'qualification': grade['qualification'], 'passed': grade['passed'], 'observation_gaps': audit['observation_gaps']}
            disp = reporting.classify_case_disposition(good_rec, engine_provenance={'provenance_verified': True})
            self.assertTrue(disp['qualified_pass'] and disp['disposition'] == 'qualified')

            # Delete or corrupt ONE authoritative row/field at a time while keeping status=completed/0.
            def _run_defect(name, mutate):
                droot = root / name
                c, p, b, dbp, lp, t = _valid_first_mutation_db(droot)
                res = mutate(p, dbp, lp, t)
                extra_gaps = res if isinstance(res, list) else []
                o, a = observe.build_observation(c, session_id='sess1234', identity=IDENT, project_dir=p, db_path=dbp, sidecar_log=lp, baseline_hashes=b, turn_records=t, prelude_records=[], extra_gaps=extra_gaps)
                g = grading.grade_case(c, o, p, IDENT)
                rec = {'case_id': c['id'], 'attempt': 'measured', 'qualification': g['qualification'], 'passed': g['passed'], 'observation_gaps': a['observation_gaps']}
                d = reporting.classify_case_disposition(rec, engine_provenance={'provenance_verified': True})
                return o, a, g, {**rec, **d}

            mutations = [
                ('missing_sidecar_log', lambda p, dbp, lp, t: lp.unlink()),
                ('truncated_sidecar_log', lambda p, dbp, lp, t: lp.write_text('level=INFO msg="server listening"\n')),
                ('missing_destination_pin', lambda p, dbp, lp, t: _sql(dbp, "DELETE FROM invocation_program_revision_pins;")),
                ('missing_publication_commit', lambda p, dbp, lp, t: _sql(dbp, "UPDATE invocation_program_publications SET materialized=0;")),
                ('unknown_effect_certainty', lambda p, dbp, lp, t: _sql(dbp, "UPDATE invocation_program_executions SET effect_certainty='unknown';")),
                ('explicit_no_effect_on_write', lambda p, dbp, lp, t: _sql(dbp, "UPDATE invocation_program_executions SET effect_certainty='none', accepted_revision='', published_revision='';")),
                ('mixed_unexpected_invocation', lambda p, dbp, lp, t: _sql(dbp, "INSERT INTO invocation_receipts VALUES ('r9','sess1234','c9','command','processes','completed',1);")),
                ('failed_fixture_init_drafts', lambda p, dbp, lp, t: ['uninjected_drafts']),
                ('failed_fixture_init_host_facts', lambda p, dbp, lp, t: ['uninjected_host_facts']),
            ]
            for label, fn in mutations:
                with self.subTest(defect=label):
                    _, a, g, rec = _run_defect(label, fn)
                    self.assertFalse(rec['qualified_pass'], f'{label} must NEVER qualify as passing')
                    per_q, passed_sel, passed_full = reporting.aggregate_qualification(SUITE['cases'], [rec])
                    self.assertNotIn('Q01', passed_sel)
                    self.assertNotIn('Q01', passed_full)
                    self.assertEqual(per_q['Q01']['status'], 'not_passed')
                    breakdown = reporting.build_discrepancy_breakdown([rec])
                    self.assertEqual(breakdown['total_discrepancies'], 1)
                    if label in {'missing_sidecar_log', 'truncated_sidecar_log', 'failed_fixture_init_drafts', 'failed_fixture_init_host_facts'}:
                        self.assertEqual(rec['disposition'], 'unqualified')
                        self.assertTrue(a['observation_gaps'])
                    else:
                        self.assertFalse(g['passed'])

    def test_explicit_effect_certainty_rejects_unknown_missing_and_mismatched_counts(self):
        good_audit = {'invocation_program_executions': [{'id': 'e1', 'program_id': 'p1', 'state': 'refused', 'effect_certainty': 'none'}]}
        self.assertTrue(explicit_effect_certainty(good_audit, {'completed_effects': 0, 'uncertain_effects': 0}, program_id='p1', require_nonempty=True))
        for bad_audit, bad_payload in (
            ({'invocation_program_executions': [{'id': 'e1', 'program_id': 'p1', 'state': 'refused', 'effect_certainty': 'unknown'}]}, {'completed_effects': 0, 'uncertain_effects': 0}),
            ({'invocation_program_executions': [{'id': 'e1', 'program_id': 'p1', 'state': 'refused', 'effect_certainty': ''}]}, {'completed_effects': 0, 'uncertain_effects': 0}),
            ({'invocation_program_executions': []}, {'completed_effects': 0, 'uncertain_effects': 0}),
            (good_audit, {'completed_effects': 0, 'uncertain_effects': 1}),
            (good_audit, {'completed_effects': 1, 'uncertain_effects': 0}),
        ):
            self.assertFalse(explicit_effect_certainty(bad_audit, bad_payload, program_id='p1', require_nonempty=True))

    def test_wrong_handle_and_incomplete_child_set_never_pass_or_qualify(self):
        case = CASES['owned-background-state']
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            proj, db_path, log_path = root / 'proj', root / 'store.db', root / 'sidecar.log'
            proj.mkdir()
            create_audit_db(db_path)
            log_path.write_text('level=INFO msg="server listening"\nlevel=INFO msg="process boundary selected" host_execution=false process_control=false component=sandbox tool=command session_id=sess1234\nlevel=INFO msg="shutdown signal received"\n')
            db = sqlite3.connect(db_path)
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN job_handle TEXT DEFAULT ''")
            db.execute("ALTER TABLE invocation_program_executions ADD COLUMN status_code INTEGER DEFAULT 0")
            db.execute("INSERT INTO invocation_receipts VALUES ('r1','sess1234','c1','command','processes','completed',1)")
            db.execute("INSERT INTO invocation_programs VALUES ('p1','r1','completed',0,'')")
            db.execute("INSERT INTO invocation_program_executions (id,program_id,ordinal,operation_id,native_command,owner,state,status_code,effect_certainty,job_handle) VALUES ('e1','p1',1,'command','external','processes','completed',0,'unknown','bg-1')")
            db.execute("INSERT INTO messages VALUES ('m1','sess1234',1,'tool','c1',?)", (json.dumps({'outcome': 'completed', 'content': '[command#1]\n' + json.dumps({'program_id': 'p1', 'state': 'completed', 'status': 0, 'stdout': '', 'stderr': ''})}),))
            db.commit(); db.close()
            # Incomplete child set (missing wait) + wrong handle not rejected -> MUST fail.
            bad_probe = {'handles': ['bg-1'], 'call_ids': [], 'wrong_handle_rejected': False, 'cross_session_refused': False, 'other_session_usable': False, 'handle_outputs': {'bg-1': {'outcome': 'completed', 'returned_handle': 'bg-1', 'stdout': f'before\n{proj.resolve()}\n', 'running': False, 'exit_code': 0}}}
            obs, _ = observe.build_observation(case, session_id='sess1234', identity=IDENT, project_dir=proj, db_path=db_path, sidecar_log=log_path, baseline_hashes={}, turn_records=[{'program': case['turns'][0]['program'], 'command_call_id': 'c1', 'call_ids': ['c1'], 'job_probe': bad_probe}], prelude_records=[])
            g = grading.grade_case(case, obs, proj, IDENT)
            self.assertFalse(g['dimensions']['receipts'])
            self.assertFalse(g['dimensions']['evidence'])
            self.assertFalse(g['passed'])

    def test_dry_manifest_and_incomplete_live_campaign_cannot_qualify_live_matrix(self):
        manifest = live.load_live_manifest()
        source = {k: 'a' * 64 for k in live.SOURCE_FIELDS}
        models = [{
            'role': row['role'], 'selection': row['selection'], 'backend_kind': 'provider', 'backend_version': '1.0',
            'backend_artifact_sha256': 'b' * 64, 'resolved_model_id': row.get('resolved_id') or f"model-{row['role']}",
            'model_artifact_sha256': 'c' * 64, 'request_controls_sha256': 'd' * 64,
            'runtime_identity_verified': True, 'alias_dependent': False,
        } for row in manifest['models']]
        auth = {'allow_live': True, 'approval_reference': 'APR-1', 'isolated_host_reference': 'host-1', 'maximum_cost_usd': 5.0, 'allowed_resolved_models': [m['resolved_model_id'] for m in models]}
        plan = live.freeze_plan(manifest, source, models, auth)
        # Dry manifest (0 attempts): planned > 0, qualified is False, accepted is False.
        dry = live.summarize_slots(plan, [])
        self.assertGreater(dry['planned'], 0)
        self.assertFalse(dry['qualified'])
        self.assertFalse(dry['accepted'])
        # Incomplete campaign (1 unmeasured passed + 1 interrupted + rest pending): cannot qualify full matrix.
        partial = live.summarize_slots(plan, [
            {'slot': 0, 'plan_sha256': plan['plan_sha256'], 'episode': plan['episodes'][0], 'outcome': 'passed'},
            {'slot': 1, 'plan_sha256': plan['plan_sha256'], 'episode': plan['episodes'][1], 'outcome': 'interrupted'},
        ])
        self.assertFalse(partial['qualified'])
        self.assertFalse(partial['accepted'])
        self.assertEqual(partial['trials'][0]['outcome'], 'unqualified')
        self.assertEqual(partial['trials'][1]['outcome'], 'interrupted')
        self.assertEqual(partial['trials'][2]['outcome'], 'pending')
        # Tampered plan_sha256 raises ValueError.
        tampered = copy.deepcopy(plan)
        tampered['episodes'].pop()
        with self.assertRaises(ValueError):
            live.summarize_slots(tampered, [])

    def test_unmeasured_attempt_and_retained_bundle_preserve_unqualified_disposition(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            case_dir, bundle = root / 'case', root / 'bundle'
            (case_dir / 'raw').mkdir(parents=True)
            (case_dir / 'attempt.json').write_text(json.dumps({'status': 'unmeasured', 'case_id': 'first-mutation'}))
            (case_dir / 'grade.json').write_text(json.dumps({'passed': True, 'qualification': 'native-runtime'}))
            manifest = reporting.archive_case_evidence(case_dir, bundle, case_id='first-mutation')
            self.assertIn('attempt.json', manifest['files'])
            disp = reporting.classify_case_disposition({'case_id': 'first-mutation', 'attempt': 'unmeasured', 'qualification': 'native-runtime', 'passed': True, 'observation_gaps': []})
            self.assertEqual(disp['disposition'], 'unqualified')
            self.assertFalse(disp['qualified_pass'])
            self.assertIn('unmeasured_attempt', disp['unqualified_reasons'])
